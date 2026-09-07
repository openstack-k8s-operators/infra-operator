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
	"time"

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
	"sigs.k8s.io/controller-runtime/pkg/event"
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

	// DefaultConsentPollInterval is the default for ConsentPollInterval.
	DefaultConsentPollInterval = 2 * time.Minute

	// DefaultPeriodicPollInterval is the default for PeriodicPollInterval.
	DefaultPeriodicPollInterval = 5 * time.Minute
)

var (
	gvrNodeHealthCheck = schema.GroupVersionResource{
		Group: "remediation.medik8s.io", Version: "v1alpha1", Resource: "nodehealthchecks",
	}
	gvrSelfNodeRemediationTemplate = schema.GroupVersionResource{
		Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Resource: "selfnoderemediationtemplates",
	}
	// gvrSelfNodeRemediation is the instance CR created by NHC for each node it has decided to remediate.
	// It exists only while remediation is active; NHC names it with a random suffix (e.g. worker-0-6lwkb).
	gvrSelfNodeRemediation = schema.GroupVersionResource{
		Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Resource: "selfnoderemediations",
	}
)

// PodRemediatorReconciler reconciles a PodRemediator object
type PodRemediatorReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Kclient       kubernetes.Interface
	DynamicClient dynamic.Interface

	// ConsentPollInterval controls how often the controller re-checks PVCs that
	// are in Path C (annotated but waiting for app-operator safe-to-delete consent).
	// Configurable via PODREMEDIATOR_CONSENT_POLL_INTERVAL env var (e.g. "2m").
	// Default: DefaultConsentPollInterval (2 minutes).
	ConsentPollInterval time.Duration

	// PeriodicPollInterval is the safety-net requeue interval for all idle states.
	// Ensures the controller re-evaluates node health after an operator pod restart
	// when nodes are already NotReady and no node-transition event fires.
	// Configurable via PODREMEDIATOR_PERIODIC_POLL_INTERVAL env var (e.g. "5m").
	// Default: DefaultPeriodicPollInterval (5 minutes).
	PeriodicPollInterval time.Duration
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
//+kubebuilder:rbac:groups=remediation.medik8s.io,resources=nodehealthchecks,verbs=get;list;watch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediationtemplates,verbs=get;list;watch
//+kubebuilder:rbac:groups=self-node-remediation.medik8s.io,resources=selfnoderemediations,verbs=get;list;watch

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

// nodeReadyChangedPredicate fires only when a Node's Ready condition status transitions.
// This avoids spurious reconciles from kubelet heartbeat patches (which change resourceVersion
// but not the NodeReady status) while still reacting to actual health state changes.
type nodeReadyChangedPredicate struct {
	predicate.Funcs
}

func (nodeReadyChangedPredicate) Update(e event.UpdateEvent) bool {
	oldNode, ok := e.ObjectOld.(*corev1.Node)
	if !ok {
		return true
	}
	newNode, ok := e.ObjectNew.(*corev1.Node)
	if !ok {
		return true
	}
	return isNodeUnhealthy(oldNode) != isNodeUnhealthy(newNode)
}

// SetupWithManager sets up the controller with the Manager
func (r *PodRemediatorReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	Log := r.GetLogger(ctx)

	// nodeFN enqueues all PodRemediator CRs cluster-wide on node health changes.
	// Nodes are cluster-scoped so all CRs must be notified.
	nodeFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.enqueuePodRemediatorsClusterWide(ctx, Log)
	})

	// pvcFN enqueues only PodRemediator CRs that watch the PVC's namespace.
	// Using a namespace-aware handler avoids a reconcile storm where a PVC annotation
	// change (e.g. pvc-stuck-on-node being set) triggers all CRs cluster-wide, most
	// of which watch unrelated namespaces and would do wasted work.
	pvcFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return r.enqueuePodRemediatorsForNamespace(ctx, o.GetNamespace(), Log)
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&remediationv1.PodRemediator{}).
		// Node watch: reconcile only on Ready condition transitions, not kubelet heartbeats.
		Watches(&corev1.Node{}, nodeFN, builder.WithPredicates(nodeReadyChangedPredicate{})).
		// PVC watch: reconcile on creation/spec changes and annotation changes, scoped to
		// CRs that actually watch the PVC's namespace.
		Watches(&corev1.PersistentVolumeClaim{}, pvcFN, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Complete(r)
}

