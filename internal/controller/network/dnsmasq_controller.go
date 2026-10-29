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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	dnsmasq "github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	annotations "github.com/openstack-k8s-operators/lib-common/modules/common/annotations"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	configmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	deployment "github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	service "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"github.com/openstack-k8s-operators/lib-common/modules/common/webhook"
)

// DNSMasqReconciler reconciles a DNSMasq object
type DNSMasqReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// fields to index to reconcile on CR change
const (
	topologyField = ".spec.topologyRef.Name"
)

var (
	allWatchFields = []string{
		topologyField,
	}
)

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *DNSMasqReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("DNSMasq")
}

// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdatas,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups=topology.openstack.org,resources=topologies,verbs=get;list;watch;update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *DNSMasqReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the DNSMasq instance
	instance := &networkv1.DNSMasq{}
	err := r.Get(ctx, req.NamespacedName, instance)
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
		Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// initialize status if Conditions is nil, but do not reset if it already
	// exists
	isNewInstance := instance.Status.Conditions == nil
	if isNewInstance {
		instance.Status.Conditions = condition.Conditions{}
	}

	// Save a copy of the condtions so that we can restore the LastTransitionTime
	// when a condition's state doesn't change.
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Always patch the instance status when exiting this function so we can
	// persist any changes.
	defer func() {
		// Don't update the status, if reconciler Panics
		if r := recover(); r != nil {
			Log.Info(fmt.Sprintf("panic during reconcile %v\n", r))
			panic(r)
		}
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
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	// initialize status
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
		condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		// service account, role, rolebinding conditions
		condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
		condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
		condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, nil
	}

	if instance.Status.Hash == nil {
		instance.Status.Hash = map[string]string{}
	}

	// Init Topology condition if there's a reference
	if instance.Spec.TopologyRef != nil {
		c := condition.UnknownCondition(condition.TopologyReadyCondition, condition.InitReason, condition.TopologyReadyInitMessage)
		cl.Set(c)
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Service account, role, binding.
	// No securitycontextconstraints "use" grant: dnsmasq runs under the
	// default restricted-v2 SCC (random UID), which needs no explicit grant.
	rbacRules := []rbacv1.PolicyRule{}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DNSMasqReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	Log := r.GetLogger(ctx)
	dnsmasqFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		result := []reconcile.Request{}

		// For each ConfigMap create / update event get the list of all
		// DNSMasq to trigger reconcile for the one in the same namespace
		dnsmasqs := &networkv1.DNSMasqList{}

		listOpts := []client.ListOption{
			client.InNamespace(o.GetNamespace()),
		}
		if err := r.List(ctx, dnsmasqs, listOpts...); err != nil {
			Log.Error(err, "Unable to retrieve DNSMasqList")
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
			Log.Info("Reconcile request for:", "result", result)

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

	// index topologyField
	if err := mgr.GetFieldIndexer().IndexField(ctx, &networkv1.DNSMasq{}, topologyField, func(rawObj client.Object) []string {
		// Extract the topology name from the spec, if one is provided
		cr := rawObj.(*networkv1.DNSMasq)
		if cr.Spec.TopologyRef == nil {
			return nil
		}
		return []string{cr.Spec.TopologyRef.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.DNSMasq{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1.ConfigMap{},
			dnsmasqFN,
			builder.WithPredicates(p)).
		Watches(&topologyv1.Topology{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// findObjectsForSrc - returns a reconcile request if the object is referenced by a DNSMasq CR
func (r *DNSMasqReconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	Log := r.GetLogger(ctx)

	for _, field := range allWatchFields {
		crList := &networkv1.DNSMasqList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(field, src.GetName()),
			Namespace:     src.GetNamespace(),
		}
		err := r.List(ctx, crList, listOps)
		if err != nil {
			Log.Error(err, fmt.Sprintf("listing %s for field: %s - %s", crList.GroupVersionKind().Kind, field, src.GetNamespace()))
			return requests
		}

		for _, item := range crList.Items {
			Log.Info(fmt.Sprintf("input source %s changed, reconcile: %s - %s", src.GetName(), item.GetName(), item.GetNamespace()))

			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
					},
				},
			)
		}
	}

	return requests
}

func (r *DNSMasqReconciler) reconcileDelete(ctx context.Context, instance *networkv1.DNSMasq, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	// Remove finalizer on the Topology CR
	if ctrlResult, err := topologyv1.EnsureDeletedTopologyRef(
		ctx,
		helper,
		instance.Status.LastAppliedTopology,
		instance.Name,
	); err != nil {
		return ctrlResult, err
	}

	return ctrl.Result{}, nil
}

func (r *DNSMasqReconciler) reconcileNormal(ctx context.Context, instance *networkv1.DNSMasq, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// Ensure existing CRs get the default local=/<namespace>.svc/ option on
	// upgrade by triggering the webhook via a reconcile-trigger annotation.
	hasLocal := false
	for _, opt := range instance.Spec.Options {
		if opt.Key == "local" {
			hasLocal = true
			break
		}
	}
	if !hasLocal {
		result, err := webhook.EnsureWebhookTrigger(
			ctx,
			instance,
			annotations.ReconcileTriggerAnnotation,
			"DNSMasq local option defaulting",
			Log,
			0, // use default 5 minute timeout
		)
		if err != nil {
			return ctrl.Result{}, err
		}
		return result, nil
	}

	serviceLabels := map[string]string{
		common.AppSelector: dnsmasq.ServiceName,
	}

	serviceAnnotations := map[string]string{}
	configMapVars := make(map[string]env.Setter)

	labelSelectorMap := map[string]string{networkv1.DNSDataLabelSelectorKey: strings.ToLower(instance.Spec.DNSDataLabelSelectorValue)}
	configMaps := &corev1.ConfigMapList{}
	listOpts := []client.ListOption{
		client.InNamespace(instance.GetNamespace()),
		client.MatchingLabels(labelSelectorMap),
	}
	err := r.List(ctx, configMaps, listOpts...)
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
	// Keep the options ConfigMap independent of the aggregated CNAME data.
	err = r.generateServiceConfigMaps(ctx, helper, instance, &configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	// Source shards have no DNSData selector label until their names are
	// published through the host ConfigMap's pointer key.
	aggregateInputs := &corev1.ConfigMapList{Items: append([]corev1.ConfigMap(nil), configMaps.Items...)}
	sourceShards, err := r.publishedCNAMESourceShards(ctx, instance.Namespace, configMaps.Items)
	if k8s_errors.IsNotFound(err) {
		// DNSData replaced the generation after our list; the next list sees the new pointer.
		Log.Info("Published CNAME source shard disappeared, retrying", "Reason", err.Error())
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	aggregateInputs.Items = append(aggregateInputs.Items, sourceShards...)
	cnameData, cnameWarnings, cnameErr := dnsmasq.AggregateCNAMEConfigMaps(aggregateInputs)
	for _, warning := range cnameWarnings {
		Log.Info("Ignoring CNAME alias that conflicts with a host record", "Reason", warning)
	}

	cnameShards := dnsmasq.SplitCNAMEConfig(cnameData)
	cnameGeneration, err := util.ObjectHash(cnameData)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("hashing aggregate CNAME generation: %w", err)
	}
	cnameShardNames := make([]string, 0, len(cnameShards))
	selectedNames := make(map[string]struct{}, len(configMaps.Items))
	for _, cm := range configMaps.Items {
		selectedNames[cm.Name] = struct{}{}
	}
	for shard, data := range cnameShards {
		name := dnsmasq.CNAMEShardConfigMapName(instance.Name, cnameGeneration, shard)
		cnameShardNames = append(cnameShardNames, name)
		if _, selected := selectedNames[name]; selected {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ServiceConfigReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.ServiceConfigReadyErrorMessage,
				fmt.Sprintf("selected DNSData ConfigMap %q conflicts with the reserved CNAME ConfigMap name", name)))
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		template := util.Template{
			Name:                 name,
			Namespace:            instance.Namespace,
			Type:                 util.TemplateTypeNone,
			InstanceType:         instance.Kind,
			CustomData:           map[string]string{dnsmasq.CNAMEShardKey(shard): data},
			Labels:               labels.GetLabels(instance, labels.GetGroupLabel(dnsmasq.ServiceName), map[string]string{dnsmasq.CNAMEShardLabel: instance.Name}),
			RequireExistingOwner: true,
		}
		err = configmap.EnsureConfigMaps(ctx, helper, instance, []util.Template{template}, &configMapVars)
		if err != nil {
			break
		}
	}
	if errors.Is(err, configmap.ErrExistingConfigMapNotOwned) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	hostConfigMaps := &corev1.ConfigMapList{}
	cmNames := []string{}
	for _, cm := range configMaps.Items {
		if cm.Data[cm.Name] == "" {
			continue
		}
		hostConfigMaps.Items = append(hostConfigMaps.Items, cm)
		// CNAMEs are hashed through the applied aggregate, not the source ConfigMaps.
		hash, err := hostConfigMapHash(&cm)
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
	Log.Info("ConfigMaps providing host information:", "ConfigMaps", cmNames)
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
	if cnameErr != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			cnameErr.Error()))
	} else {
		instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)
	}

	// expose the service
	svcOverride := instance.Spec.Override.Service
	if svcOverride == nil {
		svcOverride = &service.OverrideSpec{}
	}

	// Create the service
	svc, err := service.NewService(
		service.GenericService(&service.GenericServiceDetails{
			Name:      dnsmasq.ServiceName + "-" + instance.Name,
			Namespace: instance.Namespace,
			Labels:    serviceLabels,
			Selector:  serviceLabels,
			Ports: []corev1.ServicePort{
				{
					Name:       dnsmasq.ServiceName,
					Protocol:   corev1.ProtocolUDP,
					Port:       dnsmasq.DNSPort,
					TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: dnsmasq.DNSTargetPort},
				},
				{
					Name:       dnsmasq.ServiceName + "-tcp",
					Protocol:   corev1.ProtocolTCP,
					Port:       dnsmasq.DNSPort,
					TargetPort: intstr.IntOrString{Type: intstr.Int, IntVal: dnsmasq.DNSTargetPort},
				},
			},
		}),
		5,
		svcOverride,
	)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.CreateServiceReadyErrorMessage,
			err.Error()))

		return ctrl.Result{}, err
	}

	svc.AddAnnotation(map[string]string{
		service.AnnotationIngressCreateKey: "false",
	})

	ctrlResult, err := svc.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.CreateServiceReadyErrorMessage,
			err.Error()))

		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.CreateServiceReadyRunningMessage))
		return ctrlResult, nil
	}

	// Update status with LoadBalancerIPs
	instance.Status.DNSAddresses = svc.GetExternalIPs()

	// Update status with Cluster Addresses
	instance.Status.DNSClusterAddresses = svc.GetClusterIPs()

	// create service - end

	instance.Status.Conditions.MarkTrue(condition.CreateServiceReadyCondition, condition.CreateServiceReadyMessage)

	//
	// Handle Topology
	//
	topology, err := topologyv1.EnsureServiceTopology(
		ctx,
		helper,
		instance.Spec.TopologyRef,
		instance.Status.LastAppliedTopology,
		instance.Name,
		labels.GetLabelSelector(serviceLabels),
	)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.TopologyReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.TopologyReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("waiting for Topology requirements: %w", err)
	}

	// If TopologyRef is present and ensureServiceTopology returned a valid
	// topology object, set .Status.LastAppliedTopology to the referenced one
	// and mark the condition as true
	if instance.Spec.TopologyRef != nil {
		// update the Status with the last retrieved Topology name
		instance.Status.LastAppliedTopology = instance.Spec.TopologyRef
		// update the TopologyRef associated condition
		instance.Status.Conditions.MarkTrue(condition.TopologyReadyCondition, condition.TopologyReadyMessage)
	} else {
		// remove LastAppliedTopology from the .Status
		instance.Status.LastAppliedTopology = nil
	}

	// Define a new Deployment object
	deplDef := dnsmasq.Deployment(instance, instance.Status.Hash[common.InputHashName], serviceLabels, serviceAnnotations, hostConfigMaps, cnameShardNames, topology)
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

	deploy := depl.GetDeployment()
	if deploy.Generation == deploy.Status.ObservedGeneration {
		instance.Status.ReadyCount = deploy.Status.ReadyReplicas
	}

	// Mark the Deployment as Ready only if the number of Replicas is equals
	// to the Deployed instances (ReadyCount), and the Status.Replicas
	// match Status.ReadyReplicas. If a deployment update is in progress,
	// Replicas > ReadyReplicas.
	// In addition, make sure the controller sees the last Generation
	// by comparing it with the ObservedGeneration.
	if deployment.IsReady(deploy) {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
		if err := r.cleanupStaleCNAMEShards(ctx, instance, cnameShardNames); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ServiceConfigReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.ServiceConfigReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		// It is OK to return success as we are watching for StatefulSet changes
		return ctrl.Result{}, nil
	}
	// create Deployment - end
	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// hostConfigMapHash hashes a DNSData ConfigMap without its CNAME inputs; those
