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

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/exp/slices"
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
		if err := r.Client.List(ctx, bgpConfigurationList, listOpts...); err != nil {
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
		if err := r.Client.List(ctx, bgpConfigurationList, listOpts...); err != nil {
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
			// * NAD annotation annotation key is missing
			// * there is no additional network configured
			if val, ok := e.Object.GetAnnotations()[k8s_networkv1.NetworkAttachmentAnnot]; !ok || len(val) == 0 {
				return false
			}

			return true
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// Skip if
			// * NAD annotation annotation key is missing
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

			return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Skip if the FRRConfiguration
			// * doesn't contain label `bgpconfiguration.openstack.org/namespace`
			ownerNamespaceLabelSelector := labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration"))
			if _, ok := e.Object.GetLabels()[ownerNamespaceLabelSelector]; !ok {
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

			return true
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
		Complete(r)
}

func (r *BGPConfigurationReconciler) reconcileDelete(ctx context.Context, instance *networkv1.BGPConfiguration, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Delete all FRRConfiguration in the Spec.FRRConfigurationNamespace namespace,
	// which have the correct owner and ownernamespace label
	err := r.Client.DeleteAllOf(
		ctx,
		&frrk8sv1.FRRConfiguration{},
		client.InNamespace(instance.Spec.FRRConfigurationNamespace),
		client.MatchingLabels{
			labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration")):      instance.Name,
			labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel("bgpconfiguration")): instance.Namespace,
		},
	)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("Error DeleteAllOf FRRConfiguration: %w", err)
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
	if err := r.Client.List(ctx, podList, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("Unable to retrieve PodList %w", err)
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
	if err := r.Client.List(ctx, frrConfigList, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("Unable to retrieve FRRConfigurationList %w", err)
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

	// delete our managed FRRConfigurations where there is no longer a pod in podNetworkDetailList
	// because the pod was deleted/completed/failed/unknown
	if err := r.deleteStaleFRRConfigurations(ctx, instance, podNetworkDetailList, frrConfigList, groupLabel); err != nil {
		return ctrl.Result{}, err
	}

	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

func (r *BGPConfigurationReconciler) deleteStaleFRRConfigurations(ctx context.Context, instance *networkv1.BGPConfiguration, podNetworkDetailList []bgp.PodDetail, frrConfigList *frrk8sv1.FRRConfigurationList, groupLabel string) error {
	for _, cfg := range frrConfigList.Items {
		frrLabels := cfg.GetLabels()
		if _, ok := frrLabels[labels.GetOwnerNameLabelSelector(labels.GetGroupLabel("bgpconfiguration"))]; ok {
			if podName, ok := frrLabels[groupLabel+"/pod-name"]; ok {
				f := func(p bgp.PodDetail) bool {
					return p.Name == podName && p.Namespace == instance.Namespace
				}
				idx := slices.IndexFunc(podNetworkDetailList, f)
				if idx < 0 {
					// There is no pod in the namespace corrsponding to the FRRConfiguration, delete it
					if err := r.Client.Delete(ctx, &cfg); err != nil && !k8s_errors.IsNotFound(err) {
						return fmt.Errorf("unable to delete FRRConfiguration %w", err)
					}
					r.GetLogger(ctx).Info(fmt.Sprintf("pod with name: %s either in state deleted, completed, failed or unknown, deleted FRRConfiguration %s", podName, cfg.Name))
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
					return detailList, fmt.Errorf(fmt.Sprintf("empty spec.nodeName on pod %s", pod.Name))
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
					return detailList, fmt.Errorf(fmt.Sprintf("metadata.Annotations['k8s.ovn.org/pod-networks'] %s on pod %s, does not match requested networks %s",
						pod.GetAnnotations()[k8s_networkv1.NetworkStatusAnnot], pod.Name, netAttachString))
				}

				var netsStatusCopy = make([]k8s_networkv1.NetworkStatus, len(netsStatus))
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
						return detailList, fmt.Errorf(fmt.Sprintf("no IP information for network %s on pod %s", netStat.Name, pod.Name))
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
