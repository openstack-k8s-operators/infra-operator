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

// Package remediation implements coordinated PVC remediation on unhealthy nodes.
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	// ReplacementPodBlocksPVCDeletionReason identifies a committed deletion
	// waiting for the workload owner to release a new Pod using the PVC.
	ReplacementPodBlocksPVCDeletionReason = "ReplacementPodBlocksPVCDeletion"

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

// PodRemediatorReconciler reconciles a PodRemediator object.
type PodRemediatorReconciler struct {
	client.Client
	APIReader     client.Reader
	Scheme        *runtime.Scheme
	Kclient       kubernetes.Interface
	DynamicClient dynamic.Interface

	// ConsentPollInterval controls how often the controller re-checks PVCs
	// waiting for fencing or app-operator safe-to-delete consent.
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

// Update enqueues reconciliation only when NodeReady health changes.
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
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}

	// nodeFN enqueues all PodRemediator CRs cluster-wide on node health changes.
	// Nodes are cluster-scoped so all CRs must be notified.
	nodeFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
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
		WatchesRawSource(&snrSource{reconciler: r}).
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

// enqueuePodRemediatorsForNamespace enqueues only PodRemediator CRs in pvcNamespace.
func (r *PodRemediatorReconciler) enqueuePodRemediatorsForNamespace(ctx context.Context, pvcNamespace string, Log logr.Logger) []reconcile.Request {
	list := &remediationv1.PodRemediatorList{}
	if err := r.List(ctx, list); err != nil {
		Log.Error(err, "Unable to list PodRemediator")
		return nil
	}
	var result []reconcile.Request
	for _, pr := range list.Items {
		if pr.Namespace == pvcNamespace {
			result = append(result, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pr)})
		}
	}
	return result
}

// reconcileDelete cleans up both pvc-stuck-on-node AND safe-to-delete annotations from PVCs
// in the CR namespace before removing the CR finalizer. If any annotation patch fails the finalizer
// is NOT removed and the reconcile requeues, preserving atomicity. Removing safe-to-delete
// prevents stale consent from being honored if the CR is later re-created while a node is
// still unhealthy.
func (r *PodRemediatorReconciler) reconcileDelete(ctx context.Context, instance *remediationv1.PodRemediator, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling PodRemediator delete")

	namespaces := []string{instance.Namespace}
	resumeResult, err := r.resumeCommittedPVCDeletions(ctx, namespaces, string(instance.UID), Log)
	if err != nil {
		return ctrl.Result{}, err
	}
	if resumeResult.Pending {
		// Keep the CR finalizer until every committed PVC deletion has finished.
		if resumeResult.BlockedByReplacementPod != "" {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ReadyCondition, ReplacementPodBlocksPVCDeletionReason, condition.SeverityWarning,
				"%s", resumeResult.BlockedByReplacementPod))
		} else {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "Completing a previously committed PVC deletion")
		}
		return ctrl.Result{RequeueAfter: DefaultConsentPollInterval}, nil
	}
	if err := r.cleanupAnnotations(ctx, namespaces, string(instance.UID), true); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	return ctrl.Result{}, nil
}

// cleanupAnnotations cancels pending handshakes on disable or CR deletion.
// Failures must be retried so consent cannot survive a maintenance window.
func (r *PodRemediatorReconciler) cleanupAnnotations(ctx context.Context, namespaces []string, owner string, includeLegacy bool) error {
	Log := r.GetLogger(ctx)
	cleanupFailed := false
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.safetyReader().List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs for annotation cleanup", "namespace", ns)
			cleanupFailed = true
			continue
		}
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			if pvc.Annotations == nil {
				continue
			}
			pvcOwner := pvc.Annotations[remediationv1.RemediatorUIDAnnotation]
			if pvcOwner != owner && (pvcOwner != "" || !includeLegacy) {
				continue
			}
			// Clean up both keys, including an orphaned safe-to-delete with no
			// pvc-stuck-on-node, so no stale consent is left behind after CR removal.
			if pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] == "" &&
				pvc.Annotations[remediationv1.SafeToDeleteAnnotation] == "" &&
				pvc.Annotations[remediationv1.RequestIDAnnotation] == "" &&
				pvc.Annotations[remediationv1.ConsentIDAnnotation] == "" &&
				pvc.Annotations[remediationv1.RemediatorUIDAnnotation] == "" &&
				pvc.Annotations[remediationv1.FencingNodeUIDAnnotation] == "" {
				continue
			}
			oldPVC := pvc.DeepCopy()
			clearRemediationAnnotations(pvc)
			if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
				Log.Error(err, "remove remediation annotations", "pvc", client.ObjectKeyFromObject(pvc))
				cleanupFailed = true
			}
		}
	}
	if cleanupFailed {
		return fmt.Errorf("annotation cleanup had errors; requeueing to retry")
	}
	return nil
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

