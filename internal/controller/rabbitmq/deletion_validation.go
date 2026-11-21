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
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *Reconciler) safetyReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// rabbitMQClaimOrdinal accepts only this StatefulSet's canonical data claims.
func rabbitMQClaimOrdinal(clusterName, claimName string) (int, bool) {
	suffix, ok := strings.CutPrefix(claimName, "persistence-"+clusterName+"-server-")
	if !ok {
		return 0, false
	}
	ordinal, err := strconv.Atoi(suffix)
	return ordinal, err == nil && ordinal >= 0 && strconv.Itoa(ordinal) == suffix
}

// ValidatePVCDeletion refreshes broker and queue safety immediately before
// PodRemediator writes its finalized token. It uses uncached Kubernetes reads,
// and never grants consent itself. Consent changes during these checks conflict
// with PodRemediator's optimistic-lock write. Failures after the finalized token
// cannot roll back a committed deletion; there is no atomic transaction across
// RabbitMQ membership and the Kubernetes API.
func (r *Reconciler) ValidatePVCDeletion(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	// Other applications keep their own request-scoped consent policy.
	if !IsRabbitmqPVC(pvc) {
		return nil
	}
	clusterName := pvc.Labels["app.kubernetes.io/name"]
	ordinal, ok := rabbitMQClaimOrdinal(clusterName, pvc.Name)
	if clusterName == "" || !ok || !remediationv1.HasRemediationConsent(pvc.Annotations) {
		return fmt.Errorf("PVC %s lacks RabbitMQ claim identity or current consent", pvc.Name)
	}
	reader := r.safetyReader()
	instance := &rabbitmqv1.RabbitMq{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: pvc.Namespace, Name: clusterName}, instance); err != nil {
		return fmt.Errorf("read RabbitMq for deletion validation: %w", err)
	}
	if !instance.DeletionTimestamp.IsZero() || instance.Labels[pauseReconciliationLabel] == "true" ||
		instance.Spec.Replicas == nil || !canRetainQuorumAfterLosingOneReplica(*instance.Spec.Replicas) {
		return fmt.Errorf("RabbitMq %s cannot authorize PVC deletion in its current state", clusterName)
	}
	recovery, exists := instance.Status.PVCRemediation[pvc.Name]
	if !exists || recovery.PVCUID != string(pvc.UID) || recovery.RequestID != pvc.Annotations[remediationv1.RequestIDAnnotation] ||
		recovery.ConsentState != rabbitmqv1.PVCRemediationConsentGranted {
		return fmt.Errorf("PVC %s has no matching RabbitMQ recovery record", pvc.Name)
	}
	claims := &corev1.PersistentVolumeClaimList{}
	if err := reader.List(ctx, claims, client.InNamespace(pvc.Namespace), client.MatchingLabels{"app.kubernetes.io/name": clusterName}); err != nil {
		return err
	}
	stuckNodes := map[string]bool{}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if _, ok := rabbitMQClaimOrdinal(clusterName, claim.Name); ok {
			if node := claim.Annotations[remediationv1.PVCStuckOnNodeAnnotation]; node != "" {
				stuckNodes[node] = true
			}
		}
	}
	// Include the authoritative commit snapshot even if a concurrent label
	// update made the candidate disappear from the selector-based list.
	stuckNodes[pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]] = true
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(pvc.Namespace), client.MatchingLabels(rabbitmq.SelectorLabels(clusterName))); err != nil {
		return err
	}
	candidatePod := fmt.Sprintf("%s-server-%d", clusterName, ordinal)
	survivors := rmqReadyPods(pods.Items, stuckNodes, map[string]bool{candidatePod: true})
	quorum := quorumOf(*instance.Spec.Replicas)
	if len(survivors) < quorum {
		return fmt.Errorf("RabbitMq has %d ready survivors below quorum %d", len(survivors), quorum)
	}
	creds, err := r.rmqLoadMgmtCreds(ctx, instance)
	if err != nil {
		return err
	}
	anchor := &survivors[0]
	nodes, err := r.rmqClusterNodes(ctx, instance, anchor, creds)
	if err != nil {
		return err
	}
	running, err := rmqRunningSurvivorCount(instance, survivors, nodes)
	if err != nil {
		return err
	}
	if running < quorum {
		return fmt.Errorf("RabbitMq has %d running survivors below quorum %d", running, quorum)
	}
	runningNodes := map[string]bool{}
	for _, n := range nodes {
		if n.Running {
			runningNodes[n.Name] = true
		}
	}
	safeNodes := map[string]bool{}
	for _, pod := range survivors {
		name := "rabbit@" + rabbitmq.ManagementPodServerName(pod.Name, instance.Name, instance.Namespace)
		if runningNodes[name] {
			safeNodes[name] = true
		}
	}
	candidateNode := "rabbit@" + rabbitmq.ManagementPodServerName(candidatePod, instance.Name, instance.Namespace)
	return r.rmqQueuesSafeAfterDeletion(ctx, instance, anchor, creds, candidateNode, safeNodes)
}
