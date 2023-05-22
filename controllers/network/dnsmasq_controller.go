/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package network

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	dnsmasq "github.com/openstack-k8s-operators/infra-operator/pkg/dnsmasq"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	configmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	deployment "github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	endpoint "github.com/openstack-k8s-operators/lib-common/modules/common/endpoint"
	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

// DNSMasqReconciler reconciles a DNSMasq object
type DNSMasqReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs/finalizers,verbs=update
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdatas,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *DNSMasqReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	_ = log.FromContext(ctx)

	// Fetch the DNSMasq instance
	instance := &networkv1.DNSMasq{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		r.Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// update the Ready condition based on the sub conditions
		if instance.Status.Conditions.AllSubConditionIsTrue() {
			instance.Status.Conditions.MarkTrue(
				condition.ReadyCondition, condition.ReadyMessage)
		} else {
			// something is not ready so reset the Ready condition
			instance.Status.Conditions.MarkUnknown(
				condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage)
			// and recalculate it based on the state of the rest of the conditions
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}

		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) {
		return ctrl.Result{}, nil
	}

	// initialize status
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}

		cl := condition.CreateList(
			condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
			condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
			condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
			// service account, role, rolebinding conditions
			condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
			condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
			condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
		)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		return ctrl.Result{}, nil
	}
	if instance.Status.Hash == nil {
		instance.Status.Hash = map[string]string{}
	}

	// Service account, role, binding
	rbacRules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{"security.openshift.io"},
			ResourceNames: []string{"anyuid"},
			Resources:     []string{"securitycontextconstraints"},
			Verbs:         []string{"use"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"create", "get", "list", "watch", "update", "patch", "delete"},
		},
	}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DNSMasqReconciler) SetupWithManager(mgr ctrl.Manager) error {

	dnsmasqFN := handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
		result := []reconcile.Request{}

		// For each ConfigMap create / update event get the list of all
		// DNSMasq to trigger reconcile for the one in the same namespace
		dnsmasqs := &networkv1.DNSMasqList{}

		listOpts := []client.ListOption{
			client.InNamespace(o.GetNamespace()),
		}
		if err := r.Client.List(context.Background(), dnsmasqs, listOpts...); err != nil {
			r.Log.Error(err, "Unable to retrieve DNSMasqList %w")
			return nil
		}

		// For each DNSMasq instance create a reconcile request
		for _, i := range dnsmasqs.Items {
			name := client.ObjectKey{
				Namespace: o.GetNamespace(),
				Name:      i.Name,
			}
			result = append(result, reconcile.Request{NamespacedName: name})
		}
		if len(result) > 0 {
			r.Log.Info(fmt.Sprintf("Reconcile request for: %+v", result))

			return result
		}
		return nil
	})

	// 'UpdateFunc' and 'CreateFunc' used to judge if a event about the object is
	// what we want. If that is true, the event will be processed by the reconciler.
	p := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// The object doesn't contain label networkv1.DNSDataLabelSelectorKey, so the event will be
			// ignored.
			if _, ok := e.ObjectOld.GetLabels()[networkv1.DNSDataLabelSelectorKey]; !ok {
				return false
			}

			return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			if _, ok := e.Object.GetLabels()[networkv1.DNSDataLabelSelectorKey]; !ok {
				return false
			}

			return true
		},
		CreateFunc: func(e event.CreateEvent) bool {
			if _, ok := e.Object.GetLabels()[networkv1.DNSDataLabelSelectorKey]; !ok {
				return false
			}

			return true
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.DNSMasq{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&source.Kind{Type: &corev1.ConfigMap{}},
			dnsmasqFN,
			builder.WithPredicates(p)).
		Complete(r)
}

