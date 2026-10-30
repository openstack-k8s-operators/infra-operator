/*
Copyright 2026.

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
	"errors"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
)

// pvcRemediationScan holds the cluster snapshot and accumulated outcomes for
// one PVC scan performed by reconcileNormal.
type pvcRemediationScan struct {
	instance          *remediationv1.PodRemediator
	nodeList          *corev1.NodeList
	rawUnhealthyNodes map[string]bool
	allNodeNames      map[string]bool
	nodeUIDByName     map[string]string
	snrNodes          map[string]*unstructured.Unstructured
	log               logr.Logger

	hadError                 bool
	pendingCommittedDeletion bool
	blockedByReplacementPod  string
	waitingForConsent        int
	waitingForFencing        int
	pausedRequests           int
}

// processExistingPVCHandshake handles an existing pvc-stuck-on-node request.
// It covers these cases:
//   - A recovered Node clears the request.
//   - A missing Node is not treated as recovery; without current SNR fencing
//     evidence, the request remains pending.
//   - A changed request identity is refreshed and requires new consent.
//   - A paused request retains its annotations and any existing consent.
//   - Matching consent proceeds to PV-locality validation and PVC deletion;
//     otherwise, the request waits for consent.
func (r *PodRemediatorReconciler) processExistingPVCHandshake(
	ctx context.Context,
	scan *pvcRemediationScan,
	pvc *corev1.PersistentVolumeClaim,
	stuckOnNode string,
) {
	// Recovered Node: clear the request only when the Node still exists and is
	// not marked unhealthy.
	if !scan.rawUnhealthyNodes[stuckOnNode] && scan.allNodeNames[stuckOnNode] {
		oldPVC := pvc.DeepCopy()
		clearRemediationAnnotations(pvc)
		if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
			scan.log.Error(err, "remove remediation annotations (node recovered)", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
			scan.hadError = true
		} else {
			scan.log.Info("Node recovered; removed remediation annotations", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
		}
		return
	}
	if !scan.rawUnhealthyNodes[stuckOnNode] && !scan.allNodeNames[stuckOnNode] {
		scan.log.Info("Stuck node no longer exists; keeping annotations instead of treating as recovery", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
	}

	// Annotations identify the consent request, but never replace live SNR
	// fencing evidence. Keep the request pending if that evidence is absent.
	snr := scan.snrNodes[stuckOnNode]
	if snr == nil {
		scan.waitingForFencing++
		return
	}
	requestID := remediationRequestID(scan.instance, pvc, snr)
	if requestID == "" {
		scan.hadError = true
		return
	}
	if pvc.Annotations[remediationv1.RequestIDAnnotation] != requestID ||
		pvc.Annotations[remediationv1.RemediatorUIDAnnotation] != string(scan.instance.UID) {
		if err := r.startRequest(ctx, scan.instance, pvc, stuckOnNode, scan.nodeUIDByName[stuckOnNode], requestID); err != nil {
			scan.hadError = true
		}
		scan.waitingForConsent++
		return
	}
	if scan.instance.Spec.RemediationPaused {
		scan.pausedRequests++
		scan.log.Info("PVC remediation is paused; retaining the stuck PVC request and any consent", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode, "consentGranted", remediationv1.HasRemediationConsent(pvc.Annotations))
		return
	}
	if remediationv1.HasRemediationConsent(pvc.Annotations) {
		r.deleteConsentedPVC(ctx, scan, pvc, stuckOnNode, requestID)
		return
	}

	// Waiting for consent: fencing is confirmed, but app-operator consent has not arrived.
	// The PVC watch handles annotation changes; polling provides a liveness fallback.
	scan.log.Info("PVC remediation is waiting for app-operator safe-to-delete consent", "pvc", client.ObjectKeyFromObject(pvc), "node", stuckOnNode)
	scan.waitingForConsent++
}

// deleteConsentedPVC revalidates PV provenance after consent so annotations
// alone cannot authorize deletion of an unrelated PVC.
func (r *PodRemediatorReconciler) deleteConsentedPVC(
	ctx context.Context,
	scan *pvcRemediationScan,
	pvc *corev1.PersistentVolumeClaim,
	stuckOnNode string,
	requestID string,
) {
	pvcKey := client.ObjectKeyFromObject(pvc)
	if pvc.Spec.VolumeName == "" {
		scan.log.Info("Refusing to delete PVC: safe-to-delete set but PVC has no bound PV", "pvc", pvcKey, "node", stuckOnNode)
		return
	}
	pv := &corev1.PersistentVolume{}
	if err := r.safetyReader().Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
		if k8s_errors.IsNotFound(err) {
			return
		}
		scan.log.Error(err, "get PV for delete-path validation", "pv", pvc.Spec.VolumeName, "pvc", pvcKey)
		scan.hadError = true
		return
	}

	pvNode := resolveLocalPVNodeName(pv, scan.nodeList.Items, scan.log)
	// An owner-bound handshake can survive Node removal only when the PV
	// explicitly names that Node; its hostname label can no longer be resolved.
	if pvNode == "" && !scan.allNodeNames[stuckOnNode] &&
		pvc.Annotations[remediationv1.RemediatorUIDAnnotation] == string(scan.instance.UID) &&
		pvc.Annotations[remediationv1.RequestIDAnnotation] == requestID &&
		pvc.Annotations[remediationv1.FencingNodeUIDAnnotation] != "" &&
		getDirectLocalPVNodeName(pv) == stuckOnNode {
		pvNode = stuckOnNode
	}
	if pvNode != stuckOnNode {
		scan.log.Info("Refusing to delete PVC: PV is not node-local or not pinned to the stuck node (possible forged annotation)",
			"pvc", pvcKey, "node", stuckOnNode, "pvNode", pvNode)
		return
	}

	pending, blocked, err := r.deletePVCAndPods(ctx, scan.instance, pvc, stuckOnNode, scan.log)
	if err != nil {
		if errors.Is(err, errPVCRemediationPaused) {
			scan.pausedRequests++
			scan.log.Info("PVC remediation became paused before deletion started; retaining request and consent", "pvc", pvcKey, "node", stuckOnNode)
		} else {
			scan.log.Error(err, "delete consented PVC and observed pods", "pvc", pvcKey)
			scan.hadError = true
		}
		return
	}
	if pending {
		scan.pendingCommittedDeletion = true
		if blocked != "" {
			scan.blockedByReplacementPod = blocked
		}
	}
}

// processNewPVCHandshake starts a request for a PVC bound to a local PV on an
// unhealthy node after current SNR fencing evidence is confirmed.
func (r *PodRemediatorReconciler) processNewPVCHandshake(
	ctx context.Context,
	scan *pvcRemediationScan,
	pvc *corev1.PersistentVolumeClaim,
) {
	// Drop orphaned consent when there is no active unhealthy-node request.
	if len(scan.rawUnhealthyNodes) == 0 && pvc.Annotations[remediationv1.SafeToDeleteAnnotation] != "" {
		oldPVC := pvc.DeepCopy()
		delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
		delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
		if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
			scan.hadError = true
			scan.log.Error(err, "remove orphaned consent", "pvc", client.ObjectKeyFromObject(pvc))
		}
	}

	if pvc.Spec.VolumeName == "" {
		return
	}
	pv := &corev1.PersistentVolume{}
	if err := r.safetyReader().Get(ctx, client.ObjectKey{Name: pvc.Spec.VolumeName}, pv); err != nil {
		if k8s_errors.IsNotFound(err) {
			return
		}
		scan.log.Error(err, "get PV", "pv", pvc.Spec.VolumeName)
		scan.hadError = true
		return
	}
	nodeName := resolveLocalPVNodeName(pv, scan.nodeList.Items, scan.log)
	if nodeName == "" || !scan.rawUnhealthyNodes[nodeName] {
		return
	}
	snr := scan.snrNodes[nodeName]
	if snr == nil {
		scan.waitingForFencing++
		return
	}
	requestID := remediationRequestID(scan.instance, pvc, snr)
	if requestID == "" {
		scan.hadError = true
		return
	}
	if err := r.startRequest(ctx, scan.instance, pvc, nodeName, scan.nodeUIDByName[nodeName], requestID); err != nil {
		scan.log.Error(err, "start PVC remediation request", "pvc", client.ObjectKeyFromObject(pvc))
		scan.hadError = true
		return
	}
	scan.log.Info("Local PVC on unhealthy node; requested fault-scoped consent", "pvc", client.ObjectKeyFromObject(pvc), "node", nodeName)
	if scan.instance.Spec.RemediationPaused {
		scan.pausedRequests++
	}
}