// are hashed through the applied aggregate shards instead.
func hostConfigMapHash(cm *corev1.ConfigMap) (string, error) {
	hostCM := cm.DeepCopy()
	delete(hostCM.Data, dnsmasq.CNAMEConfigMapKey)
	delete(hostCM.Data, dnsmasq.CNAMEShardPointerKey)
	return configmap.Hash(hostCM)
}

// publishedCNAMESourceShards returns the DNSData CNAME source shards named by
// the pointer key of each host ConfigMap, verifying ownership and content.
// A missing shard is returned as a NotFound error.
func (r *DNSMasqReconciler) publishedCNAMESourceShards(ctx context.Context, namespace string, hostCMs []corev1.ConfigMap) ([]corev1.ConfigMap, error) {
	var shards []corev1.ConfigMap
	for _, cm := range hostCMs {
		pointer := cm.Data[dnsmasq.CNAMEShardPointerKey]
		if pointer == "" {
			continue
		}
		owner := metav1.GetControllerOf(&cm)
		for _, name := range strings.Split(pointer, ",") {
			if name == "" {
				return nil, fmt.Errorf("DNSData ConfigMap %q has an empty CNAME source shard name", cm.Name)
			}
			shard := &corev1.ConfigMap{}
			if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, shard); err != nil {
				return nil, fmt.Errorf("published CNAME source shard %q is unavailable: %w", name, err)
			}
			shardOwner := metav1.GetControllerOf(shard)
			if owner == nil || shardOwner == nil || shardOwner.UID != owner.UID || shard.Labels[dnsmasq.DNSDataCNAMEShardLabel] != string(owner.UID) {
				return nil, fmt.Errorf("CNAME source shard %q does not belong to DNSData ConfigMap %q", name, cm.Name)
			}
			if shard.Data[dnsmasq.CNAMEConfigMapKey] == "" {
				return nil, fmt.Errorf("CNAME source shard %q has no CNAME data", name)
			}
			shards = append(shards, *shard)
		}
	}
	return shards, nil
}