// getNodesWithFencedSNR returns only nodes whose active SNR has crossed the reboot
// safety barrier. CR existence (or an elapsed timestamp alone) is not fencing proof.
// In medik8s/self-node-remediation, waitForNodeRebooted advances to Reboot-Completed
// before SNR removes workloads; Fencing-Completed follows that cleanup.
func (r *PodRemediatorReconciler) getNodesWithFencedSNR(ctx context.Context, Log logr.Logger) (map[string]*unstructured.Unstructured, error) {
	snrList, err := r.DynamicClient.Resource(gvrSelfNodeRemediation).List(ctx, metav1.ListOptions{})
	if err != nil {
		if meta.IsNoMatchError(err) || k8s_errors.IsNotFound(err) {
			return map[string]*unstructured.Unstructured{}, nil
		}
		return nil, err
	}
	active := make(map[string]int)
	fenced := make(map[string]*unstructured.Unstructured)
	for i := range snrList.Items {
		snr := &snrList.Items[i]
		if snr.GetDeletionTimestamp() != nil {
			continue
		}
		node := snrNodeName(snr)
		if node == "" {
			continue
		}
		active[node]++
		phase, _, phaseErr := unstructured.NestedString(snr.Object, "status", "phase")
		if phaseErr == nil && snr.GetUID() != "" && (phase == "Reboot-Completed" || phase == "Fencing-Completed") {
			fenced[node] = snr
		}
	}
	for node, count := range active {
		if count > 1 {
			// Never let an older completed SNR authorize a newer pending fault.
			delete(fenced, node)
			Log.Info("Multiple active SNRs for node; waiting for unambiguous fencing", "node", node)
		}
	}
	return fenced, nil
}

// effectiveInterval returns the CR-spec value if set, otherwise the reconciler
// field (from env var), otherwise the hardcoded default.
func effectiveInterval(specVal *metav1.Duration, reconcilerVal time.Duration, defaultVal time.Duration) time.Duration {
	if specVal != nil {
		return specVal.Duration
	}
	if reconcilerVal > 0 {
		return reconcilerVal
	}
	return defaultVal
}

