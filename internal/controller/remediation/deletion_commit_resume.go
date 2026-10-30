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
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This file contains recovery and cleanup for persisted PVC deletion commits.
func replacementPodBlockingMessage(pvc *corev1.PersistentVolumeClaim, pod *corev1.Pod) string {
	return fmt.Sprintf("PVC %s/%s is terminating while replacement Pod %s/%s (UID %s) uses it; workload operator must pause recreation and release that Pod to finish committed cleanup", pvc.Namespace, pvc.Name, pod.Namespace, pod.Name, pod.UID)
}

func (r *PodRemediatorReconciler) podsUsingPVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) ([]corev1.Pod, error) {
	list := &corev1.PodList{}
	if err := r.safetyReader().List(ctx, list, client.InNamespace(pvc.Namespace)); err != nil {
		return nil, fmt.Errorf("list pods for PVC %s: %w", pvc.Name, err)
	}
	result := make([]corev1.Pod, 0)
	for i := range list.Items {
		if podUsesPVC(&list.Items[i], pvc.Name) {
			result = append(result, list.Items[i])
		}
	}
	return result, nil
}

// A stale or forged PVC hint cannot authorize cleanup. Removing the handshake
// also prevents its old consent from being used later in the same reconcile.
func (r *PodRemediatorReconciler) clearAbortedPVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	if pvc.DeletionTimestamp != nil {
		return nil // API deletion cannot be undone; never remove its commit here.
	}
	old := pvc.DeepCopy()
	clearRemediationAnnotations(pvc)
	delete(pvc.Annotations, remediationv1.PVCDeletionCommittedAnnotation)
	delete(pvc.Annotations, deletionCommitTokenAnnotation)
	delete(pvc.Annotations, deletionCommitFinalizedAnnotation)
	if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil && !k8s_errors.IsNotFound(err) {
		return fmt.Errorf("clear aborted PVC %s remediation: %w", pvc.Name, err)
	}
	return nil
}

