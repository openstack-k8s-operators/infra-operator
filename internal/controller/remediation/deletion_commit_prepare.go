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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// deletePVCAndPods starts or resumes committed cleanup for a consented PVC.
// The caller reaches it after confirming accepted SNR fencing, request-scoped
// consent, and that the PV is pinned to the stuck node. It requires non-empty
// PVC and PodRemediator UIDs, a node name, the PVC resource version, and
// owner-bound consent. It rechecks consent and live fencing before writing the
// finalized token. That token is the commit boundary: earlier revocation aborts
// preparation, while later reconciles resume cleanup from the durable record.
func (r *PodRemediatorReconciler) deletePVCAndPods(ctx context.Context, instance *remediationv1.PodRemediator, pvc *corev1.PersistentVolumeClaim, node string, Log logr.Logger) (bool, string, error) {
	if pvc.UID == "" || instance.UID == "" || node == "" {
		return false, "", fmt.Errorf("cannot commit PVC %s deletion without PVC, PodRemediator, and node identity", pvc.Name)
	}
	if pvc.ResourceVersion == "" {
		return false, "", fmt.Errorf("cannot commit PVC %s deletion without its resource version", pvc.Name)
	}
	requestID := pvc.Annotations[remediationv1.RequestIDAnnotation]
	if !pvcDeletionConsentMatches(pvc, string(instance.UID), node, requestID) {
		return false, "", fmt.Errorf("cannot commit PVC %s deletion without matching owner-bound consent", pvc.Name)
	}
	paused, err := r.isRemediationPaused(ctx, instance)
	if err != nil {
		return false, "", fmt.Errorf("check PodRemediator pause before deleting PVC %s: %w", pvc.Name, err)
	}
	if paused {
		return false, "", errPVCRemediationPaused
	}
	if pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" ||
		pvc.Annotations[deletionCommitTokenAnnotation] != "" || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
		// A concurrent reconcile may be completing the PVC commit boundary.
		// Let the resume path validate the annotations against its ConfigMap.
		return true, "", nil
	}
	pods, err := r.podsUsingPVC(ctx, pvc)
	if err != nil {
		return false, "", err
	}
	token, err := newDeletionCommitToken()
	if err != nil {
		return false, "", err
	}
	state := deletionCommit{
		PVCName: pvc.Name, PVCUID: string(pvc.UID), PVCResourceVersion: pvc.ResourceVersion,
		RequestID: requestID, RemediatorUID: string(instance.UID), Node: node,
		Token: token, Phase: deletionCommitPhasePrepared,
	}
	for i := range pods {
		pod := &pods[i]
		if pod.Spec.NodeName != "" && pod.Spec.NodeName != node {
			Log.Info("Aborting PVC remediation because a Pod uses the claim on another node", "pvc", pvc.Name, "pod", pod.Name)
			return false, "", r.clearAbortedPVC(ctx, pvc)
		}
		if pod.UID == "" {
			return false, "", fmt.Errorf("cannot commit PVC %s deletion with unidentified Pod %s", pvc.Name, pod.Name)
		}
		state.Pods = append(state.Pods, committedPod{Name: pod.Name, UID: string(pod.UID)})
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return false, "", err
	}
	commit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: deletionCommitName(state.PVCUID), Namespace: pvc.Namespace,
		Annotations: map[string]string{deletionCommitAnnotation: string(encoded)}}}
	if err := r.Create(ctx, commit); err != nil {
		if !k8s_errors.IsAlreadyExists(err) {
			return false, "", fmt.Errorf("prepare PVC %s deletion commit: %w", pvc.Name, err)
		}
		storedCommit := &corev1.ConfigMap{}
		if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(commit), storedCommit); err != nil {
			return false, "", err
		}
		stored, err := decodeDeletionCommit(storedCommit)
		if err != nil || stored.PVCUID != state.PVCUID || stored.RemediatorUID != state.RemediatorUID || stored.PVCName != state.PVCName || stored.Node != state.Node {
			return false, "", fmt.Errorf("PVC %s has a conflicting deletion commit", pvc.Name)
		}
		if !committedDeletion(stored) && (stored.PVCResourceVersion != pvc.ResourceVersion || stored.RequestID != requestID) {
			return false, "", fmt.Errorf("PVC %s has a conflicting provisional deletion commit", pvc.Name)
		}
		commit = storedCommit
		state = stored
	}
	if committedDeletion(state) {
		current := &corev1.PersistentVolumeClaim{}
		if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
			if k8s_errors.IsNotFound(err) {
				if deleteErr := r.Delete(ctx, commit, observedDeleteOptions(commit)); deleteErr != nil && !k8s_errors.IsNotFound(deleteErr) {
					return false, "", deleteErr
				}
				return false, "", nil
			}
			return false, "", err
		}
		if current.UID != pvc.UID {
			if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
				return false, "", err
			}
			return false, "", nil
		}
		pending, _, blocked, err := r.resumeCommittedPVCDeletion(ctx, current, commit, state, Log)
		return pending, blocked, err
	}

	current := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
		if k8s_errors.IsNotFound(err) {
			if deleteErr := r.discardPreparedDeletionCommit(ctx, commit); deleteErr != nil {
				return false, "", deleteErr
			}
		}
		return false, "", err
	}
	if string(current.UID) != state.PVCUID {
		if err := r.discardPreparedDeletionCommit(ctx, commit); err != nil {
			return false, "", err
		}
		return false, "", fmt.Errorf("PVC %s changed identity before committed cleanup", pvc.Name)
	}
	if !preparedCommitMatchesPVC(current, state) {
		if current.ResourceVersion != state.PVCResourceVersion || !pvcDeletionConsentMatches(current, state.RemediatorUID, state.Node, state.RequestID) {
			if err := r.discardPreparedDeletionCommit(ctx, commit); err != nil {
				return false, "", err
			}
			return false, "", fmt.Errorf("PVC %s changed after deletion consent was validated", pvc.Name)
		}

		old := pvc.DeepCopy()
		marked := pvc.DeepCopy()
		if marked.Annotations == nil {
			marked.Annotations = map[string]string{}
		}
		marked.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = committedPVCDeletionValue(state.PVCUID, state.RemediatorUID, state.Node)
		marked.Annotations[deletionCommitTokenAnnotation] = state.Token
		// This marker is provisional. A second resource-version-locked PVC write
		// below is the consent boundary; revocation between the two must win.
		if err := r.Patch(ctx, marked, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil {
			latest := &corev1.PersistentVolumeClaim{}
			getErr := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), latest)
			if getErr != nil || !preparedCommitMatchesPVC(latest, state) {
				if !k8s_errors.IsNotFound(getErr) && getErr != nil {
					return false, "", fmt.Errorf("conditionally mark PVC %s deletion and verify commit: %w (read current PVC: %w)", pvc.Name, err, getErr)
				}
				if discardErr := r.discardPreparedDeletionCommit(ctx, commit); discardErr != nil {
					return false, "", discardErr
				}
				return false, "", fmt.Errorf("conditionally mark PVC %s deletion: %w", pvc.Name, err)
			}
			current = latest
		} else {
			current = &corev1.PersistentVolumeClaim{}
			if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
				return false, "", fmt.Errorf("read committed PVC %s: %w", pvc.Name, err)
			}
			if !preparedCommitMatchesPVC(current, state) {
				if err := r.discardPreparedDeletionCommit(ctx, commit); err != nil {
					return false, "", err
				}
				return false, "", fmt.Errorf("PVC %s lost its provisional deletion marker before commit", pvc.Name)
			}
		}
	}

	_, err = r.finalizePreparedDeletionCommit(ctx, instance, current, commit, state)
	if err != nil {
		if errors.Is(err, errPreparedCommitRejected) {
			return false, "", nil
		}
		return false, "", err
	}
	// Promotion records the completed PVC decision. A crash here leaves the
	// prepared ConfigMap and finalized token for the resume path to finish.
	commit, state, err = r.promotePreparedDeletionCommit(ctx, commit, state)
	if err != nil {
		return false, "", err
	}
	current = &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
		if k8s_errors.IsNotFound(err) {
			if deleteErr := r.Delete(ctx, commit, observedDeleteOptions(commit)); deleteErr != nil && !k8s_errors.IsNotFound(deleteErr) {
				return false, "", deleteErr
			}
			return false, "", nil
		}
		return false, "", err
	}
	pending, _, blocked, err := r.resumeCommittedPVCDeletion(ctx, current, commit, state, Log)
	return pending, blocked, err
}

