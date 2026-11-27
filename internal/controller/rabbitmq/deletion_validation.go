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

package rabbitmq

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// safetyReader returns a reader that bypasses the controller's cache, for the
// checks that must not act on a stale view. It falls back to the cached client
// only when no APIReader was wired up, which is the case in some tests.
func (r *Reconciler) safetyReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// rabbitMQClaimOrdinal parses the pod ordinal out of a claim name, and reports
// false for any name that is not one of this StatefulSet's own data claims
// (persistence-<cluster>-server-<n>).
func rabbitMQClaimOrdinal(clusterName, claimName string) (int, bool) {
	suffix, ok := strings.CutPrefix(claimName, "persistence-"+clusterName+"-server-")
	if !ok {
		return 0, false
	}
	ordinal, err := strconv.Atoi(suffix)
	return ordinal, err == nil && ordinal >= 0 && strconv.Itoa(ordinal) == suffix
}

// rabbitMQClaimNameIdentity derives the cluster and ordinal from the claim name
// without trusting the editable app label. The caller still requires a matching
// recovery record before treating the claim as RabbitMQ-owned.
func rabbitMQClaimNameIdentity(claimName string) (string, int, bool) {
	rest, ok := strings.CutPrefix(claimName, "persistence-")
	if !ok {
		return "", 0, false
	}
	separator := strings.LastIndex(rest, "-server-")
	if separator <= 0 {
		return "", 0, false
	}
	clusterName := rest[:separator]
	ordinal, ok := rabbitMQClaimOrdinal(clusterName, claimName)
	return clusterName, ordinal, ok
}

// rmqDataClaims lists this cluster's own data PVCs using the given reader.
//
// Claims whose names do not follow the StatefulSet's volumeClaimTemplate naming
// are dropped. The name, rather than the editable app label, is the identity:
// a label removal must not hide a RabbitMQ claim from these safety checks.
func (r *Reconciler) rmqDataClaims(
	ctx context.Context,
	instance *rabbitmqv1.RabbitMq,
	reader client.Reader,
) ([]corev1.PersistentVolumeClaim, error) {
	claimList := &corev1.PersistentVolumeClaimList{}
	if err := reader.List(ctx, claimList, client.InNamespace(instance.Namespace)); err != nil {
		return nil, fmt.Errorf("listing RabbitMq data claims: %w", err)
	}
	claims := make([]corev1.PersistentVolumeClaim, 0, len(claimList.Items))
	for _, claim := range claimList.Items {
		if _, ok := rabbitMQClaimOrdinal(instance.Name, claim.Name); ok {
			claims = append(claims, claim)
		}
	}
	return claims, nil
}

// rmqStuckNodes returns the Kubernetes nodes PodRemediator has marked as stuck
// for any of this cluster's claims. Pods on those nodes never count as healthy
// survivors.
func rmqStuckNodes(claims []corev1.PersistentVolumeClaim) map[string]bool {
	stuck := make(map[string]bool)
	for _, claim := range claims {
		if node := claim.Annotations[remediationv1.PVCStuckOnNodeAnnotation]; node != "" {
			stuck[node] = true
		}
	}
	return stuck
}

// ValidatePVCDeletion re-evaluates broker and queue safety immediately before
// PodRemediator writes its finalized token, which is the point of no return
// for the deletion.
//
// Consent may have been granted much earlier, so this repeats the checks
// against current state using uncached reads. It only ever vetoes: it never
// grants consent itself, and returning an error rejects the deletion.
//
// Two properties bound what it can guarantee. A consent change racing these
// checks is caught by PodRemediator's optimistic-lock write, which conflicts.
// But a broker failing after the last check cannot be caught at all, and once
// the finalized token is written the deletion cannot be rolled back: there is
// no atomic transaction spanning RabbitMQ membership and the Kubernetes API.
func (r *Reconciler) ValidatePVCDeletion(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	clusterName, ordinal, ok := rabbitMQClaimNameIdentity(pvc.Name)
	if !ok {
		// Other applications keep their own request-scoped consent policy.
		return nil
	}
	reader := r.safetyReader()
	instance := &rabbitmqv1.RabbitMq{}
	instanceErr := reader.Get(ctx, client.ObjectKey{Namespace: pvc.Namespace, Name: clusterName}, instance)
	if instanceErr != nil && !apierrors.IsNotFound(instanceErr) {
		return fmt.Errorf("read RabbitMq for deletion validation: %w", instanceErr)
	}
	if instanceErr != nil {
		// With no live RabbitMq there is no broker or queue safety view for this
		// validator to apply. Treat the name match as advisory and let the
		// generic PodRemediator checks handle the claim.
		return nil
	}
	recovery, exists := instance.Status.PVCRemediation[pvc.Name]
	if !exists {
		cached := &rabbitmqv1.RabbitMq{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(instance), cached); err != nil &&
			!apierrors.IsNotFound(err) {
			return fmt.Errorf("read cached RabbitMq recovery state: %w", err)
		} else if err == nil {
			if _, withdrawn := cached.Status.PVCRemediation[pvc.Name]; withdrawn {
				return fmt.Errorf("PVC %s RabbitMQ recovery record was withdrawn from the API", pvc.Name)
			}
		}
		// The status record is RabbitMQ-specific ownership evidence that does
		// not depend on an editable label. Without it, keep generic claims on
		// the generic PodRemediator validation path.
		return nil
	}
	if !instance.DeletionTimestamp.IsZero() || instance.Labels[pauseReconciliationLabel] == "true" ||
		instance.Spec.Replicas == nil || !canRetainQuorumAfterLosingOneReplica(*instance.Spec.Replicas) {
		return fmt.Errorf("RabbitMq %s cannot authorize PVC deletion in its current state", clusterName)
	}
	if recovery.PVCUID != string(pvc.UID) ||
		recovery.RequestID != pvc.Annotations[remediationv1.RequestIDAnnotation] ||
		!recovery.Consented() || !remediationv1.HasRemediationConsent(pvc.Annotations) {
		return fmt.Errorf("PVC %s has no matching RabbitMQ recovery record and consent", pvc.Name)
	}
	claims, err := r.rmqDataClaims(ctx, instance, reader)
	if err != nil {
		return err
	}
	stuckNodes := rmqStuckNodes(claims)
	// Include the authoritative commit snapshot even if a concurrent label
	// update made the candidate disappear from the selector-based list.
	stuckNodes[pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]] = true

	pods, err := r.rmqListPods(ctx, instance, reader)
	if err != nil {
		return err
	}
	candidatePod := rmqPodName(instance, ordinal)
	survivors := rmqReadyPods(pods, stuckNodes, map[string]bool{candidatePod: true})
	if quorum := quorumOf(*instance.Spec.Replicas); len(survivors) < quorum {
		return fmt.Errorf("RabbitMq has %d ready survivors below quorum %d", len(survivors), quorum)
	}
	view, err := r.rmqClusterView(ctx, instance, survivors)
	if err != nil {
		return err
	}
	return r.verifyCandidateRemovable(ctx, instance, view, candidatePod)
}