// resumeCommittedPVCDeletions promotes only prepared records with a finalized
// PVC token. Committed ConfigMaps authorize cleanup only while that matching
// token remains on the PVC, independent of SNR, node health, or CR enabled
// state. Scrub hints without a matching ConfigMap.
func (r *PodRemediatorReconciler) resumeCommittedPVCDeletions(ctx context.Context, namespaces []string, ownerUID string, Log logr.Logger) (committedPVCDeletionResumeResult, error) {
	result := committedPVCDeletionResumeResult{
		PendingPVCUIDs: make(map[string]struct{}),
		BlockedPVCUIDs: make(map[string]struct{}),
		AbortedPVCUIDs: make(map[string]struct{}),
	}
	for _, namespace := range namespaces {
		pvcs := &corev1.PersistentVolumeClaimList{}
		if err := r.safetyReader().List(ctx, pvcs, client.InNamespace(namespace)); err != nil {
			return committedPVCDeletionResumeResult{}, err
		}
		pvcByCommitName := make(map[string]*corev1.PersistentVolumeClaim, len(pvcs.Items))
		for i := range pvcs.Items {
			pvc := &pvcs.Items[i]
			pvcByCommitName[deletionCommitName(string(pvc.UID))] = pvc
		}

		commits := &corev1.ConfigMapList{}
		if err := r.safetyReader().List(ctx, commits, client.InNamespace(namespace)); err != nil {
			return committedPVCDeletionResumeResult{}, fmt.Errorf("list PVC deletion commits: %w", err)
		}
		active := map[string]bool{}
		for i := range commits.Items {
			commit := &commits.Items[i]
			if !strings.HasPrefix(commit.Name, deletionCommitPrefix) {
				continue
			}
			state, err := decodeDeletionCommit(commit)
			if err != nil {
				pvc := pvcByCommitName[commit.Name]
				if pvc == nil {
					Log.Info("Ignoring malformed PVC deletion commit with no matching PVC", "configMap", commit.Name)
					continue
				}
				pvcUID := string(pvc.UID)
				if pvcOwner := pvc.Annotations[remediationv1.RemediatorUIDAnnotation]; pvcOwner != "" && pvcOwner != ownerUID {
					active[pvcUID] = true
					continue
				}
				Log.Error(err, "Invalid PVC deletion commit ConfigMap", "configMap", commit.Name, "pvc", pvc.Name)
				if !pvc.DeletionTimestamp.IsZero() || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
					active[pvcUID] = true
					result.BlockedPVCUIDs[pvcUID] = struct{}{}
					result.BlockedByInvalidCommit = fmt.Sprintf("PVC %s/%s has an invalid deletion commit record; preserve the PVC and repair or remove the record before cleanup can continue", pvc.Namespace, pvc.Name)
					continue
				}
				if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
					return committedPVCDeletionResumeResult{}, fmt.Errorf("remove invalid PVC deletion commit %s: %w", commit.Name, err)
				}
				if err := r.clearAbortedPVC(ctx, pvc); err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
				active[pvcUID] = true
				result.AbortedPVCUIDs[pvcUID] = struct{}{}
				continue
			}
			if state.RemediatorUID != ownerUID {
				active[state.PVCUID] = true
				continue
			}
			pvc := &corev1.PersistentVolumeClaim{}
			if err := r.safetyReader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: state.PVCName}, pvc); err != nil {
				if !k8s_errors.IsNotFound(err) {
					return committedPVCDeletionResumeResult{}, err
				}
				if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
					return committedPVCDeletionResumeResult{}, err
				}
				continue
			}
			if string(pvc.UID) != state.PVCUID {
				if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
					return committedPVCDeletionResumeResult{}, err
				}
				continue
			}
			if state.Phase == deletionCommitPhasePrepared {
				if !finalizedCommitMatchesPVC(pvc, state) {
					// The first PVC marker is provisional. After a restart,
					// revalidate fencing and consent in a fresh normal scan.
					if err := r.rejectPreparedDeletionCommit(ctx, pvc, commit, state); err != nil {
						return committedPVCDeletionResumeResult{}, err
					}
					if pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" ||
						pvc.Annotations[deletionCommitTokenAnnotation] != "" ||
						pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
						if err := r.clearAbortedPVC(ctx, pvc); err != nil {
							return committedPVCDeletionResumeResult{}, err
						}
					}
					active[state.PVCUID] = true
					result.AbortedPVCUIDs[state.PVCUID] = struct{}{}
					continue
				}
				// Consent may now be absent: the finalized PVC write already
				// committed the selected Pod identities.
				commit, state, err = r.promotePreparedDeletionCommit(ctx, commit, state)
				if err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
			} else if !finalizedCommitMatchesPVC(pvc, state) {
				// A committed record is authoritative only while its matching PVC
				// token proves the consent boundary was crossed. Consent may be
				// withdrawn afterward, but the commit token must remain intact.
				if !pvc.DeletionTimestamp.IsZero() || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
					active[state.PVCUID] = true
					result.BlockedPVCUIDs[state.PVCUID] = struct{}{}
					result.BlockedByInvalidCommit = fmt.Sprintf("PVC %s/%s has a committed deletion record without its matching finalized PVC token", pvc.Namespace, pvc.Name)
					continue
				}
				if err := r.discardPreparedDeletionCommit(ctx, commit); err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
				if err := r.clearAbortedPVC(ctx, pvc); err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
				active[state.PVCUID] = true
				result.AbortedPVCUIDs[state.PVCUID] = struct{}{}
				continue
			}
			active[state.PVCUID] = true
			stillPending, aborted, blocked, err := r.resumeCommittedPVCDeletion(ctx, pvc, commit, state, Log)
			if err != nil {
				return committedPVCDeletionResumeResult{}, err
			}
			result.Pending = result.Pending || stillPending
			if stillPending {
				result.PendingPVCUIDs[state.PVCUID] = struct{}{}
			}
			if blocked != "" {
				result.BlockedByReplacementPod = blocked
			}
			if aborted {
				result.AbortedPVCUIDs[string(pvc.UID)] = struct{}{}
			}
		}
		for i := range pvcs.Items {
			pvc := &pvcs.Items[i]
			if (pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" ||
				pvc.Annotations[deletionCommitTokenAnnotation] != "" ||
				pvc.Annotations[deletionCommitFinalizedAnnotation] != "") && !active[string(pvc.UID)] {
				if err := r.clearAbortedPVC(ctx, pvc); err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
			}
		}
	}
	return result, nil
}

