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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
)

// PVC deletion is a crash-safe transaction that starts after PodRemediator
// accepts matching remediation consent. A prepared deletionCommit in a
// controller-owned ConfigMap records the PVC identity, owner/node/request
// context, and exact Pods selected for cleanup. The prepared record alone
// grants no authority: an optimistic-lock PVC write stores the finalized token
// only after consent and live fencing are rechecked. That token is the
// transaction's irreversible boundary; committed state lets reconciliation
// resume Pod/PVC cleanup after restart despite later consent or fencing changes.
// PVC writers must not have write access to these ConfigMaps. The ConfigMap
// name is deterministic for retries; its hash is not an authorization token.
const deletionCommitPrefix = "podremediator-deletion-"
const deletionCommitAnnotation = "remediation.openstack.org/deletion-commit-state"
const deletionCommitTokenAnnotation = "remediation.openstack.org/pvc-deletion-commit-token"
const deletionCommitFinalizedAnnotation = "remediation.openstack.org/pvc-deletion-commit-finalized"

var errPreparedCommitRejected = errors.New("prepared PVC deletion commit rejected")
var errPVCRemediationPaused = errors.New("PVC remediation is paused")

const (
	deletionCommitPhasePrepared  = "prepared"
	deletionCommitPhaseCommitted = "committed"
)

// committedPod identifies a Pod selected for cleanup by name and UID. Checking
// both prevents a replacement Pod with the same name from being treated as the
// Pod whose cleanup was authorized.
type committedPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// deletionCommit is the durable state for one PVC deletion transaction. It
// records the PVC and request identity, the PodRemediator owner and node, the
// commit token and phase, and the exact Pod identities selected for cleanup.
// Prepared state is provisional until the matching finalized token is written
// to the PVC; committed state lets reconciliation resume cleanup after restart.
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
	BlockedByInvalidCommit  string
	PendingPVCUIDs          map[string]struct{}
	BlockedPVCUIDs          map[string]struct{}
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
	return state.Phase == deletionCommitPhaseCommitted
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

func decodeDeletionCommit(commit *corev1.ConfigMap) (deletionCommit, error) {
	var state deletionCommit
	if json.Unmarshal([]byte(commit.Annotations[deletionCommitAnnotation]), &state) != nil ||
		state.PVCName == "" || state.PVCUID == "" || state.RemediatorUID == "" || state.Node == "" ||
		commit.Name != deletionCommitName(state.PVCUID) {
		return state, fmt.Errorf("invalid PVC deletion commit ConfigMap %s", commit.Name)
	}
	if state.Phase == "" {
		return state, fmt.Errorf("legacy PVC deletion commit ConfigMap %s has no finalized-token phase", commit.Name)
	}
	if state.Phase != deletionCommitPhasePrepared && state.Phase != deletionCommitPhaseCommitted {
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
