/*
Copyright 2025.

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

package remediation

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
)

const (
	// NHCRequiredMessage is set in status when Node Health Check is not present
	NHCRequiredMessage = "Node Health Check (NHC) and Self Node Remediation (SNR) are required; controller cannot proceed without them"
	// NHCNotFoundReason is the condition reason when NHC/SNR are missing
	NHCNotFoundReason = "NHC/SNRNotFound"

	// PodRemediatorPendingDeletionAnnotation is set on a PVC when we have decided to delete it (node unhealthy).
	// If the controller restarts before the delete completes, it will see this annotation and complete the deletion (Option B: persist intent).
	// Same idea as Instance HA (IHA), which stores evacuation state in the Nova service disabled_reason so the process can resume after restart (see docs/INSTANCEHA_ARCHITECTURE.md).
	PodRemediatorPendingDeletionAnnotation = "remediation.openstack.org/podremediator-pending-deletion"
)

var (
	gvrNodeHealthCheck = schema.GroupVersionResource{
		Group: "remediation.medik8s.io", Version: "v1alpha1", Resource: "nodehealthchecks",
	}
	gvrSelfNodeRemediationTemplate = schema.GroupVersionResource{
		Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Resource: "selfnoderemediationtemplates",
	}
)

// PodRemediatorReconciler reconciles a PodRemediator object
type PodRemediatorReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Kclient       kubernetes.Interface
	DynamicClient dynamic.Interface
}

// GetLogger returns a logger with controller context
func (r *PodRemediatorReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("PodRemediator")
}

//+kubebuilder:rbac:groups=remediation.openstack.org,resources=podremediators,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=remediation.openstack.org,resources=podremediators/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=remediation.openstack.org,resources=podremediators/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
//+kubebuilder:rbac:groups=remediation.medik8s.io,resources=nodehealthchecks,verbs=get;list;watch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=get;list;watch

// Reconcile reconciles a PodRemediator
func (r *PodRemediatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	Log := r.GetLogger(ctx)

	instance := &remediationv1.PodRemediator{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	helper, err := helper.NewHelper(instance, r.Client, r.Kclient, r.Scheme, Log)
	if err != nil {
		return ctrl.Result{}, err
	}

	isNewInstance := instance.Status.Conditions == nil
	if isNewInstance {
		instance.Status.Conditions = condition.Conditions{}
	}
	savedConditions := instance.Status.Conditions.DeepCopy()
	defer func() {
		if rec := recover(); rec != nil {
			Log.Info("panic during reconcile", "panic", rec)
			panic(rec)
		}
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)
		if instance.Status.Conditions.IsUnknown(condition.ReadyCondition) {
			instance.Status.Conditions.Set(instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		if patchErr := helper.PatchInstance(ctx, instance); patchErr != nil {
			err = patchErr
		}
	}()

	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, "Checking NHC/SNR availability"),
	)
	instance.Status.Conditions.Init(&cl)

	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, nil
	}

	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	return r.reconcileNormal(ctx, instance)
}

// SetupWithManager sets up the controller with the Manager
func (r *PodRemediatorReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	Log := r.GetLogger(ctx)

	podFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.enqueuePodRemediatorsForObject(ctx, o.GetNamespace(), Log)
	})
	nodeFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.enqueuePodRemediatorsForObject(ctx, "", Log) // cluster-wide for nodes
	})
	pvcFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.enqueuePodRemediatorsForObject(ctx, o.GetNamespace(), Log)
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1.PodRemediator{}).
		Watches(&corev1.Pod{}, podFN, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, nodeFN).
		Watches(&corev1.PersistentVolumeClaim{}, pvcFN, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *PodRemediatorReconciler) enqueuePodRemediatorsForObject(ctx context.Context, namespace string, Log logr.Logger) []reconcile.Request {
	list := &remediationv1.PodRemediatorList{}
	opts := []client.ListOption{}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := r.List(ctx, list, opts...); err != nil {
		Log.Error(err, "Unable to list PodRemediator")
		return nil
	}
	result := make([]reconcile.Request, 0, len(list.Items))
	for _, pr := range list.Items {
		result = append(result, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pr)})
	}
	return result
}

func (r *PodRemediatorReconciler) reconcileDelete(ctx context.Context, instance *remediationv1.PodRemediator, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling PodRemediator delete")
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	return ctrl.Result{}, nil
}

// checkNHCAndSNR returns true if at least one NodeHealthCheck and one SelfNodeRemediationTemplate exist
func (r *PodRemediatorReconciler) checkNHCAndSNR(ctx context.Context) (bool, error) {
	nhcList, err := r.DynamicClient.Resource(gvrNodeHealthCheck).List(ctx, metav1.ListOptions{})
	if err != nil {
		if meta.IsNoMatchError(err) || k8s_errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if nhcList == nil || len(nhcList.Items) == 0 {
		return false, nil
	}

	snrList, err := r.DynamicClient.Resource(gvrSelfNodeRemediationTemplate).List(ctx, metav1.ListOptions{})
	if err != nil {
		if meta.IsNoMatchError(err) || k8s_errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if snrList == nil || len(snrList.Items) == 0 {
		return false, nil
	}
	return true, nil
}

func (r *PodRemediatorReconciler) reconcileNormal(ctx context.Context, instance *remediationv1.PodRemediator) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	// 1) Dependency check: NHC and SNR must be present
	nhcSNROk, err := r.checkNHCAndSNR(ctx)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition, condition.ErrorReason, condition.SeverityError,
			"NHC/SNR check failed"))
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, condition.ErrorReason, condition.SeverityError,
			NHCRequiredMessage))
		return ctrl.Result{}, err
	}
	if !nhcSNROk {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition, NHCNotFoundReason, condition.SeverityError,
			NHCRequiredMessage))
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, NHCNotFoundReason, condition.SeverityError,
			NHCRequiredMessage))
		return ctrl.Result{}, nil
	}
	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, "NHC and SNR are available")

	if instance.Spec.Disabled {
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "PVC remediation is disabled")
		return ctrl.Result{}, nil
	}

	// 2) Determine namespaces to watch (CR namespace if not specified)
	namespaces := instance.Spec.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{instance.Namespace}
	}

	// 3) Find nodes that are NotReady or have remediation in progress (simplified: check Node conditions)
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}

	unhealthyNodes := make(map[string]bool)
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		if isNodeUnhealthy(n) {
			unhealthyNodes[n.Name] = true
		}
	}

	if len(unhealthyNodes) == 0 {
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "No unhealthy nodes; monitoring")
		return ctrl.Result{}, nil
	}

	// Log which nodes are unhealthy so we can confirm the controller sees Node updates (e.g. after virsh destroy)
	unhealthyNames := make([]string, 0, len(unhealthyNodes))
	for name := range unhealthyNodes {
		unhealthyNames = append(unhealthyNames, name)
	}
	Log.Info("Unhealthy nodes detected, checking for local PVCs to delete", "nodes", unhealthyNames)

	maxUnhealthy := instance.Spec.MaxUnhealthyNodes
	if maxUnhealthy > 0 && len(unhealthyNodes) > int(maxUnhealthy) {
		Log.Info("Skipping PVC remediation: unhealthy node count exceeds maxUnhealthyNodes",
			"unhealthyCount", len(unhealthyNodes), "maxUnhealthyNodes", maxUnhealthy)
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition,
			fmt.Sprintf("PVC remediation skipped: %d unhealthy nodes exceed maxUnhealthyNodes (%d)",
				len(unhealthyNodes), maxUnhealthy))
		return ctrl.Result{}, nil
	}

	// 4) For each namespace, find PVCs to remediate: either already marked pending-deletion (resume after controller restart) or local on unhealthy node (then annotate + delete)
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs", "namespace", ns)
			continue
		}
		Log.Info("Listing PVCs in namespace for unhealthy-node remediation", "namespace", ns, "pvcCount", len(pvcList.Items))
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			pvcKey := client.ObjectKeyFromObject(pvc)

			// Resume path (Option B): PVC was previously marked for deletion but controller restarted before delete completed
			if pvc.Annotations != nil && pvc.Annotations[PodRemediatorPendingDeletionAnnotation] != "" {
				nodeName := pvc.Annotations[PodRemediatorPendingDeletionAnnotation]
				Log.Info("Resuming: deleting PVC marked pending-deletion (controller may have restarted)", "pvc", pvcKey, "node", nodeName)
				if err := r.Delete(ctx, pvc); err != nil && !k8s_errors.IsNotFound(err) {
					Log.Error(err, "delete PVC (resume)", "pvc", pvcKey)
					continue
				}
				continue
			}

			if pvc.Spec.VolumeName == "" {
				Log.Info("Skipping PVC (unbound)", "pvc", pvcKey)
				continue
			}
			pv := &corev1.PersistentVolume{}
			if err := r.Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
				if k8s_errors.IsNotFound(err) {
					Log.Info("Skipping PVC (PV not found)", "pvc", pvcKey, "volumeName", pvc.Spec.VolumeName)
					continue
				}
				Log.Error(err, "get PV", "pv", pvc.Spec.VolumeName)
				continue
			}
			if !isLocalPV(pv) {
				Log.Info("Skipping PVC (PV not local)", "pvc", pvcKey, "pv", pv.Name)
				continue
			}
			nodeName := getLocalPVNodeName(pv)
			if nodeName == "" {
				Log.Info("Skipping PVC (PV node affinity hostname not found)", "pvc", pvcKey, "pv", pv.Name)
				continue
			}
			if !unhealthyNodes[nodeName] {
				Log.Info("Skipping PVC (node not unhealthy)", "pvc", pvcKey, "node", nodeName)
				continue
			}
			// Persist intent (Option B): annotate before delete so a restarted controller can resume
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[PodRemediatorPendingDeletionAnnotation] = nodeName
			if err := r.Patch(ctx, pvc, client.MergeFrom(oldPVC)); err != nil {
				Log.Error(err, "patch PVC with pending-deletion annotation", "pvc", pvcKey)
				continue
			}
			Log.Info("Deleting PVC bound to unhealthy node (intent persisted via annotation)", "pvc", pvcKey, "node", nodeName)
			if err := r.Delete(ctx, pvc); err != nil && !k8s_errors.IsNotFound(err) {
				Log.Error(err, "delete PVC", "pvc", pvcKey)
				continue
			}
		}
	}

	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "Monitoring; remediated PVCs on unhealthy nodes if any")
	return ctrl.Result{}, nil
}

func isNodeUnhealthy(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status != corev1.ConditionTrue {
				return true
			}
			return false
		}
	}
	return false
}

func isLocalPV(pv *corev1.PersistentVolume) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return false
	}
	// local volume type or CSI with node affinity
	// Check for local volume: either PersistentVolumeLocal or storage class provisioner
	if pv.Spec.Local != nil {
		return true
	}
	// CSI volumes with node affinity are typically local-like (e.g. local-path, openstack cinder with node affinity)
	if pv.Spec.CSI != nil && len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) > 0 {
		return true
	}
	// HostPath with node affinity
	if pv.Spec.HostPath != nil && len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) > 0 {
		return true
	}
	return false
}

// Known topology keys that carry the node name for local/CSI volumes (e.g. LVMS/TopoLVM).
var localPVNodeTopologyKeys = []string{
	corev1.LabelHostname,           // kubernetes.io/hostname (hostPath, local, many CSI)
	"topology.topolvm.io/node",     // TopoLVM / Red Hat LVMS
	"topology.lvms.io/node",        // LVMS variant
}

func getLocalPVNodeName(pv *corev1.PersistentVolume) string {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return ""
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			for _, key := range localPVNodeTopologyKeys {
				if expr.Key == key && len(expr.Values) > 0 {
					return expr.Values[0]
				}
			}
		}
	}
	// Fallback for CSI local volumes: single term with single expression whose key suggests node (e.g. LVMS custom key).
	if pv.Spec.CSI != nil && len(pv.Spec.NodeAffinity.Required.NodeSelectorTerms) == 1 {
		term := &pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0]
		for _, expr := range term.MatchExpressions {
			if len(expr.Values) != 1 {
				continue
			}
			// Accept keys that typically indicate node name (avoid zone/region).
			if strings.Contains(expr.Key, "hostname") || strings.Contains(expr.Key, "node") {
				return expr.Values[0]
			}
		}
		for _, f := range term.MatchFields {
			if (f.Key == corev1.LabelHostname || strings.Contains(f.Key, "hostname")) && len(f.Values) == 1 {
				return f.Values[0]
			}
		}
	}
	return ""
}