// enqueuePodRemediatorsClusterWide enqueues all PodRemediator CRs regardless of namespace.
// Used for cluster-scoped events (node health changes) that affect all CRs.
func (r *PodRemediatorReconciler) enqueuePodRemediatorsClusterWide(ctx context.Context, Log logr.Logger) []reconcile.Request {
	list := &remediationv1.PodRemediatorList{}
	if err := r.List(ctx, list); err != nil {
		Log.Error(err, "Unable to list PodRemediator")
		return nil
	}
	result := make([]reconcile.Request, 0, len(list.Items))
	for _, pr := range list.Items {
		result = append(result, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pr)})
	}
	return result
}

// enqueuePodRemediatorsForNamespace enqueues only PodRemediator CRs that watch pvcNamespace.
// A CR watches pvcNamespace if pvcNamespace is in spec.namespaces, or if spec.namespaces is
// empty and the CR's own namespace equals pvcNamespace.
func (r *PodRemediatorReconciler) enqueuePodRemediatorsForNamespace(ctx context.Context, pvcNamespace string, Log logr.Logger) []reconcile.Request {
	list := &remediationv1.PodRemediatorList{}
	if err := r.List(ctx, list); err != nil {
		Log.Error(err, "Unable to list PodRemediator")
		return nil
	}
	var result []reconcile.Request
	for _, pr := range list.Items {
		watched := pr.Spec.Namespaces
		if len(watched) == 0 {
			watched = []string{pr.Namespace}
		}
		for _, ns := range watched {
			if ns == pvcNamespace {
				result = append(result, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pr)})
				break
			}
		}
	}
	return result
}

// reconcileDelete cleans up both pvc-stuck-on-node AND safe-to-delete annotations from all
// watched PVCs before removing the CR finalizer. If any annotation patch fails the finalizer
// is NOT removed and the reconcile requeues, preserving atomicity. Removing safe-to-delete
// prevents stale consent from being honored if the CR is later re-created while a node is
// still unhealthy.
func (r *PodRemediatorReconciler) reconcileDelete(ctx context.Context, instance *remediationv1.PodRemediator, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling PodRemediator delete")

	namespaces := instance.Spec.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{instance.Namespace}
	}
	cleanupFailed := false
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs for annotation cleanup", "namespace", ns)
			cleanupFailed = true
			continue
		}
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			if pvc.Annotations == nil || pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] == "" {
				continue
			}
			oldPVC := pvc.DeepCopy()
			delete(pvc.Annotations, remediationv1.PVCStuckOnNodeAnnotation)
			delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
			if err := r.Patch(ctx, pvc, client.MergeFrom(oldPVC)); err != nil && !k8s_errors.IsNotFound(err) {
				Log.Error(err, "remove remediation annotations during CR delete", "pvc", client.ObjectKeyFromObject(pvc))
				cleanupFailed = true
			}
		}
	}
	if cleanupFailed {
		return ctrl.Result{}, fmt.Errorf("annotation cleanup had errors during CR delete; requeueing to retry before removing finalizer")
	}

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

// getNodesWithActiveSNR returns the set of node names for which NHC has already created a
// SelfNodeRemediation CR (meaning NHC has committed to remediating those nodes).
// Using this set prevents PodRemediator from acting during the window between a node going
// NotReady and NHC deciding to remediate it (transient kubelet restarts, brief partitions).
func (r *PodRemediatorReconciler) getNodesWithActiveSNR(ctx context.Context, Log logr.Logger) (map[string]bool, error) {
	snrList, err := r.DynamicClient.Resource(gvrSelfNodeRemediation).List(ctx, metav1.ListOptions{})
	if err != nil {
		if meta.IsNoMatchError(err) || k8s_errors.IsNotFound(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	nodes := make(map[string]bool, len(snrList.Items))
	for _, snr := range snrList.Items {
		// NHC names the SNR CR with a random suffix (e.g. worker-0-6lwkb); the
		// authoritative node name is in the medik8s annotation.
		if nodeName, ok := snr.GetAnnotations()["remediation.medik8s.io/node-name"]; ok && nodeName != "" {
			nodes[nodeName] = true
		} else if nodeName, ok := snr.GetLabels()["remediation.medik8s.io/node-name"]; ok && nodeName != "" {
			// Fallback to label for forward compatibility if medik8s moves it.
			nodes[nodeName] = true
		} else {
			// CR name does not reliably equal the node name (random suffix); skip rather
			// than inserting a name that will never match an entry in unhealthyNodes.
			Log.Info("SelfNodeRemediation CR has no node-name annotation or label; skipping", "snr", snr.GetName())
		}
	}
	return nodes, nil
}

// deletePodsForPVC force-deletes (gracePeriod=0) all pods in the same namespace that reference
// the PVC. This releases the kubernetes.io/pvc-protection finalizer so the PVC can terminate.
// Force deletion is safe here because SNR guarantees the node is fenced before PodRemediator
// acts; the app operator implicitly consents to pod force-deletion by setting safe-to-delete.
func (r *PodRemediatorReconciler) deletePodsForPVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim, Log logr.Logger) error {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(pvc.Namespace)); err != nil {
		return fmt.Errorf("list pods for PVC %s: %w", pvc.Name, err)
	}
	gracePeriod := int64(0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == pvc.Name {
				Log.Info("Force-deleting pod referencing PVC to release pvc-protection finalizer", "pod", pod.Name, "pvc", pvc.Name)
				if err := r.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &gracePeriod}); err != nil && !k8s_errors.IsNotFound(err) {
					return fmt.Errorf("force-delete pod %s: %w", pod.Name, err)
				}
				break
			}
		}
	}
	return nil
}

