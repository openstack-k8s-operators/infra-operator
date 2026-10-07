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

package rabbitmq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	"github.com/openstack-k8s-operators/lib-common/modules/common/rsh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const rabbitmqContainerName = "rabbitmq"

// execInPod is a seam over lib-common's rsh.ExecInPod so unit tests can inject a
// fake executor. It must not be reassigned outside of tests.
var execInPod = rsh.ExecInPod

// clusterStatus is the subset of `rabbitmqctl --formatter json cluster_status`
// output that we rely on.
type clusterStatus struct {
	RunningNodes []string `json:"running_nodes"`
}

// rejoinNodeName returns the RabbitMQ Erlang node name for a pod, e.g.
// rabbit@<name>-server-0.<name>-nodes.<namespace>.
func rejoinNodeName(instance *rabbitmqv1beta1.RabbitMq, podName string) string {
	return fmt.Sprintf("rabbit@%s.%s-nodes.%s", podName, instance.Name, instance.Namespace)
}

func rejoinEnabled(instance *rabbitmqv1beta1.RabbitMq) bool {
	if instance.Spec.Replicas == nil || *instance.Spec.Replicas <= 1 {
		return false
	}
	version := instance.Status.CurrentVersion
	if instance.Spec.TargetVersion != nil && *instance.Spec.TargetVersion != "" {
		version = *instance.Spec.TargetVersion
	}
	if version == "" {
		version = DefaultRabbitMQVersion
	}
	return rabbitmq.IsVersion4_1OrLater(version)
}

func podIsReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podRabbitMQContainerReady allows exec in a target whose custom readiness gate
// is deliberately false while its RabbitMQ container is otherwise running.
func podRabbitMQContainerReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == rabbitmqContainerName {
			return status.Ready
		}
	}
	return false
}

func podUsesPVC(pod *corev1.Pod, pvcName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvcName {
			return true
		}
	}
	return false
}

// rabbitmqctl runs `rabbitmqctl <args...>` in the pod's rabbitmq container and
// returns stdout. A non-zero exit code surfaces as a non-nil error.
func (r *Reconciler) rabbitmqctl(ctx context.Context, podName types.NamespacedName, args ...string) (string, error) {
	cmd := append([]string{"rabbitmqctl"}, args...)
	var stdout string
	err := execInPod(ctx, r.Kclient, r.config, podName, rabbitmqContainerName, cmd,
		func(out *bytes.Buffer, _ *bytes.Buffer) error {
			stdout = out.String()
			return nil
		})
	return stdout, err
}

func (r *Reconciler) runningNodes(ctx context.Context, pod *corev1.Pod) (map[string]bool, error) {
	out, err := r.rabbitmqctl(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
		"--formatter", "json", "cluster_status")
	if err != nil {
		return nil, err
	}
	var status clusterStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return nil, fmt.Errorf("parsing cluster_status from %s: %w", pod.Name, err)
	}
	set := make(map[string]bool, len(status.RunningNodes))
	for _, node := range status.RunningNodes {
		set[node] = true
	}
	return set, nil
}