// reconcileNormal scans PVCs in the CR namespace and advances their fencing consent handshakes.
func (r *PodRemediatorReconciler) reconcileNormal(ctx context.Context, instance *remediationv1.PodRemediator) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	// Resolve effective poll intervals: CR spec > env-var reconciler field > hardcoded default.
	consentPoll := effectiveInterval(instance.Spec.ConsentPollInterval, r.ConsentPollInterval, DefaultConsentPollInterval)
	periodicPoll := effectiveInterval(instance.Spec.PeriodicPollInterval, r.PeriodicPollInterval, DefaultPeriodicPollInterval)

	namespaces := []string{instance.Namespace}
	resumeResult, err := r.resumeCommittedPVCDeletions(ctx, namespaces, string(instance.UID), Log)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, condition.ErrorReason, condition.SeverityWarning, "%s", err))
		return ctrl.Result{}, err
	}
	// Cancel consent even if dependencies disappear during maintenance. This also
	// covers recovery and a subsequent failure while remediation is disabled.
	if instance.Spec.Disabled {
		if err := r.cleanupAnnotations(ctx, namespaces, string(instance.UID), true); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ReadyCondition, condition.ErrorReason, condition.SeverityWarning, "%s", err))
			return ctrl.Result{}, err
		}
	}
	if resumeResult.Pending {
		if resumeResult.BlockedByReplacementPod != "" {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ReadyCondition, ReplacementPodBlocksPVCDeletionReason, condition.SeverityWarning,
				"%s", resumeResult.BlockedByReplacementPod))
		} else {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "Completing a previously committed PVC deletion")
		}
		return ctrl.Result{RequeueAfter: DefaultConsentPollInterval}, nil
	}

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

	// A missing node is not recovered, but its pending handshake must still run
	// when all remaining nodes are healthy.
	nodeList := &corev1.NodeList{}
	if err := r.safetyReader().List(ctx, nodeList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}
	rawUnhealthyNodes := make(map[string]bool)
	allNodeNames := make(map[string]bool, len(nodeList.Items))
	nodeUIDByName := make(map[string]string, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		allNodeNames[n.Name] = true
		nodeUIDByName[n.Name] = string(n.UID)
		if isNodeUnhealthy(n) {
			rawUnhealthyNodes[n.Name] = true
		}
	}

	// Require live fencing evidence for annotation AND deletion. Missing/expired
	// SNRs leave handshakes pending; annotations cannot stand in for fencing proof.
	snrNodes, err := r.getNodesWithFencedSNR(ctx, Log)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list SelfNodeRemediation CRs: %w", err)
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		if snr := snrNodes[node.Name]; snr != nil && !fencingMatchesNode(snr, node) {
			delete(snrNodes, node.Name)
		}
	}
	unhealthyNames := make([]string, 0, len(rawUnhealthyNodes))
	for name := range rawUnhealthyNodes {
		unhealthyNames = append(unhealthyNames, name)
	}
	Log.V(1).Info("Scanning PVCs", "unhealthyNodes", unhealthyNames, "fencedNodes", len(snrNodes))

	// Always scan existing handshakes, including PVCs whose Node was removed.
	hadError := false
	pendingCommittedDeletion := false
	blockedByReplacementPod := ""
	waitingForConsent := 0
	waitingForFencing := 0
	for _, ns := range namespaces {
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.safetyReader().List(ctx, pvcList, client.InNamespace(ns)); err != nil {
			Log.Error(err, "list PVCs", "namespace", ns)
			hadError = true
			continue
		}
		Log.Info("Scanning PVCs for remediation", "namespace", ns, "pvcCount", len(pvcList.Items))
		for i := range pvcList.Items {
			pvc := &pvcList.Items[i]
			pvcKey := client.ObjectKeyFromObject(pvc)
			if _, aborted := resumeResult.AbortedPVCUIDs[string(pvc.UID)]; aborted {
				Log.Info("Skipping PVC after aborting its committed deletion", "pvc", pvcKey)
				continue
			}
			if owner := pvc.Annotations[remediationv1.RemediatorUIDAnnotation]; owner != "" && owner != string(instance.UID) {
				Log.V(1).Info("Skipping PVC owned by another PodRemediator", "pvc", pvcKey, "owner", owner)
				continue
			}

			stuckOnNode := ""
			if pvc.Annotations != nil {
				stuckOnNode = pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]
			}

			if stuckOnNode != "" {
				// Path A: the node has truly recovered (exists AND back to Ready). Remove both
				// annotations so the app operator must re-consent on the next independent fault.
				// A node absent from the cluster (deleted/scaled-out) is NOT treated as recovery:
				// silently stripping the annotations would abort an in-flight handshake and leave
				// the PVC pinned to a node that will never return. Instead fall through to Path
				// B/C so the handshake can still complete (delete on consent, else keep waiting).
				if !rawUnhealthyNodes[stuckOnNode] && allNodeNames[stuckOnNode] {
					oldPVC := pvc.DeepCopy()
					clearRemediationAnnotations(pvc)
					if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
						Log.Error(err, "remove remediation annotations (node recovered)", "pvc", pvcKey, "node", stuckOnNode)
						hadError = true
					} else {
						Log.Info("Node recovered; removed remediation annotations", "pvc", pvcKey, "node", stuckOnNode)
					}
					continue
				}
				if !rawUnhealthyNodes[stuckOnNode] && !allNodeNames[stuckOnNode] {
					Log.Info("Stuck node no longer exists; keeping annotations instead of treating as recovery", "pvc", pvcKey, "node", stuckOnNode)
				}

				// Annotations identify the consent request, but never replace live SNR
				// fencing evidence. This remains true after SNR garbage collection.
				var requestID string
				if snrNodes[stuckOnNode] != nil {
					requestID = remediationRequestID(instance, pvc, snrNodes[stuckOnNode])
					if requestID == "" {
						hadError = true
						continue
					}
					if pvc.Annotations[remediationv1.RequestIDAnnotation] != requestID ||
						pvc.Annotations[remediationv1.RemediatorUIDAnnotation] != string(instance.UID) {
						if err := r.startRequest(ctx, instance, pvc, stuckOnNode, nodeUIDByName[stuckOnNode], requestID); err != nil {
							hadError = true
						}
						waitingForConsent++
						continue
					}
				} else {
					waitingForFencing++
					continue
				}
				if requestID == "" {
					waitingForFencing++
					continue
				}
				if remediationv1.HasRemediationConsent(pvc.Annotations) {
					// Re-validate provenance on the DELETING path: the safe-to-delete
					// annotation alone is not trusted. The PVC's bound PV must still be
					// node-local AND pinned to the node named in pvc-stuck-on-node.
					// This rejects a forged pvc-stuck-on-node + safe-to-delete pair on an
					// unrelated or network-attached PVC, which would otherwise be deleted
					// (Path D validates locality only on the annotation-WRITING path).
					if pvc.Spec.VolumeName == "" {
						Log.Info("Refusing to delete PVC: safe-to-delete set but PVC has no bound PV", "pvc", pvcKey, "node", stuckOnNode)
						continue
					}
					pv := &corev1.PersistentVolume{}
					if err := r.safetyReader().Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
						if k8s_errors.IsNotFound(err) {
							continue
						}
						Log.Error(err, "get PV for delete-path validation", "pv", pvc.Spec.VolumeName, "pvc", pvcKey)
						hadError = true
						continue
					}
					pvNode := resolveLocalPVNodeName(pv, nodeList.Items, Log)
					// An existing, owner-bound handshake can survive removal of its
					// Node object only when the PV explicitly names the Node. A hostname
					// label cannot be resolved after the Node is gone.
					if pvNode == "" && !allNodeNames[stuckOnNode] &&
						pvc.Annotations[remediationv1.RemediatorUIDAnnotation] == string(instance.UID) &&
						pvc.Annotations[remediationv1.RequestIDAnnotation] == requestID &&
						pvc.Annotations[remediationv1.FencingNodeUIDAnnotation] != "" &&
						getDirectLocalPVNodeName(pv) == stuckOnNode {
						pvNode = stuckOnNode
					}
					if pvNode != stuckOnNode {
						Log.Info("Refusing to delete PVC: PV is not node-local or not pinned to the stuck node (possible forged annotation)",
							"pvc", pvcKey, "node", stuckOnNode, "pvNode", pvNode)
						continue
					}
					pending, blocked, err := r.deletePVCAndPods(ctx, instance, pvc, stuckOnNode, Log)
					if err != nil {
						Log.Error(err, "delete consented PVC and observed pods", "pvc", pvcKey)
						hadError = true
					} else if pending {
						pendingCommittedDeletion = true
						if blocked != "" {
							blockedByReplacementPod = blocked
						}
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

			if len(rawUnhealthyNodes) == 0 && pvc.Annotations[remediationv1.SafeToDeleteAnnotation] != "" {
				oldPVC := pvc.DeepCopy()
				delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
				delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
				if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
					hadError = true
					Log.Error(err, "remove orphaned consent", "pvc", pvcKey)
				}
			}

			// Path D: start a new handshake only after SNR confirms fencing.
			if pvc.Spec.VolumeName == "" {
				continue
			}
			pv := &corev1.PersistentVolume{}
			if err := r.safetyReader().Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
				if k8s_errors.IsNotFound(err) {
					continue
				}
				Log.Error(err, "get PV", "pv", pvc.Spec.VolumeName)
				hadError = true
				continue
			}
			nodeName := resolveLocalPVNodeName(pv, nodeList.Items, Log)
			if nodeName == "" {
				continue
			}
			if !rawUnhealthyNodes[nodeName] {
				continue
			}
			if snrNodes[nodeName] == nil {
				waitingForFencing++
				continue
			}
			requestID := remediationRequestID(instance, pvc, snrNodes[nodeName])
			if requestID == "" {
				hadError = true
				continue
			}
			if err := r.startRequest(ctx, instance, pvc, nodeName, nodeUIDByName[nodeName], requestID); err != nil {
				Log.Error(err, "start PVC remediation request", "pvc", pvcKey)
				hadError = true
				continue
			}
			Log.Info("Local PVC on unhealthy node; requested fault-scoped consent", "pvc", pvcKey, "node", nodeName)

		}
	}

	if hadError {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition, condition.ErrorReason, condition.SeverityWarning,
			"Partial scan: errors listing PVCs or fetching PVs; will retry"))
		return ctrl.Result{}, fmt.Errorf("partial scan errors during PVC remediation; requeueing")
	}

	if pendingCommittedDeletion {
		if blockedByReplacementPod != "" {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ReadyCondition, ReplacementPodBlocksPVCDeletionReason, condition.SeverityWarning,
				"%s", blockedByReplacementPod))
		} else {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, "Completing a previously committed PVC deletion")
		}
		return ctrl.Result{RequeueAfter: DefaultConsentPollInterval}, nil
	}

	if waitingForFencing > 0 {
		instance.Status.Conditions.MarkTrue(condition.ReadyCondition,
			"%d PVC(s) waiting for SNR fencing confirmation; %d waiting for consent", waitingForFencing, waitingForConsent)
		return ctrl.Result{RequeueAfter: consentPoll}, nil
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

// isNodeUnhealthy reports whether a Node has a non-true Ready condition.
func isNodeUnhealthy(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status != corev1.ConditionTrue
		}
	}
	return false
}