// cleanupStaleAnnotations removes pvc-stuck-on-node and safe-to-delete from PVCs whose
// stuck node is NOT in rawUnhealthyNodes (i.e. the node has truly recovered to Ready).
// Only called when rawUnhealthyNodes is empty (all nodes healthy); in all other cases
// stale-annotation cleanup is handled by Path A in the main PVC loop, which also uses
// rawUnhealthyNodes as the recovery gate.
func (r *PodRemediatorReconciler) cleanupStaleAnnotations(ctx context.Context, namespaces []string, Log logr.Logger) {
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs for stale-annotation cleanup", "namespace", ns)
			continue
		}
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			if pvc.Annotations == nil || pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] == "" {
				continue
			}
			stuckOnNode := pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]
			oldPVC := pvc.DeepCopy()
			delete(pvc.Annotations, remediationv1.PVCStuckOnNodeAnnotation)
			delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
			if err := r.Patch(ctx, pvc, client.MergeFrom(oldPVC)); err != nil && !k8s_errors.IsNotFound(err) {
				Log.Error(err, "remove stale remediation annotations (all nodes healthy)", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
			} else {
				Log.Info("All nodes healthy; removed stale annotations", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
			}
		}
	}
}

// effectiveInterval returns the CR-spec value if set, otherwise the reconciler
// field (from env var), otherwise the hardcoded default.
func effectiveInterval(specVal *metav1.Duration, reconcilerVal time.Duration, defaultVal time.Duration) time.Duration {
	if specVal != nil && specVal.Duration > 0 {
		return specVal.Duration
	}
	if reconcilerVal > 0 {
		return reconcilerVal
	}
	return defaultVal
}

