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
	"strings"
	"time"

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

const rabbitmqCommandTimeout = 2 * time.Minute

// clusterStatus is the subset of `rabbitmqctl --formatter json cluster_status`
// output that we rely on.
type clusterStatus struct {
	DiskNodes    []string `json:"disk_nodes"`
	RAMNodes     []string `json:"ram_nodes"`
	RunningNodes []string `json:"running_nodes"`
}

// clusterMembership distinguishes configured members from nodes that are
// currently online. RabbitMQ keeps an offline node in disk_nodes/ram_nodes
// until it is explicitly forgotten.
type clusterMembership struct {
	Configured map[string]bool
	Running    map[string]bool
}

func (s clusterStatus) membership() (clusterMembership, error) {
	if s.DiskNodes == nil || s.RAMNodes == nil || s.RunningNodes == nil {
		return clusterMembership{}, fmt.Errorf("cluster_status is missing disk_nodes, ram_nodes, or running_nodes")
	}
	membership := clusterMembership{
		Configured: make(map[string]bool, len(s.DiskNodes)+len(s.RAMNodes)),
		Running:    make(map[string]bool, len(s.RunningNodes)),
	}
	for _, node := range append(append([]string{}, s.DiskNodes...), s.RAMNodes...) {
		if node == "" {
			return clusterMembership{}, fmt.Errorf("cluster_status contains an empty configured node name")
		}
		membership.Configured[node] = true
	}
	if len(membership.Configured) == 0 {
		return clusterMembership{}, fmt.Errorf("cluster_status contains no configured nodes")
	}
	for _, node := range s.RunningNodes {
		if node == "" {
			return clusterMembership{}, fmt.Errorf("cluster_status contains an empty running node name")
		}
		if !membership.Configured[node] {
			return clusterMembership{}, fmt.Errorf("running node %q is not listed in disk_nodes or ram_nodes", node)
		}
		if membership.Running[node] {
			return clusterMembership{}, fmt.Errorf("cluster_status lists running node %q more than once", node)
		}
		membership.Running[node] = true
	}
	if len(membership.Running) == 0 {
		return clusterMembership{}, fmt.Errorf("cluster_status contains no running nodes")
	}
	return membership, nil
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

func podRejoinReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodConditionType(rabbitmqv1beta1.RabbitMQRejoinReadyCondition) {
			return condition.Status == corev1.ConditionTrue
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

type rabbitmqCommandOutput struct {
	stdout string
	stderr string
}

// rabbitmqCommand runs a bounded RabbitMQ CLI command in the pod's rabbitmq
// container. Both output streams are retained because rabbitmq-queues reports
// per-queue failures on stderr while still exiting successfully.
func (r *Reconciler) rabbitmqCommand(ctx context.Context, podName types.NamespacedName, command string, args ...string) (rabbitmqCommandOutput, error) {
	cmd := append([]string{command}, args...)
	commandCtx, cancel := context.WithTimeout(ctx, rabbitmqCommandTimeout)
	defer cancel()

	executor := r.PodCommandExecutor
	if executor == nil {
		executor = rsh.ExecInPod
	}
	var output rabbitmqCommandOutput
	err := executor(commandCtx, r.Kclient, r.config, podName, rabbitmqContainerName, cmd,
		func(out *bytes.Buffer, errOut *bytes.Buffer) error {
			output.stdout = out.String()
			output.stderr = errOut.String()
			return nil
		})
	if err != nil && output.stderr != "" {
		return output, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(output.stderr))
	}
	return output, err
}

func (r *Reconciler) rabbitmqctl(ctx context.Context, podName types.NamespacedName, args ...string) (string, error) {
	output, err := r.rabbitmqCommand(ctx, podName, "rabbitmqctl", args...)
	return output.stdout, err
}

func (r *Reconciler) clusterMembership(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	pod *corev1.Pod,
) (clusterMembership, error) {
	out, err := r.rabbitmqctl(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
		"--formatter", "json", "cluster_status")
	if err != nil {
		return clusterMembership{}, err
	}
	var status clusterStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return clusterMembership{}, fmt.Errorf("parsing cluster_status from %s: %w", pod.Name, err)
	}
	membership, err := status.membership()
	if err != nil {
		return clusterMembership{}, fmt.Errorf("parsing cluster_status from %s: %w", pod.Name, err)
	}
	podNode := rejoinNodeName(instance, pod.Name)
	if !membership.Running[podNode] {
		return clusterMembership{}, fmt.Errorf("cluster_status from %s does not report its own node %s as running", pod.Name, podNode)
	}
	return membership, nil
}