// isLocalPV reports whether a PV is a supported local volume pinned to one node.
func isLocalPV(pv *corev1.PersistentVolume) bool {
	return (pv.Spec.Local != nil || pv.Spec.CSI != nil || pv.Spec.HostPath != nil) &&
		getLocalPVNodeName(pv, logr.Discard()) != ""
}

// Known topology keys that carry the node name for local/CSI volumes (e.g. LVMS/TopoLVM).
// Add new keys here when supporting additional local CSI drivers; do not use heuristics.
var localPVNodeTopologyKeys = []string{
	corev1.LabelHostname,       // kubernetes.io/hostname (hostPath, local, many CSI)
	"topology.topolvm.io/node", // TopoLVM / Red Hat LVMS
	"topology.lvms.io/node",    // LVMS variant
}

// getLocalPVNodeName accepts only affinity that exclusively pins every OR term
// to the same node. Expressions within a term are ANDed. Ambiguous topology
// expressions fail closed even if another expression could narrow the selection.
func getLocalPVNodeName(pv *corev1.PersistentVolume, Log logr.Logger) string {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return ""
	}
	nodeName := ""
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		termNode := ""
		for _, expr := range term.MatchExpressions {
			for _, key := range localPVNodeTopologyKeys {
				if expr.Key != key {
					continue
				}
				if expr.Operator != corev1.NodeSelectorOpIn || len(expr.Values) != 1 || expr.Values[0] == "" {
					return ""
				}
				if termNode != "" && termNode != expr.Values[0] {
					return ""
				}
				termNode = expr.Values[0]
			}
		}
		if termNode == "" || (nodeName != "" && nodeName != termNode) {
			Log.V(1).Info("PV is not exclusively pinned to one known node; skipping", "pv", pv.Name)
			return ""
		}
		nodeName = termNode
	}
	return nodeName
}