func (r *PodRemediatorReconciler) resumeCommittedPVCDeletion(ctx context.Context, pvc *corev1.PersistentVolumeClaim, commit *corev1.ConfigMap, state deletionCommit, Log logr.Logger) (bool, bool, string, error) {
	if !finalizedCommitMatchesPVC(pvc, state) {
		return false, false, "", fmt.Errorf("PVC %s has a committed deletion record without its matching finalized token", pvc.Name)
	}
	pods, err := r.podsUsingPVC(ctx, pvc)
	if err != nil {
		return false, false, "", err
	}
	authorized := map[string]string{}
	for _, pod := range state.Pods {
		authorized[pod.Name] = pod.UID
	}
	for i := range pods {
		pod := &pods[i]
		if (pod.Spec.NodeName != "" && pod.Spec.NodeName != state.Node) || authorized[pod.Name] != string(pod.UID) {
			if !pvc.DeletionTimestamp.IsZero() {
				// Deletion cannot be rolled back, and the replacement Pod was not
				// part of the consented set. Preserve the durable commit and CR
				// finalizer until the workload owner releases this Pod.
				blocked := replacementPodBlockingMessage(pvc, pod)
				Log.Info("Committed PVC deletion is waiting for the workload operator to release a replacement Pod", "pvc", pvc.Name, "pod", pod.Name, "podUID", pod.UID)
				return true, false, blocked, nil
			}
			Log.Info("Aborting committed PVC deletion because claim users changed", "pvc", pvc.Name, "pod", pod.Name)
			if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
				return false, false, "", err
			}
			return false, true, "", r.clearAbortedPVC(ctx, pvc)
		}
	}
	gracePeriod := int64(0)
	for i := range pods {
		pod := &pods[i]
		if !pod.DeletionTimestamp.IsZero() && pod.DeletionGracePeriodSeconds != nil && *pod.DeletionGracePeriodSeconds == 0 {
			// The API server records zero once a force deletion has already been
			// accepted. Keep waiting on Pod finalizers without issuing the request
			// again on every reconciliation.
			continue
		}
		options := observedDeleteOptions(pod)
		options.GracePeriodSeconds = &gracePeriod
		if err := r.Delete(ctx, pod, options); err != nil && !k8s_errors.IsNotFound(err) {
			return false, false, "", fmt.Errorf("conditionally force-delete committed Pod %s: %w", pod.Name, err)
		}
	}
	remaining, err := r.podsUsingPVC(ctx, pvc)
	if err != nil {
		return false, false, "", err
	}
	if len(remaining) != 0 {
		return true, false, "", nil
	}
	if pvc.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pvc, observedDeleteOptions(pvc)); err != nil && !k8s_errors.IsNotFound(err) {
			return false, false, "", fmt.Errorf("conditionally delete committed PVC %s: %w", pvc.Name, err)
		}
	}
	current := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pvc), current); err != nil {
		if !k8s_errors.IsNotFound(err) {
			return false, false, "", err
		}
		if err := r.Delete(ctx, commit, observedDeleteOptions(commit)); err != nil && !k8s_errors.IsNotFound(err) {
			return false, false, "", err
		}
		return false, false, "", nil
	}
	if current.UID == pvc.UID && !current.DeletionTimestamp.IsZero() {
		// A StatefulSet may recreate the ordinal between the empty Pod list
		// and the PVC delete request. Surface that race on this reconcile.
		users, err := r.podsUsingPVC(ctx, current)
		if err != nil {
			return false, false, "", err
		}
		for i := range users {
			pod := &users[i]
			if (pod.Spec.NodeName != "" && pod.Spec.NodeName != state.Node) || authorized[pod.Name] != string(pod.UID) {
				return true, false, replacementPodBlockingMessage(current, pod), nil
			}
		}
	}
	return true, false, "", nil
}
