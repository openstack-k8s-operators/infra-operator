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

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	"github.com/openstack-k8s-operators/lib-common/modules/common/rsh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rabbitmqContainerName is the name of the RabbitMQ container in the pod spec
// (see internal/rabbitmq/statefulset.go).
const rabbitmqContainerName = "rabbitmq"

// execInPod is a seam over lib-common's rsh.ExecInPod so unit tests can inject a
// fake executor. It must not be reassigned outside of tests.
var execInPod = rsh.ExecInPod

// clusterStatus is the subset of `rabbitmqctl --formatter json cluster_status`
// output that we rely on. The JSON shape is version-dependent; only running_nodes
// is consumed, and callers tolerate an empty slice.
type clusterStatus struct {
	RunningNodes []string `json:"running_nodes"`
}

// rejoinNodeName returns the RabbitMQ Erlang node name for a pod, e.g.
// rabbit@<name>-server-0.<name>-nodes.<namespace>. This matches the StatefulSet
// headless service (<name>-nodes) and cluster_formation.k8s.address_type=hostname.
func rejoinNodeName(instance *rabbitmqv1beta1.RabbitMq, podName string) string {
	return fmt.Sprintf("rabbit@%s.%s-nodes.%s", podName, instance.Name, instance.Namespace)
}

// podIsReady reports whether the pod is Running and its Ready condition is True.
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

// runningNodes returns the set of running Erlang node names as seen by the given
// pod via `rabbitmqctl cluster_status`.
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
	for _, n := range status.RunningNodes {
		set[n] = true
	}
	return set, nil
}

// ReconcileNodeRejoin repairs cluster membership for any pod carrying the
// AnnotationRejoinCluster annotation. It is fail-closed and destructive-safe:
//   - it only ever resets a node confirmed to be standalone (its own view lists
//     <= 1 running node) that is NOT a current member of the surviving cluster;
//   - it refuses to act unless a healthy peer with quorum exists, so the last
//     surviving node is never reset;
//   - it processes at most one node per call and requeues for the rest.
//
// It returns requeue=true when it performed work (or deferred because a peer was
// not yet ready) so the caller can re-run shortly and verify the result.
func (r *Reconciler) ReconcileNodeRejoin(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
) (requeue bool, err error) {
	Log := r.GetLogger(ctx)

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(rabbitmq.SelectorLabels(instance.Name)),
	); err != nil {
		return false, fmt.Errorf("listing RabbitMq pods for rejoin: %w", err)
	}

	// Find the first pod requesting a rejoin. One per reconcile keeps the
	// destructive reset/join serialized across the cluster.
	var target *corev1.Pod
	remaining := 0
	for i := range pods.Items {
		if pods.Items[i].Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] == "true" {
			remaining++
			if target == nil {
				target = &pods.Items[i]
			}
		}
	}
	if target == nil {
		return false, nil
	}

	if !podIsReady(target) {
		Log.Info("Rejoin requested but target pod not ready yet; deferring", "pod", target.Name)
		return true, nil
	}

	replicas := int32(1)
	if instance.Spec.Replicas != nil {
		replicas = *instance.Spec.Replicas
	}
	quorum := int(replicas/2 + 1)
	targetNode := rejoinNodeName(instance, target.Name)

	// Pick a healthy anchor: a ready peer (not the target) whose own view has at
	// least quorum running nodes. This both proves the real cluster survives and
	// gives us a node to join against. Without it we must not reset anything.
	var anchor *corev1.Pod
	var anchorRunning map[string]bool
	for i := range pods.Items {
		peer := &pods.Items[i]
		if peer.Name == target.Name || !podIsReady(peer) {
			continue
		}
		running, err := r.runningNodes(ctx, peer)
		if err != nil {
			Log.Info("Failed to read cluster_status from candidate peer; skipping", "pod", peer.Name, "error", err.Error())
			continue
		}
		if len(running) >= quorum {
			anchor = peer
			anchorRunning = running
			break
		}
	}
	if anchor == nil {
		Log.Info("No healthy peer with quorum to rejoin against; deferring (never resetting the last node)",
			"pod", target.Name, "quorum", quorum)
		return true, nil
	}

	// If the target is already a running member of the surviving cluster, the
	// repair is unnecessary (or already completed): just clear the annotation.
	if anchorRunning[targetNode] {
		Log.Info("Target already a member of the surviving cluster; clearing rejoin annotation", "pod", target.Name)
		return true, r.clearRejoinAnnotation(ctx, target)
	}

	// Confirm the target genuinely came up standalone before resetting it. A node
	// that still holds real cluster data (view size > 1) must never be reset here.
	targetRunning, err := r.runningNodes(ctx, target)
	if err != nil {
		return true, fmt.Errorf("reading target cluster_status: %w", err)
	}
	if len(targetRunning) > 1 {
		Log.Info("Target is not standalone (view > 1 node); refusing to reset, deferring",
			"pod", target.Name, "runningNodes", len(targetRunning))
		return true, nil
	}

	anchorName := types.NamespacedName{Namespace: anchor.Namespace, Name: anchor.Name}
	targetName := types.NamespacedName{Namespace: target.Namespace, Name: target.Name}
	anchorNode := rejoinNodeName(instance, anchor.Name)

	Log.Info("Repairing RabbitMQ node cluster membership",
		"target", target.Name, "anchor", anchor.Name, "remainingRequests", remaining)

	// 1. Forget the stale incarnation from the surviving cluster. Tolerate "not a
	//    member" errors: the member may already be absent from the anchor's view.
	if _, err := r.rabbitmqctl(ctx, anchorName, "forget_cluster_node", targetNode); err != nil {
		Log.Info("forget_cluster_node returned an error (tolerated)", "node", targetNode, "error", err.Error())
	}

	// 2. Join the existing cluster. Since RabbitMQ 4.1 join_cluster performs the
	//    necessary stop/reset preparations itself (the seed-node discovery problem
	//    this repairs only exists on 4.1+), so no explicit stop_app/reset is
	//    needed. The join is destructive to the blank node's local state and
	//    bounces its app, which drops it from the Service during the operation.
	if _, err := r.rabbitmqctl(ctx, targetName, "join_cluster", anchorNode); err != nil {
		return true, fmt.Errorf("join_cluster %s on %s: %w", anchorNode, target.Name, err)
	}

	// 3. Verify the node is now a running member from the anchor's point of view
	//    before clearing the annotation.
	running, err := r.runningNodes(ctx, anchor)
	if err != nil {
		return true, fmt.Errorf("verifying rejoin via anchor: %w", err)
	}
	if !running[targetNode] {
		Log.Info("Node not yet visible as running from anchor after join; will retry", "node", targetNode)
		return true, nil
	}

	Log.Info("RabbitMQ node rejoined the cluster", "node", targetNode)
	return true, r.clearRejoinAnnotation(ctx, target)
}

// clearRejoinAnnotation removes the rejoin opt-in so the repair runs once per
// request.
func (r *Reconciler) clearRejoinAnnotation(ctx context.Context, pod *corev1.Pod) error {
	patch := client.MergeFrom(pod.DeepCopy())
	delete(pod.Annotations, rabbitmqv1beta1.AnnotationRejoinCluster)
	if err := r.Patch(ctx, pod, patch); err != nil {
		return fmt.Errorf("clearing rejoin annotation on %s: %w", pod.Name, err)
	}
	return nil
}