// resolveLocalPVNodeName maps every exclusive topology requirement to an
// actual Node and returns its Kubernetes object name. Topology label values are
// not assumed to be Node.Name; hostname labels in particular commonly differ.
func resolveLocalPVNodeName(pv *corev1.PersistentVolume, nodes []corev1.Node, Log logr.Logger) string {
	if pv.Spec.Local == nil && pv.Spec.CSI == nil && pv.Spec.HostPath == nil {
		return ""
	}
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return ""
	}

	resolvedNodeName := ""
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		if len(term.MatchFields) != 0 {
			return ""
		}
		var termNodeName string
		requirementCount := 0
		for _, expression := range term.MatchExpressions {
			if !isLocalPVNodeTopologyKey(expression.Key) {
				// The additional constraint may change the set of matching Nodes.
				// Resolve only affinity expressed in supported node topology keys.
				return ""
			}
			requirementCount++
			if expression.Operator != corev1.NodeSelectorOpIn || len(expression.Values) != 1 || expression.Values[0] == "" {
				return ""
			}
			nodeName := resolvePVTopologyRequirement(expression.Key, expression.Values[0], nodes)
			if nodeName == "" || (termNodeName != "" && termNodeName != nodeName) {
				Log.V(1).Info("PV topology requirement does not resolve to one consistent Node", "pv", pv.Name, "key", expression.Key, "value", expression.Values[0])
				return ""
			}
			termNodeName = nodeName
		}
		if requirementCount == 0 || termNodeName == "" || (resolvedNodeName != "" && resolvedNodeName != termNodeName) {
			Log.V(1).Info("PV is not exclusively pinned to one actual Node; skipping", "pv", pv.Name)
			return ""
		}
		resolvedNodeName = termNodeName
	}
	return resolvedNodeName
}