// ReconcileNodeRejoin authorizes a rejoin only for a replacement PVC recorded
// in RabbitMq status with the same PodRemediator request ID that granted its
// deletion. It keeps that pod unready until both surviving and target nodes
// confirm cluster membership.
func (r *Reconciler) ReconcileNodeRejoin(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
) (requeue bool, err error) {
	if !rejoinEnabled(instance) {
		return false, nil
	}
	log := r.GetLogger(ctx)
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(rabbitmq.SelectorLabels(instance.Name)),
	); err != nil {
		return false, fmt.Errorf("listing RabbitMq pods for rejoin: %w", err)
	}
	podsByName := make(map[string]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		podsByName[pods.Items[i].Name] = &pods.Items[i]
	}

	// A granted consent remains associated with the old PVC UID after deletion.
	// A different UID is the proof that this is the replacement claim.
	pendingPods := make(map[string]string)
	keys := make([]string, 0, len(instance.Status.PVCRemediation))
	for pvcName := range instance.Status.PVCRemediation {
		keys = append(keys, pvcName)
	}
	sort.Strings(keys)
	for _, pvcName := range keys {
		remediation := instance.Status.PVCRemediation[pvcName]
		if !remediation.ConsentGranted {
			continue
		}
		ordinal, ok := rmqPVCOrdinal(pvcName)
		if !ok {
			continue
		}
		podName := fmt.Sprintf("%s-server-%d", instance.Name, ordinal)
		pvc := &corev1.PersistentVolumeClaim{}
		pvcErr := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: pvcName}, pvc)
		if pvcErr != nil && !apierrors.IsNotFound(pvcErr) {
			return true, fmt.Errorf("reading replacement PVC %s: %w", pvcName, pvcErr)
		}
		if pvcErr == nil && string(pvc.UID) == remediation.PVCUID && pvc.DeletionTimestamp == nil {
			continue
		}

		pendingPods[podName] = remediation.RequestID
		pod := podsByName[podName]
		if pod == nil {
			continue
		}
		if _, err := r.setRejoinReady(ctx, pod, false); err != nil {
			return true, err
		}
		if remediation.RequestID == "" {
			return true, fmt.Errorf("PVC %s recovery has no recorded PodRemediator request ID", pvcName)
		}
		if pvcErr != nil || pvc.DeletionTimestamp != nil || string(pvc.UID) == remediation.PVCUID {
			continue
		}
		if !podUsesPVC(pod, pvcName) {
			continue
		}
		if pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != remediation.RequestID {
			base := pod.DeepCopy()
			if pod.Annotations == nil {
				pod.Annotations = map[string]string{}
			}
			pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] = remediation.RequestID
			if err := r.Patch(ctx, pod, client.MergeFrom(base)); err != nil {
				return true, fmt.Errorf("authorizing rejoin on %s: %w", pod.Name, err)
			}
			log.Info("Authorized replacement pod rejoin from PVC consent", "pod", pod.Name, "pvc", pvcName)
			return true, nil
		}
	}

	// Ordinary pods pass the gate once the operator observes them. A replacement
	// waiting for recovery stays out of client-facing Services.
	changed := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		ready := true
		if _, pending := pendingPods[pod.Name]; pending {
			ready = false
		}
		updated, err := r.setRejoinReady(ctx, pod, ready)
		if err != nil {
			return true, err
		}
		changed = changed || updated
	}
	if changed {
		return true, nil
	}
	if len(pendingPods) == 0 {
		return false, nil
	}

	// Process one authorized replacement at a time.
	var target *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		requestID, pending := pendingPods[pod.Name]
		if !pending || requestID == "" || pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != requestID {
			continue
		}
		target = pod
		break
	}
	if target == nil {
		return true, nil
	}
	if !podRabbitMQContainerReady(target) {
		log.Info("Authorized rejoin is waiting for the RabbitMQ container", "pod", target.Name)
		return true, nil
	}

	replicas := *instance.Spec.Replicas
	quorum := int(replicas/2 + 1)
	targetNode := rejoinNodeName(instance, target.Name)
	var anchor *corev1.Pod
	var anchorRunning map[string]bool
	for i := range pods.Items {
		peer := &pods.Items[i]
		if peer.Name == target.Name || !podIsReady(peer) {
			continue
		}
		running, err := r.runningNodes(ctx, peer)
		if err != nil {
			log.Info("Could not read cluster status from candidate peer", "pod", peer.Name, "error", err.Error())
			continue
		}
		if len(running) >= quorum {
			anchor, anchorRunning = peer, running
			break
		}
	}
	if anchor == nil {
		log.Info("No ready RabbitMQ peer has quorum; deferring rejoin", "pod", target.Name, "quorum", quorum)
		return true, nil
	}

	anchorName := types.NamespacedName{Namespace: anchor.Namespace, Name: anchor.Name}
	targetName := types.NamespacedName{Namespace: target.Namespace, Name: target.Name}
	anchorNode := rejoinNodeName(instance, anchor.Name)
	targetRunning, err := r.runningNodes(ctx, target)
	if err != nil {
		return true, fmt.Errorf("reading target cluster status: %w", err)
	}
	if anchorRunning[targetNode] && len(targetRunning) >= quorum && targetRunning[anchorNode] {
		return true, r.finishNodeRejoin(ctx, target)
	}
	if len(targetRunning) > 1 {
		log.Info("Authorized replacement is not standalone; refusing reset", "pod", target.Name, "runningNodes", len(targetRunning))
		return true, nil
	}

	// Forget only when the surviving cluster actually lists the stale member.
	// A failure here is not ignored: joining without removing that member could
	// leave conflicting cluster metadata.
	if anchorRunning[targetNode] {
		if _, err := r.rabbitmqctl(ctx, anchorName, "forget_cluster_node", targetNode); err != nil {
			return true, fmt.Errorf("forgetting stale RabbitMQ node %s: %w", targetNode, err)
		}
	}
	if _, err := r.rabbitmqctl(ctx, targetName, "join_cluster", anchorNode); err != nil {
		return true, fmt.Errorf("joining RabbitMQ node %s to %s: %w", target.Name, anchorNode, err)
	}

	anchorRunning, err = r.runningNodes(ctx, anchor)
	if err != nil {
		return true, fmt.Errorf("verifying rejoin via anchor: %w", err)
	}
	targetRunning, err = r.runningNodes(ctx, target)
	if err != nil {
		return true, fmt.Errorf("verifying target cluster status after rejoin: %w", err)
	}
	if !anchorRunning[targetNode] || !targetRunning[anchorNode] || len(targetRunning) < quorum {
		log.Info("RabbitMQ membership is not yet consistent after join", "target", target.Name)
		return true, nil
	}
	log.Info("RabbitMQ replacement rejoined its cluster", "node", targetNode, "requestID", target.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster])
	return true, r.finishNodeRejoin(ctx, target)
}

