/*
Copyright 2024.

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

// Package network implements network controllers for managing BGP configuration and related network resources
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
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

	"github.com/go-logr/logr"
	k8s_networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bgp "github.com/openstack-k8s-operators/infra-operator/pkg/bgp"
)

// BGPConfigurationReconciler reconciles a BGPConfiguration object
type BGPConfigurationReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *BGPConfigurationReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("BGPConfiguration")
}

//+kubebuilder:rbac:groups=network.openstack.org,resources=bgpconfigurations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.openstack.org,resources=bgpconfigurations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.openstack.org,resources=bgpconfigurations/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups=frrk8s.metallb.io,resources=frrconfigurations,verbs=get;list;watch;create;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the BGPConfiguration object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *BGPConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the BGPConfiguration instance
	instance := &networkv1.BGPConfiguration{}
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
		condition.RestoreLastTransitionTimes(
			&instance.Status.Conditions, savedConditions)
		if instance.Status.Conditions.IsUnknown(condition.ReadyCondition) {
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	// initialize status
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, nil
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BGPConfigurationReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	Log := r.GetLogger(ctx)
	podFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		result := []reconcile.Request{}

		// For each Pod event get the list of all
		// BGPConfiguration to trigger reconcile for the one in the same namespace
		bgpConfigurationList := &networkv1.BGPConfigurationList{}

		listOpts := []client.ListOption{
			client.InNamespace(o.GetNamespace()),
		}
		if err := r.List(ctx, bgpConfigurationList, listOpts...); err != nil {
			Log.Error(err, "Unable to retrieve BGPConfigurationList in namespace %s", o.GetNamespace())
			return nil
		}

		// For each BGPConfiguration instance create a reconcile request
		for _, i := range bgpConfigurationList.Items {
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

	frrFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		result := []reconcile.Request{}

		// For each FRRConfiguration event get the namespace of its openstack deployment
		// from the `bgpconfiguration.openstack.org/namespace: openstack` label,
		// get a list of all BGPConfiguration and trigger the reconcile.
		ownerNamespaceLabelSelector := labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
		bgpConfigurationNamespace, ok := o.GetLabels()[ownerNamespaceLabelSelector]
		if !ok {
			Log.Info(fmt.Sprintf("label %s not found for %s: %+v",
				ownerNamespaceLabelSelector, o.GetName(), o.GetLabels()))
			return nil
		}

		bgpConfigurationList := &networkv1.BGPConfigurationList{}
		listOpts := []client.ListOption{
			client.InNamespace(bgpConfigurationNamespace),
		}
		if err := r.List(ctx, bgpConfigurationList, listOpts...); err != nil {
			Log.Error(err, "Unable to retrieve BGPConfigurationList in namespace %s", bgpConfigurationNamespace)
			return nil
		}

		// For each BGPConfiguration instance create a reconcile request
		for _, i := range bgpConfigurationList.Items {
			name := client.ObjectKey{
				Namespace: bgpConfigurationNamespace,
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

	// 'UpdateFunc', 'DeleteFunc' and 'CreateFunc' used to judge if a event about the object is
	// what we want. If that is true, the event will be processed by the reconciler.
	pPod := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Skip if
			// * no NAD annotation was configured on old object OR
			// * no NAD annotation was configured on new object AND
			// * the resourceVersion has not changed

			oldConfigured := true
			if val, ok := e.ObjectOld.GetAnnotations()[k8s_networkv1.NetworkAttachmentAnnot]; !ok || len(val) == 0 {
				oldConfigured = false
			}
			newConfigured := true
			if val, ok := e.ObjectNew.GetAnnotations()[k8s_networkv1.NetworkAttachmentAnnot]; !ok || len(val) == 0 {
				newConfigured = false
			}

			return (oldConfigured || newConfigured) && e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Skip if
			// * NAD annotation key is missing
			// * there is no additional network configured
			if val, ok := e.Object.GetAnnotations()[k8s_networkv1.NetworkAttachmentAnnot]; !ok || len(val) == 0 {
				return false
			}

			return true
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// Skip if
			// * NAD annotation key is missing
			// * there is no additional network configured
			if val, ok := e.Object.GetAnnotations()[k8s_networkv1.NetworkAttachmentAnnot]; !ok || len(val) == 0 {
				return false
			}

			return true
		},
	}

	// 'UpdateFunc', 'DeleteFunc' and 'CreateFunc' used to judge if a event about the object is
	// what we want. If that is true, the event will be processed by the reconciler.
	pFRR := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Skip if the FRRConfiguration
			// * doesn't contain label `bgpconfiguration.openstack.org/namespace`
			ownerNamespaceLabelSelector := labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.ObjectOld.GetLabels()[ownerNamespaceLabelSelector]; !ok {
				return false
			}

			// Skip if this FRRConfiguration is owned by this controller (prevent reconcile loop)
			ownerNameLabelSelector := labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.ObjectOld.GetLabels()[ownerNameLabelSelector]; ok {
				return false
			}

			return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Skip if the FRRConfiguration
			// * doesn't contain label `bgpconfiguration.openstack.org/namespace`
			ownerNamespaceLabelSelector := labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.Object.GetLabels()[ownerNamespaceLabelSelector]; !ok {
				return false
			}

			// Skip if this FRRConfiguration is owned by this controller (prevent reconcile loop)
			ownerNameLabelSelector := labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.Object.GetLabels()[ownerNameLabelSelector]; ok {
				return false
			}

			return true
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// Skip if the FRRConfiguration
			// * doesn't contain label `bgpconfiguration.openstack.org/namespace`
			ownerNamespaceLabelSelector := labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.Object.GetLabels()[ownerNamespaceLabelSelector]; !ok {
				return false
			}

			// Skip if this FRRConfiguration is owned by this controller (prevent reconcile loop)
			ownerNameLabelSelector := labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.Object.GetLabels()[ownerNameLabelSelector]; ok {
				return false
			}

			return true
		},
	}

	configMapFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		result := []reconcile.Request{}

		// Only trigger reconcile for service ConfigMaps
		validConfigMaps := []string{"octavia-hmport-map", "designate-bind-ip-map", "designate-mdns-ip-map"}
		configMapName := o.GetName()
		isValidConfigMap := false
		for _, validName := range validConfigMaps {
			if configMapName == validName {
				isValidConfigMap = true
				break
			}
		}
		if !isValidConfigMap {
			return nil
		}

		// For service ConfigMap events get the list of all BGPConfiguration in the same namespace
		bgpConfigurationList := &networkv1.BGPConfigurationList{}

		listOpts := []client.ListOption{
			client.InNamespace(o.GetNamespace()),
		}
		if err := r.List(ctx, bgpConfigurationList, listOpts...); err != nil {
			Log.Error(err, "Unable to retrieve BGPConfigurationList in namespace %s", o.GetNamespace())
			return nil
		}

		// For each BGPConfiguration instance create a reconcile request
		for _, i := range bgpConfigurationList.Items {
			name := client.ObjectKey{
				Namespace: o.GetNamespace(),
				Name:      i.Name,
			}
			result = append(result, reconcile.Request{NamespacedName: name})
		}
		if len(result) > 0 {
			Log.Info("Reconcile request for service ConfigMap change:", "configMap", configMapName, "result", result)
			return result
		}
		return nil
	})

	// Predicate to filter service ConfigMap events
	pConfigMap := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			validConfigMaps := []string{"octavia-hmport-map", "designate-bind-ip-map", "designate-mdns-ip-map"}
			configMapName := e.ObjectNew.GetName()
			for _, validName := range validConfigMaps {
				if configMapName == validName {
					return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
				}
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			validConfigMaps := []string{"octavia-hmport-map", "designate-bind-ip-map", "designate-mdns-ip-map"}
			configMapName := e.Object.GetName()
			for _, validName := range validConfigMaps {
				if configMapName == validName {
					return true
				}
			}
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			validConfigMaps := []string{"octavia-hmport-map", "designate-bind-ip-map", "designate-mdns-ip-map"}
			configMapName := e.Object.GetName()
			for _, validName := range validConfigMaps {
				if configMapName == validName {
					return true
				}
			}
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.BGPConfiguration{}).
		// Watch pods which have additional networks configured with k8s_networkv1.NetworkAttachmentAnnot annotation in the same namespace
		Watches(&corev1.Pod{},
			podFN,
			builder.WithPredicates(pPod)).
		// Watch FRRConfiguration which have the `bgpconfiguration.openstack.org/namespace` label
		Watches(&frrk8sv1.FRRConfiguration{},
			frrFN,
			builder.WithPredicates(pFRR)).
		// Watch service ConfigMaps to handle IP changes (octavia, designate)
		Watches(&corev1.ConfigMap{},
			configMapFN,
			builder.WithPredicates(pConfigMap)).
		Complete(r)
}

func (r *BGPConfigurationReconciler) reconcileDelete(ctx context.Context, instance *networkv1.BGPConfiguration, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Delete all FRRConfiguration in the Spec.FRRConfigurationNamespace namespace,
	// which have the correct owner and ownernamespace label
	err := r.DeleteAllOf(
		ctx,
		&frrk8sv1.FRRConfiguration{},
		client.InNamespace(instance.Spec.FRRConfigurationNamespace),
		client.MatchingLabels{
			labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration")):      instance.Name,
			labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration")): instance.Namespace,
		},
	)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("error DeleteAllOf FRRConfiguration: %w", err)
	}

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

func (r *BGPConfigurationReconciler) reconcileNormal(ctx context.Context, instance *networkv1.BGPConfiguration, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// Get a list of pods which are in the same namespace as the ctlplane
	// to verify if a FRRConfiguration needs to be created for.
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(instance.Namespace),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to retrieve PodList %w", err)
	}

	// get podDetail all pods which have additional interfaces configured
	podNetworkDetailList, err := getPodNetworkDetails(ctx, helper, podList)
	if err != nil {
		return ctrl.Result{}, err
	}

	// get all frrconfigs
	frrConfigList := &frrk8sv1.FRRConfigurationList{}
	listOpts = []client.ListOption{
		client.InNamespace(instance.Spec.FRRConfigurationNamespace), // defaults to metallb-system
	}
	if err := r.List(ctx, frrConfigList, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to retrieve FRRConfigurationList %w", err)
	}

	// get all frr configs for the nodes pods are scheduled on
	groupLabel := labels.GetGroupLabel("bgpconfiguration")
	frrNodeConfigs := map[string]frrk8sv1.FRRConfiguration{}
	for _, nodeName := range bgp.GetNodesRunningPods(podNetworkDetailList) {
		var nodeSelector metav1.LabelSelector

		// validate if a nodeSelector is configured for the nodeName
		// this is just for the case where metallb would change
		// the nodeSelector it puts on the FRRConfigurations it creates
		f := func(c networkv1.FRRNodeConfigurationSelectorType) bool {
			return c.NodeName == nodeName
		}
		idx := slices.IndexFunc(instance.Spec.FRRNodeConfigurationSelector, f)
		if idx >= 0 {
			nodeSelector = instance.Spec.FRRNodeConfigurationSelector[idx].NodeSelector
		} else {
			// metallb puts per default just the hostname selector in the frrconfiguration.
			// nodeSelector:
			//   matchLabels:
			//     kubernetes.io/hostname: worker-0
			nodeSelector.MatchLabels = map[string]string{
				corev1.LabelHostname: nodeName,
			}
			Log.Info(fmt.Sprintf("using default nodeSelector %+v", nodeSelector.MatchLabels))
		}

		var frrCfg *frrk8sv1.FRRConfiguration
		for _, cfg := range frrConfigList.Items {
			frrLabels := cfg.GetLabels()
			// skip checking our own managed FRRConfigurations,
			if _, ok := frrLabels[labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))]; ok {
				continue
			}
			if equality.Semantic.DeepEqual(cfg.Spec.NodeSelector, nodeSelector) {
				frrCfg = cfg.DeepCopy()
			}
		}

		if frrCfg != nil {
			frrNodeConfigs[nodeName] = *frrCfg
		} else {
			// error, we have not found the frrConfig for the node
			return ctrl.Result{}, fmt.Errorf("no FRRConfiguration found for node %s using nodeSelector %v", nodeName, nodeSelector)
		}
	}

	// create FRRConfigurations for the podNetworkDetailList
	for _, podNetworkDetail := range podNetworkDetailList {
		err = r.createOrPatchFRRConfiguration(
			ctx,
			instance,
			&podNetworkDetail,
			frrNodeConfigs,
			labels.GetLabels(instance,
				groupLabel,
				map[string]string{
					groupLabel + "/pod-name": podNetworkDetail.Name,
				}),
		)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Process service ConfigMaps for service IPs (octavia, designate)
	serviceMappings, err := getAllServiceIPMappings(ctx, r.Client, instance.Namespace)
	if err != nil {
		Log.Info("Warning: Failed to process service ConfigMaps", "error", err.Error())
		// Continue processing even if ConfigMaps are not available
	} else {
		// Create FRRConfigurations for each service IP
		for _, serviceMapping := range serviceMappings {
			// For octavia, only process if worker has pods with NAD annotations
			if serviceMapping.Type == "octavia" {
				if _, exists := frrNodeConfigs[serviceMapping.Name]; !exists {
					Log.Info("Skipping octavia worker - no pods with NAD annotations found", "worker", serviceMapping.Name)
					continue
				}
			}

			labelKey := groupLabel + "/service-type"
			labelValue := fmt.Sprintf("%s-%s", serviceMapping.Type, serviceMapping.Name)
			if serviceMapping.Type == "octavia" {
				labelKey = groupLabel + "/octavia-worker" // maintain backward compatibility
				labelValue = serviceMapping.Name
			}

			err = r.createOrPatchFRRConfigurationForService(
				ctx,
				instance,
				serviceMapping,
				frrNodeConfigs,
				frrConfigList,
				labels.GetLabels(instance,
					groupLabel,
					map[string]string{
						labelKey: labelValue,
					}),
			)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// delete our managed FRRConfigurations where there is no longer a pod in podNetworkDetailList
	// because the pod was deleted/completed/failed/unknown
	if err := r.deleteStaleFRRConfigurations(ctx, instance, podNetworkDetailList, frrConfigList, groupLabel); err != nil {
		return ctrl.Result{}, err
	}

	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

func (r *BGPConfigurationReconciler) deleteStaleFRRConfigurations(ctx context.Context, instance *networkv1.BGPConfiguration, podNetworkDetailList []bgp.PodDetail, frrConfigList *frrk8sv1.FRRConfigurationList, groupLabel string) error {
	// Get current service mappings to check which service FRRConfigurations should exist
	currentServiceMappings, err := getAllServiceIPMappings(ctx, r.Client, instance.Namespace)
	if err != nil {
		r.GetLogger(ctx).Info("Warning: Failed to get service mappings during stale deletion check", "error", err.Error())
		currentServiceMappings = []ServiceIPMapping{} // Continue with empty list
	}

	for _, cfg := range frrConfigList.Items {
		frrLabels := cfg.GetLabels()
		if _, ok := frrLabels[labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))]; ok {
			// Check pod-based FRRConfigurations
			if podName, ok := frrLabels[groupLabel+"/pod-name"]; ok {
				f := func(p bgp.PodDetail) bool {
					return p.Name == podName && p.Namespace == instance.Namespace
				}
				idx := slices.IndexFunc(podNetworkDetailList, f)
				if idx < 0 {
					// There is no pod in the namespace corrsponding to the FRRConfiguration, delete it
					if err := r.Delete(ctx, &cfg); err != nil && !k8s_errors.IsNotFound(err) {
						return fmt.Errorf("unable to delete FRRConfiguration %w", err)
					}
					r.GetLogger(ctx).Info(fmt.Sprintf("pod with name: %s either in state deleted, completed, failed or unknown, deleted FRRConfiguration %s", podName, cfg.Name))
				}
			}

			// Check octavia worker-based FRRConfigurations (backward compatibility)
			if workerName, ok := frrLabels[groupLabel+"/octavia-worker"]; ok {
				f := func(s ServiceIPMapping) bool {
					return s.Type == "octavia" && s.Name == workerName
				}
				idx := slices.IndexFunc(currentServiceMappings, f)
				if idx < 0 {
					// There is no service mapping corresponding to the FRRConfiguration, delete it
					if err := r.Delete(ctx, &cfg); err != nil && !k8s_errors.IsNotFound(err) {
						return fmt.Errorf("unable to delete octavia FRRConfiguration %w", err)
					}
					r.GetLogger(ctx).Info(fmt.Sprintf("octavia worker %s no longer exists in ConfigMap, deleted FRRConfiguration %s", workerName, cfg.Name))
				}
			}

			// Check service-based FRRConfigurations
			if serviceType, ok := frrLabels[groupLabel+"/service-type"]; ok {
				f := func(s ServiceIPMapping) bool {
					return fmt.Sprintf("%s-%s", s.Type, s.Name) == serviceType
				}
				idx := slices.IndexFunc(currentServiceMappings, f)
				if idx < 0 {
					// There is no service mapping corresponding to the FRRConfiguration, delete it
					if err := r.Delete(ctx, &cfg); err != nil && !k8s_errors.IsNotFound(err) {
						return fmt.Errorf("unable to delete service FRRConfiguration %w", err)
					}
					r.GetLogger(ctx).Info(fmt.Sprintf("service %s no longer exists in ConfigMap, deleted FRRConfiguration %s", serviceType, cfg.Name))
				}
			}
		}
	}
	return nil
}

// getPodNetworkDetails - returns the podDetails for a list of pods in status.phase: Running
// where the pod has the multus k8s_networkv1.NetworkAttachmentAnnot annotation
// and its value is not '[]'
func getPodNetworkDetails(
	ctx context.Context,
	h *helper.Helper,
	pods *corev1.PodList,
) ([]bgp.PodDetail, error) {
	Log := h.GetLogger()
	Log.Info("Reconciling getPodNetworkDetails")
	detailList := []bgp.PodDetail{}
	if pods != nil {
		for _, pod := range pods.Items {
			// skip pods which are in deletion to make sure its FRRConfiguration gets deleted
			if !pod.DeletionTimestamp.IsZero() {
				Log.Info(fmt.Sprintf("Skipping pod as its in deletion with DeletionTimestamp set: %s", pod.Name))
				continue
			}
			// skip pods which are not in Running phase (deleted/completed/failed/unknown)
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			if netAttachString, ok := pod.Annotations[k8s_networkv1.NetworkAttachmentAnnot]; ok && netAttachString != "[]" {
				// get the elements from val to validate the status annotation has the right length
				netAttach := []k8s_networkv1.NetworkSelectionElement{}
				err := json.Unmarshal([]byte(netAttachString), &netAttach)
				if err != nil {
					return nil, fmt.Errorf("failed to decode networks %s: %w", netAttachString, err)
				}

				// verify the nodeName information is already present in the pod spec, otherwise report an error to reconcile
				if pod.Spec.NodeName == "" {
					return detailList, fmt.Errorf("empty spec.nodeName on pod %s", pod.Name)
				}

				detail := bgp.PodDetail{
					Name:      pod.Name,
					Namespace: pod.Namespace,
					Node:      pod.Spec.NodeName,
				}

				netsStatus, err := nad.GetNetworkStatusFromAnnotation(pod.Annotations)
				if err != nil {
					return detailList, fmt.Errorf("failed to get netsStatus from pod annoation - %v: %w", pod.Annotations, err)
				}
				// on pod start it can happen that the network status annotation does not yet
				// reflect all requested networks. return with an error to reconcile if the length
				// is <= the status. Note: the status also has the pod network
				if len(netsStatus) <= len(netAttach) {
					return detailList, fmt.Errorf("metadata.Annotations['k8s.ovn.org/pod-networks'] %s on pod %s, does not match requested networks %s",
						pod.GetAnnotations()[k8s_networkv1.NetworkStatusAnnot], pod.Name, netAttachString)
				}

				netsStatusCopy := make([]k8s_networkv1.NetworkStatus, len(netsStatus))
				copy(netsStatusCopy, netsStatus)
				// verify there are IP information for all networks in the status, otherwise report an error to reconcile
				for idx, netStat := range netsStatusCopy {
					// remove status for the pod interface
					// it should always be ovn-kubernetes, but if not, remove status for eth0 which is the pod network
					if netStat.Name == "ovn-kubernetes" || netStat.Interface == "eth0" {
						removeIndex(netsStatus, idx)
						continue
					}

					// get ipam configuration from NAD
					nadName := strings.TrimPrefix(netStat.Name, pod.Namespace+"/")
					netAtt, err := nad.GetNADWithName(
						ctx, h, nadName, pod.Namespace)
					if err != nil {
						return detailList, err
					}

					ipam, err := nad.GetJSONPathFromConfig(*netAtt, ".ipam")
					if err != nil {
						return detailList, err
					}

					// if the NAD has no ipam configured, skip it as there will be no IP
					if ipam == "{}" {
						Log.Info(fmt.Sprintf("removing netsStatus for NAD %s for %s, IPAM configuration is empty: %s", netAtt.Name, pod.Name, ipam))
						removeIndex(netsStatus, idx)
						continue
					}

					// verify there is IP information for the network, otherwise report an error to reconcile
					if len(netStat.IPs) == 0 {
						return detailList, fmt.Errorf("no IP information for network %s on pod %s", netStat.Name, pod.Name)
					}
				}

				detail.NetworkStatus = netsStatus

				detailList = append(detailList, detail)
			}
		}
	}

	Log.Info("Reconciled getPodNetworkDetails successfully")
	return detailList, nil
}

func removeIndex(s []k8s_networkv1.NetworkStatus, index int) []k8s_networkv1.NetworkStatus {
	return append(s[:index], s[index+1:]...)
}

// WorkerIPMapping - represents IP mapping for a worker node
type WorkerIPMapping struct {
	WorkerName string
	HMIP       string
	RsyslogIP  string
}

// ServiceIPMapping - represents IP mapping for a service
type ServiceIPMapping struct {
	Name string
	IP   string
	Type string // "octavia", "designate-bind", "designate-mdns"
}

// getAllServiceIPMappings - gets all service IP mappings from various ConfigMaps
func getAllServiceIPMappings(
	ctx context.Context,
	client client.Client,
	namespace string,
) ([]ServiceIPMapping, error) {
	var allMappings []ServiceIPMapping

	// Get Octavia mappings
	octaviaMappings, err := getOctaviaWorkerIPMappings(ctx, client, namespace)
	if err != nil {
		return nil, err
	}
	for _, mapping := range octaviaMappings {
		allMappings = append(allMappings,
			ServiceIPMapping{Name: mapping.WorkerName, IP: mapping.HMIP, Type: "octavia"},
			ServiceIPMapping{Name: mapping.WorkerName, IP: mapping.RsyslogIP, Type: "octavia"},
		)
	}

	// Get Designate mappings
	bindMappings, err := getDesignateIPMappings(ctx, client, namespace, "designate-bind-ip-map", "bind_address_", "designate-bind")
	if err != nil {
		return nil, err
	}
	allMappings = append(allMappings, bindMappings...)

	mdnsMappings, err := getDesignateIPMappings(ctx, client, namespace, "designate-mdns-ip-map", "mdns_address_", "designate-mdns")
	if err != nil {
		return nil, err
	}
	allMappings = append(allMappings, mdnsMappings...)

	return allMappings, nil
}

// getDesignateIPMappings - parses designate ConfigMaps and returns IP mappings
func getDesignateIPMappings(
	ctx context.Context,
	client client.Client,
	namespace, configMapName, keyPrefix, serviceType string,
) ([]ServiceIPMapping, error) {
	configMap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, configMap)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return []ServiceIPMapping{}, nil
		}
		return nil, fmt.Errorf("failed to get %s ConfigMap: %w", configMapName, err)
	}

	var mappings []ServiceIPMapping
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(keyPrefix) + `(\d+)$`)

	for key, ip := range configMap.Data {
		if matches := pattern.FindStringSubmatch(key); len(matches) > 1 {
			mappings = append(mappings, ServiceIPMapping{
				Name: matches[1],
				IP:   ip,
				Type: serviceType,
			})
		}
	}

	return mappings, nil
}

// getOctaviaWorkerIPMappings - parses octavia-hmport-map ConfigMap and returns worker IP mappings
func getOctaviaWorkerIPMappings(
	ctx context.Context,
	client client.Client,
	namespace string,
) ([]WorkerIPMapping, error) {
	configMap := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{
		Name:      "octavia-hmport-map",
		Namespace: namespace,
	}, configMap)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// ConfigMap not found, return empty list
			return []WorkerIPMapping{}, nil
		}
		return nil, fmt.Errorf("failed to get octavia-hmport-map ConfigMap: %w", err)
	}

	// Parse worker mappings from ConfigMap data
	workerMappings := map[string]*WorkerIPMapping{}

	// Regex patterns to extract worker names
	hmPattern := regexp.MustCompile(`^hm_worker-(.+)$`)
	rsyslogPattern := regexp.MustCompile(`^rsyslog_worker-(.+)$`)

	for key, ip := range configMap.Data {
		if matches := hmPattern.FindStringSubmatch(key); len(matches) > 1 {
			workerName := "worker-" + matches[1]
			if _, exists := workerMappings[workerName]; !exists {
				workerMappings[workerName] = &WorkerIPMapping{WorkerName: workerName}
			}
			workerMappings[workerName].HMIP = ip
		} else if matches := rsyslogPattern.FindStringSubmatch(key); len(matches) > 1 {
			workerName := "worker-" + matches[1]
			if _, exists := workerMappings[workerName]; !exists {
				workerMappings[workerName] = &WorkerIPMapping{WorkerName: workerName}
			}
			workerMappings[workerName].RsyslogIP = ip
		}
	}

	// Convert map to slice and filter complete mappings
	var result []WorkerIPMapping
	for _, mapping := range workerMappings {
		if mapping.HMIP != "" && mapping.RsyslogIP != "" {
			result = append(result, *mapping)
		}
	}

	return result, nil
}

// findDesignatePodNode - finds the node where a designate StatefulSet pod is running
func (r *BGPConfigurationReconciler) findDesignatePodNode(ctx context.Context, namespace, serviceType, index string) (string, error) {
	// Map service type to actual pod name prefix
	var podNamePrefix string
	switch serviceType {
	case "designate-bind":
		podNamePrefix = "designate-backendbind9"
	case "designate-mdns":
		podNamePrefix = "designate-mdns"
	default:
		return "", fmt.Errorf("unknown designate service type: %s", serviceType)
	}

	// Construct pod name based on actual naming pattern (e.g., "designate-backendbind9-0")
	podName := fmt.Sprintf("%s-%s", podNamePrefix, index)

	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      podName,
		Namespace: namespace,
	}, pod)
	if err != nil {
		return "", fmt.Errorf("failed to find pod %s: %w", podName, err)
	}

	if pod.Spec.NodeName == "" {
		return "", fmt.Errorf("pod %s has no node assignment yet", podName)
	}

	return pod.Spec.NodeName, nil
}

// createOrPatchFRRConfigurationForService - creates FRRConfiguration for a service IP
func (r *BGPConfigurationReconciler) createOrPatchFRRConfigurationForService(
	ctx context.Context,
	instance *networkv1.BGPConfiguration,
	serviceMapping ServiceIPMapping,
	nodeFRRCfgs map[string]frrk8sv1.FRRConfiguration,
	frrConfigList *frrk8sv1.FRRConfigurationList,
	frrLabels map[string]string,
) error {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling createOrPatchFRRConfigurationForService", "service", serviceMapping.Type, "name", serviceMapping.Name)

	// Create prefix for the service IP
	servicePrefixes := []string{serviceMapping.IP + "/32"}

	// For octavia, use worker name as node; for designate, find the actual pod node
	var nodeName string
	var nodeFRRCfg frrk8sv1.FRRConfiguration

	if serviceMapping.Type == "octavia" {
		nodeName = serviceMapping.Name
		var exists bool
		nodeFRRCfg, exists = nodeFRRCfgs[nodeName]
		if !exists {
			return fmt.Errorf("no FRRConfiguration found for node %s", nodeName)
		}
	} else {
		// For designate services, find the node where the actual StatefulSet pod is running
		var err error
		nodeName, err = r.findDesignatePodNode(ctx, instance.Namespace, serviceMapping.Type, serviceMapping.Name)
		if err != nil {
			return fmt.Errorf("failed to find node for %s pod %s: %w", serviceMapping.Type, serviceMapping.Name, err)
		}

		// Try to get FRRConfiguration from nodes with pods first
		var exists bool
		nodeFRRCfg, exists = nodeFRRCfgs[nodeName]
		if !exists {
			// Pod is on a node without NAD pods, find corresponding FRRConfiguration
			for _, cfg := range frrConfigList.Items {
				cfgLabels := cfg.GetLabels()
				// Skip our own managed FRRConfigurations
				if _, ok := cfgLabels[labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))]; ok {
					continue
				}
				// Check if this FRRConfiguration matches the node
				if cfg.Spec.NodeSelector.MatchLabels != nil {
					if hostname, ok := cfg.Spec.NodeSelector.MatchLabels[corev1.LabelHostname]; ok && hostname == nodeName {
						nodeFRRCfg = cfg
						exists = true
						break
					}
				}
			}
			if !exists {
				return fmt.Errorf("no FRRConfiguration found for node %s", nodeName)
			}
		}
	}

	frrConfigName := fmt.Sprintf("%s-%s-%s", instance.Namespace, serviceMapping.Type, serviceMapping.Name)

	// Check if there's already a pod-based FRRConfiguration with the same name
	// This happens when a pod has both NAD networks and is also listed in service ConfigMaps
	existingFRRConfig := &frrk8sv1.FRRConfiguration{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      frrConfigName,
		Namespace: instance.Spec.FRRConfigurationNamespace,
	}, existingFRRConfig)

	var allPrefixes []string
	var existingLabels map[string]string

	if err == nil {
		// FRRConfiguration already exists (likely from pod-based creation)
		// Merge service prefixes with existing pod prefixes
		existingPrefixes := []string{}
		if len(existingFRRConfig.Spec.BGP.Routers) > 0 {
			existingPrefixes = existingFRRConfig.Spec.BGP.Routers[0].Prefixes
		}

		// Merge prefixes, avoiding duplicates
		prefixSet := make(map[string]bool)
		for _, prefix := range existingPrefixes {
			prefixSet[prefix] = true
			allPrefixes = append(allPrefixes, prefix)
		}
		for _, prefix := range servicePrefixes {
			if !prefixSet[prefix] {
				allPrefixes = append(allPrefixes, prefix)
			}
		}

		// Preserve existing labels and merge with service labels
		existingLabels = existingFRRConfig.Labels
		Log.Info("Merging service IP with existing pod-based FRRConfiguration", "existing_prefixes", existingPrefixes, "service_prefixes", servicePrefixes, "merged_prefixes", allPrefixes)
	} else if k8s_errors.IsNotFound(err) {
		// No existing FRRConfiguration, use only service prefixes
		allPrefixes = servicePrefixes
		existingLabels = make(map[string]string)
	} else {
		// Error reading existing FRRConfiguration
		return fmt.Errorf("error checking existing FRRConfiguration: %w", err)
	}

	frrConfig := &frrk8sv1.FRRConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      frrConfigName,
			Namespace: instance.Spec.FRRConfigurationNamespace,
		},
	}

	frrConfigSpec := &frrk8sv1.FRRConfigurationSpec{}
	var routers []frrk8sv1.Router
	for _, r := range nodeFRRCfg.Spec.BGP.Routers {
		routers = append(routers, frrk8sv1.Router{
			ASN:       r.ASN,
			Neighbors: bgp.GetFRRNeighbors(r.Neighbors, allPrefixes),
			Prefixes:  allPrefixes,
		})
	}
	frrConfigSpec.BGP.Routers = routers
	frrConfigSpec.NodeSelector = nodeFRRCfg.Spec.NodeSelector

	// create or update the FRRConfiguration
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, frrConfig, func() error {
		// Merge existing labels with new service labels
		mergedLabels := util.MergeMaps(existingLabels, frrLabels)
		frrConfig.Labels = mergedLabels
		frrConfigSpec.DeepCopyInto(&frrConfig.Spec)
		return nil
	})
	if err != nil {
		return fmt.Errorf("error create/updating %s FRRConfiguration: %w", serviceMapping.Type, err)
	}

	if op != controllerutil.OperationResultNone {
		Log.Info("operation:", "FRRConfiguration name", frrConfig.Name, "Operation", string(op))
	}

	Log.Info("Reconciled createOrPatchFRRConfigurationForService successfully", "service", serviceMapping.Type)
	return nil
}

// createOrPatchFRRConfiguration -
func (r *BGPConfigurationReconciler) createOrPatchFRRConfiguration(
	ctx context.Context,
	instance *networkv1.BGPConfiguration,
	podDtl *bgp.PodDetail,
	nodeFRRCfgs map[string]frrk8sv1.FRRConfiguration,
	frrLabels map[string]string,
) error {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling createOrUpdateFRRConfiguration")

	podPrefixes := bgp.GetFRRPodPrefixes(podDtl.NetworkStatus)

	nodeFRRCfg := nodeFRRCfgs[podDtl.Node]

	frrConfig := &frrk8sv1.FRRConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Namespace + "-" + podDtl.Name,
			Namespace: instance.Spec.FRRConfigurationNamespace,
		},
	}

	frrConfigSpec := &frrk8sv1.FRRConfigurationSpec{}
	var routers []frrk8sv1.Router
	for _, r := range nodeFRRCfg.Spec.BGP.Routers {
		routers = append(routers, frrk8sv1.Router{
			ASN:       r.ASN,
			Neighbors: bgp.GetFRRNeighbors(r.Neighbors, podPrefixes),
			Prefixes:  podPrefixes,
		})
	}
	frrConfigSpec.BGP.Routers = routers
	frrConfigSpec.NodeSelector = nodeFRRCfg.Spec.NodeSelector

	// create or update the FRRConfiguration
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, frrConfig, func() error {
		frrConfig.Labels = util.MergeMaps(frrConfig.Labels, frrLabels)
		frrConfigSpec.DeepCopyInto(&frrConfig.Spec)

		return nil
	})
	if err != nil {
		return fmt.Errorf("error create/updating service FRRConfiguration: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		Log.Info("operation:", "FRRConfiguration name", frrConfig.Name, "Operation", string(op))
	}

	Log.Info("Reconciled createOrUpdateFRRConfiguration successfully")
	return nil
}