// getDirectLocalPVNodeName permits the documented TopoLVM/LVMS node-name
// convention when the Node object has disappeared. Hostname is never a direct
// Node identity, and mixed topology expressions cannot prove exclusivity here.
func getDirectLocalPVNodeName(pv *corev1.PersistentVolume) string {
	if pv.Spec.Local == nil && pv.Spec.CSI == nil && pv.Spec.HostPath == nil {
		return ""
	}
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return ""
	}
	name := ""
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		if len(term.MatchFields) != 0 || len(term.MatchExpressions) != 1 {
			return ""
		}
		expression := term.MatchExpressions[0]
		if expression.Key != "topology.topolvm.io/node" && expression.Key != "topology.lvms.io/node" {
			return ""
		}
		if expression.Operator != corev1.NodeSelectorOpIn || len(expression.Values) != 1 || expression.Values[0] == "" ||
			(name != "" && name != expression.Values[0]) {
			return ""
		}
		name = expression.Values[0]
	}
	return name
}

func isLocalPVNodeTopologyKey(key string) bool {
	for _, knownKey := range localPVNodeTopologyKeys {
		if key == knownKey {
			return true
		}
	}
	return false
}

func resolvePVTopologyRequirement(key, value string, nodes []corev1.Node) string {
	matchingNodes := make([]string, 0, 1)
	for i := range nodes {
		node := &nodes[i]
		if node.Labels[key] == value {
			matchingNodes = append(matchingNodes, node.Name)
		}
	}
	if len(matchingNodes) == 1 {
		return matchingNodes[0]
	}
	if len(matchingNodes) > 1 || key == corev1.LabelHostname {
		return ""
	}

	// TopoLVM/LVMS affinity values are explicitly node identities on some
	// installations. Preserve that convention only when no Node carries the
	// topology label and exactly one Node has the requested object name.
	for i := range nodes {
		if nodes[i].Name == value {
			return nodes[i].Name
		}
	}
	return ""
}