// cleanupStaleCNAMEShards runs after the new Deployment is ready, so old pods
// no longer need surplus shards.
// ponytail: while a rollout is stuck every CNAME change leaves another shard
// generation behind (bounded by etcd, freed once the Deployment is Ready).
func (r *DNSMasqReconciler) cleanupStaleCNAMEShards(ctx context.Context, instance *networkv1.DNSMasq, shardNames []string) error {
	shards := &corev1.ConfigMapList{}
	if err := r.List(ctx, shards, client.InNamespace(instance.Namespace), client.MatchingLabels{dnsmasq.CNAMEShardLabel: instance.Name}); err != nil {
		return fmt.Errorf("listing CNAME shards: %w", err)
	}
	desired := make(map[string]struct{}, len(shardNames))
	for _, name := range shardNames {
		desired[name] = struct{}{}
	}
	for i := range shards.Items {
		cm := &shards.Items[i]
		if _, keep := desired[cm.Name]; keep || !metav1.IsControlledBy(cm, instance) {
			continue
		}
		if err := r.Delete(ctx, cm, client.Preconditions{ResourceVersion: &cm.ResourceVersion}); err != nil && !k8s_errors.IsNotFound(err) {
			return fmt.Errorf("deleting stale CNAME shard %q: %w", cm.Name, err)
		}
	}
	return nil
}

// generateServiceConfigMaps - create configmaps which hold service configuration
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
		cfg += option.Key
		if len(option.Values) > 0 {
			values := strings.Join(option.Values, ",")
			cfg += "=" + values
		}
		cfg += "\n"
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