func (r *Reconciler) finishNodeRejoin(ctx context.Context, pod *corev1.Pod) error {
	if _, err := r.setRejoinReady(ctx, pod, true); err != nil {
		return err
	}
	return r.clearRejoinAnnotation(ctx, pod)
}

func (r *Reconciler) setRejoinReady(ctx context.Context, pod *corev1.Pod, ready bool) (bool, error) {
	wanted := corev1.ConditionFalse
	reason := "RejoinPending"
	message := "Waiting for the RabbitMQ controller to verify cluster membership"
	if ready {
		wanted = corev1.ConditionTrue
		reason = "RejoinVerified"
		message = "RabbitMQ cluster membership is verified"
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodConditionType(rabbitmqv1beta1.RabbitMQRejoinReadyCondition) &&
			condition.Status == wanted && condition.Reason == reason {
			return false, nil
		}
	}
	base := pod.DeepCopy()
	newCondition := corev1.PodCondition{
		Type:               corev1.PodConditionType(rabbitmqv1beta1.RabbitMQRejoinReadyCondition),
		Status:             wanted,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	updated := false
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == newCondition.Type {
			pod.Status.Conditions[i] = newCondition
			updated = true
			break
		}
	}
	if !updated {
		pod.Status.Conditions = append(pod.Status.Conditions, newCondition)
	}
	if err := r.Client.Status().Patch(ctx, pod, client.MergeFrom(base)); err != nil {
		return false, fmt.Errorf("setting rejoin readiness on pod %s: %w", pod.Name, err)
	}
	return true, nil
}

func (r *Reconciler) clearRejoinAnnotation(ctx context.Context, pod *corev1.Pod) error {
	base := pod.DeepCopy()
	delete(pod.Annotations, rabbitmqv1beta1.AnnotationRejoinCluster)
	if err := r.Patch(ctx, pod, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("clearing rejoin annotation on %s: %w", pod.Name, err)
	}
	return nil
}