func (r *PodRemediatorReconciler) reconcileNormal(ctx context.Context, instance *remediationv1.PodRemediator) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	// Resolve effective poll intervals: CR spec > env-var reconciler field > hardcoded default.
	consentPoll := effectiveInterval(instance.Spec.ConsentPollInterval, r.ConsentPollInterval, DefaultConsentPollInterval)
	periodicPoll := effectiveInterval(instance.Spec.PeriodicPollInterval, r.PeriodicPollInterval, DefaultPeriodicPollInterval)

	// Validate intervals: reject zero or negative values to prevent tight requeue loops.
	const minInterval = time.Second
	if consentPoll < minInterval || periodicPoll < minInterval {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, condition.ErrorReason, condition.SeverityError,
			"Invalid poll interval (consentPollInterval=%s periodicPollInterval=%s): must be >= 1s",
			consentPoll, periodicPoll))
		return ctrl.Result{}, nil
	}

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
		return ctrl.Result{RequeueAfter: periodicPoll}, nil
	}
	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, "NHC and SNR are available")

	if instance.Spec.Disabled {
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "PVC remediation is disabled")
		return ctrl.Result{RequeueAfter: periodicPoll}, nil
	}

	// 2) Determine namespaces to watch (CR namespace if not specified)
	namespaces := instance.Spec.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{instance.Namespace}
	}

	// 3) Build rawUnhealthyNodes: all nodes currently NotReady, regardless of SNR state.
	// This is the authoritative set for Path A (cleanup) and Path B (deletion): a PVC
	// annotation should only be removed when the node is truly back to Ready, not just
	// because its SNR CR expired while the node is still down.
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}
	rawUnhealthyNodes := make(map[string]bool)
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		if isNodeUnhealthy(n) {
			rawUnhealthyNodes[n.Name] = true
		}
	}

	if len(rawUnhealthyNodes) == 0 {
		// All nodes are truly healthy: clean up any stale annotations from previous fault cycles.
		r.cleanupStaleAnnotations(ctx, namespaces, Log)
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "No unhealthy nodes; monitoring")
		// Periodic safety-net: requeue so a restarted operator re-evaluates node health
		// rather than waiting for a node transition event that may never arrive.
		return ctrl.Result{RequeueAfter: periodicPoll}, nil
	}

	// 4) Build snrGatedNodes: subset of rawUnhealthyNodes that have an active SNR CR.
	// Used exclusively for Path D (new annotations): only annotate PVCs when NHC has
	// committed to remediation. Nodes in rawUnhealthyNodes but not snrGatedNodes are
	// either transient (pre-NHC window) or post-fencing; either way, no new annotations
	// should be started but existing in-flight handshakes (Paths A/B/C) must continue.
	snrNodes, err := r.getNodesWithActiveSNR(ctx, Log)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list SelfNodeRemediation CRs: %w", err)
	}
	snrGatedNodes := make(map[string]bool)
	for nodeName := range rawUnhealthyNodes {
		if snrNodes[nodeName] {
			snrGatedNodes[nodeName] = true
		} else {
			Log.Info("Unhealthy node has no SelfNodeRemediation CR yet; new annotations paused", "node", nodeName)
		}
	}

	unhealthyNames := make([]string, 0, len(rawUnhealthyNodes))
	for name := range rawUnhealthyNodes {
		unhealthyNames = append(unhealthyNames, name)
	}
	Log.Info("Unhealthy nodes detected, scanning PVCs", "nodes", unhealthyNames, "snrActive", len(snrGatedNodes))

	// 5) Scan namespaces: drive in-flight handshakes (Paths A/B/C) and detect new stuck
	// PVCs (Path D, SNR-gated). Paths A/B/C use rawUnhealthyNodes so they are not
	// disrupted by SNR lifecycle changes (expiry after fencing, etc.).
	hadError := false
	waitingForConsent := 0
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs", "namespace", ns)
			hadError = true
			continue
		}
		Log.Info("Scanning PVCs for remediation", "namespace", ns, "pvcCount", len(pvcList.Items))
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			pvcKey := client.ObjectKeyFromObject(pvc)

			stuckOnNode := ""
			if pvc.Annotations != nil {
				stuckOnNode = pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]
			}

			if stuckOnNode != "" {
				// Path A: the node has truly recovered (back to Ready). Remove both annotations
				// so the app operator must re-consent on the next independent fault event.
				if !rawUnhealthyNodes[stuckOnNode] {
					oldPVC := pvc.DeepCopy()
					delete(pvc.Annotations, remediationv1.PVCStuckOnNodeAnnotation)
					delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
					if err := r.Patch(ctx, pvc, client.MergeFrom(oldPVC)); err != nil && !k8s_errors.IsNotFound(err) {
						Log.Error(err, "remove remediation annotations (node recovered)", "pvc", pvcKey, "node", stuckOnNode)
					} else {
						Log.Info("Node recovered; removed remediation annotations", "pvc", pvcKey, "node", stuckOnNode)
					}
					continue
				}

				// Path B: node still unhealthy and app operator consented — delete PVC.
				// Uses rawUnhealthyNodes (not snrGatedNodes) so deletion proceeds even if the
				// SNR CR expired post-fencing while the node is still NotReady.
				// The app operator implicitly accepts pod force-deletion by granting safe-to-delete
				// (see remediationv1.PVCStuckOnNodeAnnotation contract comment above).
				if pvc.Annotations[remediationv1.SafeToDeleteAnnotation] == "true" {
					Log.Info("Deleting PVC (stuck on unhealthy node, safe-to-delete granted)", "pvc", pvcKey, "node", stuckOnNode)
					if err := r.deletePodsForPVC(ctx, pvc, Log); err != nil {
						Log.Error(err, "delete pods for PVC", "pvc", pvcKey)
						hadError = true
						continue
					}
					if err := r.Delete(ctx, pvc); err != nil && !k8s_errors.IsNotFound(err) {
						Log.Error(err, "delete PVC", "pvc", pvcKey)
					}
					continue
				}

				// Path C: node still unhealthy, no consent yet — wait for app operator.
				// The PVC watch (AnnotationChangedPredicate) triggers when safe-to-delete is set.
				// The periodic requeue below provides liveness if the event is missed.
				Log.V(1).Info("PVC stuck on unhealthy node; waiting for app-operator safe-to-delete consent", "pvc", pvcKey, "node", stuckOnNode)
				waitingForConsent++
				continue
			}

			// Path D: new local PVC on a node under active SNR remediation — start the handshake.
			// Gated on snrGatedNodes (not rawUnhealthyNodes): we only annotate when NHC has
			// committed, to avoid acting during transient NotReady windows.
			if pvc.Spec.VolumeName == "" {
				continue
			}
			pv := &corev1.PersistentVolume{}
			if err := r.Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
				if k8s_errors.IsNotFound(err) {
					continue
				}
				Log.Error(err, "get PV", "pv", pvc.Spec.VolumeName)
				hadError = true
				continue
			}
			if !isLocalPV(pv) {
				continue
			}
			nodeName := getLocalPVNodeName(pv, Log)
			if nodeName == "" {
				continue
			}
			if !snrGatedNodes[nodeName] {
				continue
			}
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = nodeName
			if err := r.Patch(ctx, pvc, client.MergeFrom(oldPVC)); err != nil {
				Log.Error(err, "annotate PVC with pvc-stuck-on-node", "pvc", pvcKey)
				hadError = true
				continue
			}
			Log.Info("Local PVC on unhealthy node; annotated pvc-stuck-on-node, awaiting safe-to-delete", "pvc", pvcKey, "node", nodeName)
		}
	}

	if hadError {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, condition.ErrorReason, condition.SeverityWarning,
			"Partial scan: errors listing PVCs or fetching PVs; will retry"))
		return ctrl.Result{}, fmt.Errorf("partial scan errors during PVC remediation; requeueing")
	}

	if waitingForConsent > 0 {
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition,
			"%d PVC(s) waiting for app-operator safe-to-delete consent", waitingForConsent)
		// Requeue periodically so Path C is re-evaluated even if annotation events are missed.
		return ctrl.Result{RequeueAfter: consentPoll}, nil
	}

	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "Monitoring; remediating PVCs on unhealthy nodes as authorized")
	return ctrl.Result{RequeueAfter: periodicPoll}, nil
}