func (r *DNSMasqReconciler) reconcileDelete(ctx context.Context, instance *networkv1.DNSMasq, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service delete")

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	r.Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

func (r *DNSMasqReconciler) reconcileNormal(ctx context.Context, instance *networkv1.DNSMasq, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service")

	serviceLabels := map[string]string{
		common.AppSelector: dnsmasq.ServiceName,
	}

	serviceAnnotations := map[string]string{}
	configMapVars := make(map[string]env.Setter)

	// create Configmap for dnsmasq input
	err := r.generateServiceConfigMaps(ctx, helper, instance, &configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	labelSelectorMap := map[string]string{networkv1.DNSDataLabelSelectorKey: strings.ToLower(instance.Spec.DNSDataLabelSelectorValue)}
	configMaps := &corev1.ConfigMapList{}
	listOpts := []client.ListOption{
		client.InNamespace(instance.GetNamespace()),
		client.MatchingLabels(labelSelectorMap),
	}
	err = r.List(ctx, configMaps, listOpts...)
	if err != nil {
		err = fmt.Errorf("error listing configmaps for labels: %v - %w", labelSelectorMap, err)
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	// Sort the ConfigMaps alphabetically by name
	sort.Slice(configMaps.Items, func(i, j int) bool {
		return configMaps.Items[i].Name < configMaps.Items[j].Name
	})

	cmNames := []string{}
	for _, cm := range configMaps.Items {
		hash, err := configmap.Hash(&cm)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.InputReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, fmt.Errorf("error calculating configuration hash: %w", err)
		}
		configMapVars[cm.Name] = env.SetValue(hash)
		cmNames = append(cmNames, cm.GetName())
	}
	r.Log.Info(fmt.Sprintf("ConfigMaps providing host information: %s", cmNames))
	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, condition.InputReadyMessage)

	// create hash over all the different input resources to identify if any of
	// those changed and a restart/recreate is required.
	inputHash, err := util.HashOfInputHashes(configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	instance.Status.Hash[common.InputHashName] = inputHash
	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	// expose the service (create service, route and return the created endpoint URLs)
	var dnsEndpointCfg = map[endpoint.Endpoint]endpoint.Data{}

	protocolUDP := corev1.ProtocolUDP
	for _, metallbcfg := range instance.Spec.ExternalEndpoints {
		name := fmt.Sprintf("%s-%s", instance.Name, metallbcfg.IPAddressPool)
		portCfg := endpoint.Data{
			MetalLB: &endpoint.MetalLBData{
				IPAddressPool:   metallbcfg.IPAddressPool,
				SharedIP:        metallbcfg.SharedIP,
				SharedIPKey:     metallbcfg.SharedIPKey,
				LoadBalancerIPs: metallbcfg.LoadBalancerIPs,
				Protocol:        &protocolUDP,
			},
			Port: dnsmasq.DNSPort,
		}

		dnsEndpointCfg[endpoint.Endpoint(name)] = portCfg
	}

	_, ctrlResult, err := endpoint.ExposeEndpoints(
		ctx,
		helper,
		dnsmasq.ServiceName,
		serviceLabels,
		dnsEndpointCfg,
		time.Duration(5)*time.Second,
	)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ExposeServiceReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.ExposeServiceReadyRunningMessage))
		return ctrlResult, nil
	}
	instance.Status.Conditions.MarkTrue(condition.ExposeServiceReadyCondition, condition.ExposeServiceReadyMessage)

	// Define a new Deployment object
	deplDef := dnsmasq.Deployment(instance, instance.Status.Hash[common.InputHashName], serviceLabels, serviceAnnotations, configMaps)
	depl := deployment.NewDeployment(
		deplDef,
		time.Duration(5)*time.Second,
	)

	ctrlResult, err = depl.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.DeploymentReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		return ctrlResult, nil
	}
	instance.Status.ReadyCount = depl.GetDeployment().Status.ReadyReplicas

	if instance.Status.ReadyCount > 0 {
		r.Log.Info(fmt.Sprintf("Deployment is ready: %s", instance.Name))
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
	} else {
		r.Log.Info(fmt.Sprintf("Deployment is not ready: %s - %+v", instance.Name, depl.GetDeployment().Status))
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		// It is OK to return success as we are watching for StatefulSet changes
		return ctrl.Result{}, nil
	}
	// create Deployment - end

	r.Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// generateServiceConfigMaps - create create configmaps which hold service configuration
func (r *DNSMasqReconciler) generateServiceConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *networkv1.DNSMasq,
	envVars *map[string]env.Setter,
) error {
	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(dnsmasq.ServiceName), map[string]string{})

	configMapData := map[string]string{}

	var cfg string
	for _, option := range instance.Spec.Options {
		cfg += option.Key + "="
		values := strings.Join(option.Values, ",")
		cfg += values + "\n"
	}
	configMapData[instance.Name] = cfg

	cms := []util.Template{
		{
			Name:         strings.ToLower(instance.Name),
			Namespace:    instance.Namespace,
			Type:         util.TemplateTypeNone,
			InstanceType: instance.Kind,
			CustomData:   configMapData,
			Labels:       cmLabels,
		},
	}

	return configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars)
}