// ReconcileNodeRejoin authorizes a rejoin only for a replacement PVC recorded
// in RabbitMq status with the same PodRemediator request ID that granted its
// deletion. It keeps that pod unready until both surviving and target nodes
// confirm cluster membership and quorum queue growth.
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
	type pendingRejoin struct {
		pvcName   string
		requestID string
	}
	pendingPods := make(map[string]pendingRejoin)
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

		pod := podsByName[podName]
		if pod != nil && remediation.QuorumQueuesGrown && podRejoinReady(pod) {
			if pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] == remediation.RequestID {
				if err := r.clearRejoinAnnotation(ctx, pod); err != nil {
					return true, err
				}
				return true, nil
			}
			if pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] == "" {
				// The verified replacement already completed rejoin and queue growth.
				// Let the PVC handshake check that the configured replica count is back.
				continue
			}
		}
		pendingPods[podName] = pendingRejoin{pvcName: pvcName, requestID: remediation.RequestID}
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
	var targetPVCName string
	for i := range pods.Items {
		pod := &pods.Items[i]
		pending, exists := pendingPods[pod.Name]
		if !exists || pending.requestID == "" || pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != pending.requestID {
			continue
		}
		target = pod
		targetPVCName = pending.pvcName
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
	var anchorMembership clusterMembership
	for i := range pods.Items {
		peer := &pods.Items[i]
		if peer.Name == target.Name || !podIsReady(peer) {
			continue
		}
		membership, err := r.clusterMembership(ctx, instance, peer)
		if err != nil {
			log.Info("Could not read cluster status from candidate peer", "pod", peer.Name, "error", err.Error())
			continue
		}
		if len(membership.Running) >= quorum {
			anchor, anchorMembership = peer, membership
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
	remediation := instance.Status.PVCRemediation[targetPVCName]
	targetMembership, err := r.clusterMembership(ctx, instance, target)
	if err != nil {
		return true, fmt.Errorf("reading target cluster status: %w", err)
	}
	if anchorMembership.Running[targetNode] && len(targetMembership.Running) >= quorum && targetMembership.Running[anchorNode] {
		return true, r.finishNodeRejoin(ctx, instance, target, targetPVCName, remediation.RequestID)
	}
	if anchorMembership.Running[targetNode] {
		// The anchor sees this node as online, but the target does not report
		// the same quorum membership. Do not forget or join either side while
		// their views disagree.
		log.Info("Target and surviving cluster disagree about running membership; deferring rejoin", "pod", target.Name)
		return true, nil
	}
	if len(targetMembership.Configured) > 1 {
		log.Info("Authorized replacement is not standalone; refusing reset", "pod", target.Name, "configuredNodes", len(targetMembership.Configured))
		return true, nil
	}

	// A deleted seed remains in disk_nodes/ram_nodes but disappears from
	// running_nodes. Forget only that configured-but-offline member. Never
	// forget an online node or infer stale membership from a partial status.
	if anchorMembership.Configured[targetNode] && !anchorMembership.Running[targetNode] {
		if _, err := r.rabbitmqctl(ctx, anchorName, "forget_cluster_node", targetNode); err != nil {
			return true, fmt.Errorf("forgetting stale RabbitMQ node %s: %w", targetNode, err)
		}
		anchorMembership, err = r.clusterMembership(ctx, instance, anchor)
		if err != nil {
			return true, fmt.Errorf("verifying stale node removal from anchor: %w", err)
		}
		if len(anchorMembership.Running) < quorum || !anchorMembership.Running[anchorNode] {
			log.Info("Anchor lost quorum after forgetting stale node; deferring rejoin", "pod", anchor.Name, "quorum", quorum)
			return true, nil
		}
		if anchorMembership.Configured[targetNode] || anchorMembership.Running[targetNode] {
			log.Info("Anchor still reports stale node after forget; deferring rejoin", "pod", anchor.Name, "target", targetNode)
			return true, nil
		}
	}
	if _, err := r.rabbitmqctl(ctx, targetName, "join_cluster", anchorNode); err != nil {
		return true, fmt.Errorf("joining RabbitMQ node %s to %s: %w", target.Name, anchorNode, err)
	}

	anchorMembership, err = r.clusterMembership(ctx, instance, anchor)
	if err != nil {
		return true, fmt.Errorf("verifying rejoin via anchor: %w", err)
	}
	targetMembership, err = r.clusterMembership(ctx, instance, target)
	if err != nil {
		return true, fmt.Errorf("verifying target cluster status after rejoin: %w", err)
	}
	if !anchorMembership.Running[targetNode] || !targetMembership.Running[anchorNode] || len(targetMembership.Running) < quorum {
		log.Info("RabbitMQ membership is not yet consistent after join", "target", target.Name)
		return true, nil
	}
	log.Info("RabbitMQ replacement rejoined its cluster", "node", targetNode, "requestID", target.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster])
	return true, r.finishNodeRejoin(ctx, instance, target, targetPVCName, remediation.RequestID)
}

func (r *Reconciler) finishNodeRejoin(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	pod *corev1.Pod,
	pvcName string,
	requestID string,
) error {
	remediation, ok := instance.Status.PVCRemediation[pvcName]
	if !ok || !remediation.ConsentGranted || remediation.RequestID != requestID || requestID == "" {
		return fmt.Errorf("PVC %s no longer has the consented request for RabbitMQ rejoin", pvcName)
	}
	if !remediation.QuorumQueuesGrown {
		nodeName := rejoinNodeName(instance, pod.Name)
		output, err := r.rabbitmqCommand(ctx,
			types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
			"rabbitmq-queues", "grow", nodeName, "all", "--errors-only", "--silent")
		if err != nil {
			return fmt.Errorf("growing quorum queue replicas on %s: %w", nodeName, err)
		}
		if outputText := strings.TrimSpace(strings.TrimSpace(output.stdout) + "\n" + strings.TrimSpace(output.stderr)); outputText != "" {
			return fmt.Errorf("growing quorum queue replicas on %s reported per-queue errors: %s", nodeName, outputText)
		}
		remediation.QuorumQueuesGrown = true
		instance.Status.PVCRemediation[pvcName] = remediation
	}
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