func isNodeUnhealthy(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status != corev1.ConditionTrue
		}
	}
	return false
}

func isLocalPV(pv *corev1.PersistentVolume) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return false
	}
	if pv.Spec.Local != nil {
		return true
	}
	// For CSI and HostPath, require a known node-pinning topology key.
	// Zone-affinity CSI volumes (e.g. Cinder: topology.cinder.csi.openstack.org/zone)
	// are reattachable across nodes and must not be treated as node-local.
	if pv.Spec.CSI != nil || pv.Spec.HostPath != nil {
		return pvHasLocalTopologyKey(pv)
	}
	return false
}

// pvHasLocalTopologyKey returns true when the PV's required node affinity contains at least one
// expression keyed by a known node-pinning topology key (hostname or LVMS/TopoLVM node key).
// Add new keys to localPVNodeTopologyKeys when supporting additional local CSI drivers.
func pvHasLocalTopologyKey(pv *corev1.PersistentVolume) bool {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return false
	}
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			for _, key := range localPVNodeTopologyKeys {
				if expr.Key == key {
					return true
				}
			}
		}
	}
	return false
}

// Known topology keys that carry the node name for local/CSI volumes (e.g. LVMS/TopoLVM).
// Add new keys here when supporting additional local CSI drivers; do not use heuristics.
var localPVNodeTopologyKeys = []string{
	corev1.LabelHostname,       // kubernetes.io/hostname (hostPath, local, many CSI)
	"topology.topolvm.io/node", // TopoLVM / Red Hat LVMS
	"topology.lvms.io/node",    // LVMS variant
}

// getLocalPVNodeName returns the node name from the PV's required node affinity using only
// the known topology keys from localPVNodeTopologyKeys. Returns "" if not found.
//
// Note: isLocalPV returns true for spec.local PVs without checking the topology key, so a
// spec.local PV with a non-standard affinity key will return "" here and be silently skipped.
// In practice spec.local PVs use kubernetes.io/hostname; if you see unexpected skips, add
// the relevant key to localPVNodeTopologyKeys.
func getLocalPVNodeName(pv *corev1.PersistentVolume, Log logr.Logger) string {
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
	// No known topology key found. Log a warning so operators can diagnose unexpected skips
	// and add the missing key to localPVNodeTopologyKeys.
	Log.Info("PV has no known node-pinning topology key; skipping (add key to localPVNodeTopologyKeys if needed)",
		"pv", pv.Name, "knownKeys", localPVNodeTopologyKeys)
	return ""
}