func (r *PodRemediatorReconciler) discardPreparedDeletionCommit(ctx context.Context, commit *corev1.ConfigMap) error {
	if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
		return fmt.Errorf("remove rejected provisional PVC deletion commit %s: %w", commit.Name, err)
	}
	return nil
}

func (r *PodRemediatorReconciler) rejectPreparedDeletionCommit(ctx context.Context, pvc *corev1.PersistentVolumeClaim, commit *corev1.ConfigMap, state deletionCommit) error {
	if err := r.discardPreparedDeletionCommit(ctx, commit); err != nil {
		return err
	}
	if pvc.Annotations[deletionCommitTokenAnnotation] == state.Token {
		return r.clearAbortedPVC(ctx, pvc)
	}
	return nil
}

// The finalized token is the irrevocable boundary. The patch compares the PVC
// resource version read with consent; a withdrawal before it causes a conflict.
func (r *PodRemediatorReconciler) finalizePreparedDeletionCommit(ctx context.Context, instance *remediationv1.PodRemediator, pvc *corev1.PersistentVolumeClaim, commit *corev1.ConfigMap, state deletionCommit) (*corev1.PersistentVolumeClaim, error) {
	if finalizedCommitMatchesPVC(pvc, state) {
		return pvc, nil
	}
	if !preparedCommitMatchesPVC(pvc, state) || !pvcDeletionConsentMatches(pvc, state.RemediatorUID, state.Node, state.RequestID) {
		if err := r.rejectPreparedDeletionCommit(ctx, pvc, commit, state); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("PVC %s lost deletion consent before commitment: %w", pvc.Name, errPreparedCommitRejected)
	}
	// PVC optimistic locking catches consent updates, but Node/SNR changes do
	// not change the PVC's resource version. Recheck those before committing.
	fenced, err := r.deletionFencingCurrent(ctx, instance, pvc, state.Node)
	if err != nil {
		return nil, err
	}
	if !fenced {
		if err := r.rejectPreparedDeletionCommit(ctx, pvc, commit, state); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("PVC %s lost current fencing before commitment: %w", pvc.Name, errPreparedCommitRejected)
	}
	old := pvc.DeepCopy()
	finalized := pvc.DeepCopy()
	finalized.Annotations[deletionCommitFinalizedAnnotation] = state.Token
	if err := r.Patch(ctx, finalized, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil {
		latest := &corev1.PersistentVolumeClaim{}
		if getErr := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), latest); getErr != nil {
			return nil, fmt.Errorf("finalize PVC %s deletion and read current PVC: %w (read: %w)", pvc.Name, err, getErr)
		}
		if finalizedCommitMatchesPVC(latest, state) {
			return latest, nil
		}
		if latest.ResourceVersion != pvc.ResourceVersion || !pvcDeletionConsentMatches(latest, state.RemediatorUID, state.Node, state.RequestID) {
			if rejectErr := r.rejectPreparedDeletionCommit(ctx, latest, commit, state); rejectErr != nil {
				return nil, rejectErr
			}
			return nil, fmt.Errorf("PVC %s changed before commitment: %w", pvc.Name, errPreparedCommitRejected)
		}
		return nil, fmt.Errorf("conditionally finalize PVC %s deletion: %w", pvc.Name, err)
	}
	current := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
		return nil, fmt.Errorf("read finalized PVC %s: %w", pvc.Name, err)
	}
	if !finalizedCommitMatchesPVC(current, state) {
		return nil, fmt.Errorf("PVC %s lost its finalized deletion token", pvc.Name)
	}
	return current, nil
}

func (r *PodRemediatorReconciler) promotePreparedDeletionCommit(ctx context.Context, commit *corev1.ConfigMap, state deletionCommit) (*corev1.ConfigMap, deletionCommit, error) {
	if committedDeletion(state) {
		return commit, state, nil
	}
	state.Phase = deletionCommitPhaseCommitted
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, state, err
	}
	promoted := commit.DeepCopy()
	if promoted.Annotations == nil {
		promoted.Annotations = map[string]string{}
	}
	promoted.Annotations[deletionCommitAnnotation] = string(encoded)
	if err := r.Update(ctx, promoted); err != nil {
		latest := &corev1.ConfigMap{}
		if getErr := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(commit), latest); getErr == nil {
			latestState, decodeErr := decodeDeletionCommit(latest)
			if decodeErr == nil && committedDeletion(latestState) && latestState.Token == state.Token {
				return latest, latestState, nil
			}
		}
		return nil, state, fmt.Errorf("promote PVC %s deletion commit: %w", state.PVCName, err)
	}
	return promoted, state, nil
}
