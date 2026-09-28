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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A ConfigMap persists the selected Pod identities using the operator's existing
// permissions. PVC writers must not have write access to these ConfigMaps.
// A prepared ConfigMap alone is not authority. The final optimistic-lock PVC
// transition commits deletion while consent is still present. A prepared
// ConfigMap needs the matching finalized token before promotion; afterward the
// committed ConfigMap persists authority across later consent removal.
// The name is deterministic for retries; its hash is not an authorization token.
const deletionCommitPrefix = "podremediator-deletion-"
const deletionCommitAnnotation = "remediation.openstack.org/deletion-commit-state"
const deletionCommitTokenAnnotation = "remediation.openstack.org/pvc-deletion-commit-token"
const deletionCommitFinalizedAnnotation = "remediation.openstack.org/pvc-deletion-commit-finalized"

var errPreparedCommitRejected = errors.New("prepared PVC deletion commit rejected")

const (
	deletionCommitPhasePrepared  = "prepared"
	deletionCommitPhaseCommitted = "committed"
)

type committedPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type deletionCommit struct {
	PVCName            string         `json:"pvcName"`
	PVCUID             string         `json:"pvcUID"`
	PVCResourceVersion string         `json:"pvcResourceVersion,omitempty"`
	RequestID          string         `json:"requestID,omitempty"`
	RemediatorUID      string         `json:"remediatorUID"`
	Node               string         `json:"node"`
	Token              string         `json:"token,omitempty"`
	Phase              string         `json:"phase,omitempty"`
	Pods               []committedPod `json:"pods"`
}

type committedPVCDeletionResumeResult struct {
	Pending                 bool
	BlockedByReplacementPod string
	AbortedPVCUIDs          map[string]struct{}
}

func deletionCommitName(pvcUID string) string {
	sum := sha256.Sum256([]byte(pvcUID))
	return deletionCommitPrefix + hex.EncodeToString(sum[:16])
}

func committedPVCDeletionValue(pvcUID, remediatorUID, nodeName string) string {
	return fmt.Sprintf("v1|%s|%s|%s", nodeName, pvcUID, remediatorUID)
}

func newDeletionCommitToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("generate PVC deletion commit token: %w", err)
	}
	return hex.EncodeToString(token), nil
}

func committedDeletion(state deletionCommit) bool {
	// An empty phase is the original committed format, retained so already
	// persisted commits continue to resume after an operator upgrade.
	return state.Phase == "" || state.Phase == deletionCommitPhaseCommitted
}

func preparedCommitMatchesPVC(pvc *corev1.PersistentVolumeClaim, state deletionCommit) bool {
	return string(pvc.UID) == state.PVCUID &&
		pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] == committedPVCDeletionValue(state.PVCUID, state.RemediatorUID, state.Node) &&
		pvc.Annotations[deletionCommitTokenAnnotation] == state.Token
}

func finalizedCommitMatchesPVC(pvc *corev1.PersistentVolumeClaim, state deletionCommit) bool {
	return preparedCommitMatchesPVC(pvc, state) && pvc.Annotations[deletionCommitFinalizedAnnotation] == state.Token
}

func pvcDeletionConsentMatches(pvc *corev1.PersistentVolumeClaim, remediatorUID, node, requestID string) bool {
	annotations := pvc.GetAnnotations()
	return remediationv1.HasRemediationConsent(annotations) &&
		annotations[remediationv1.PVCStuckOnNodeAnnotation] == node &&
		annotations[remediationv1.RemediatorUIDAnnotation] == remediatorUID &&
		annotations[remediationv1.FencingNodeUIDAnnotation] != "" &&
		annotations[remediationv1.RequestIDAnnotation] == requestID
}

func podUsesPVC(pod *corev1.Pod, claim string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}

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

	_, err = r.finalizePreparedDeletionCommit(ctx, current, commit, state)
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
func (r *PodRemediatorReconciler) finalizePreparedDeletionCommit(ctx context.Context, pvc *corev1.PersistentVolumeClaim, commit *corev1.ConfigMap, state deletionCommit) (*corev1.PersistentVolumeClaim, error) {
	if finalizedCommitMatchesPVC(pvc, state) {
		return pvc, nil
	}
	if !preparedCommitMatchesPVC(pvc, state) || !pvcDeletionConsentMatches(pvc, state.RemediatorUID, state.Node, state.RequestID) {
		if err := r.rejectPreparedDeletionCommit(ctx, pvc, commit, state); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("PVC %s lost deletion consent before commitment: %w", pvc.Name, errPreparedCommitRejected)
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

func decodeDeletionCommit(commit *corev1.ConfigMap) (deletionCommit, error) {
	var state deletionCommit
	if json.Unmarshal([]byte(commit.Annotations[deletionCommitAnnotation]), &state) != nil ||
		state.PVCName == "" || state.PVCUID == "" || state.RemediatorUID == "" || state.Node == "" ||
		commit.Name != deletionCommitName(state.PVCUID) {
		return state, fmt.Errorf("invalid PVC deletion commit ConfigMap %s", commit.Name)
	}
	if state.Phase != "" && state.Phase != deletionCommitPhasePrepared && state.Phase != deletionCommitPhaseCommitted {
		return state, fmt.Errorf("invalid PVC deletion commit phase in ConfigMap %s", commit.Name)
	}
	if state.Phase == deletionCommitPhasePrepared || state.Phase == deletionCommitPhaseCommitted {
		if state.PVCResourceVersion == "" || state.RequestID == "" || state.Token == "" {
			return state, fmt.Errorf("incomplete PVC deletion commit state in ConfigMap %s", commit.Name)
		}
	}
	for _, pod := range state.Pods {
		if pod.Name == "" || pod.UID == "" {
			return state, fmt.Errorf("invalid Pod identity in deletion commit ConfigMap %s", commit.Name)
		}
	}
	return state, nil
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
// PVC token. Committed ConfigMaps then authorize cleanup independent of SNR,
// node health, or CR enabled state. Scrub hints without a matching ConfigMap.
func (r *PodRemediatorReconciler) resumeCommittedPVCDeletions(ctx context.Context, namespaces []string, ownerUID string, Log logr.Logger) (committedPVCDeletionResumeResult, error) {
	result := committedPVCDeletionResumeResult{AbortedPVCUIDs: make(map[string]struct{})}
	for _, namespace := range namespaces {
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
				return committedPVCDeletionResumeResult{}, err
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
					result.AbortedPVCUIDs[state.PVCUID] = struct{}{}
					continue
				}
				// Consent may now be absent: the finalized PVC write already
				// committed the selected Pod identities.
				commit, state, err = r.promotePreparedDeletionCommit(ctx, commit, state)
				if err != nil {
					return committedPVCDeletionResumeResult{}, err
				}
			}
			active[state.PVCUID] = true
			stillPending, aborted, blocked, err := r.resumeCommittedPVCDeletion(ctx, pvc, commit, state, Log)
			if err != nil {
				return committedPVCDeletionResumeResult{}, err
			}
			result.Pending = result.Pending || stillPending
			if blocked != "" {
				result.BlockedByReplacementPod = blocked
			}
			if aborted {
				result.AbortedPVCUIDs[string(pvc.UID)] = struct{}{}
			}
		}
		pvcs := &corev1.PersistentVolumeClaimList{}
		if err := r.safetyReader().List(ctx, pvcs, client.InNamespace(namespace)); err != nil {
			return committedPVCDeletionResumeResult{}, err
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
