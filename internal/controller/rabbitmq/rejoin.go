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

// rmqNodeName returns the RabbitMQ Erlang node name for a pod, e.g.
// rabbit@<name>-server-0.<name>-nodes.<namespace>. This is the same hostname
// the pod certificate's SAN uses, so it is built from one helper to keep the
// management API, the CLI and the certificate in agreement.
func rmqNodeName(instance *rabbitmqv1beta1.RabbitMq, podName string) string {
	return "rabbit@" + rabbitmq.ManagementPodServerName(podName, instance.Name, instance.Namespace)
}

// rmqPodName returns the StatefulSet pod name for an ordinal.
func rmqPodName(instance *rabbitmqv1beta1.RabbitMq, ordinal int) string {
	return fmt.Sprintf("%s-server-%d", instance.Name, ordinal)
}

// rmqConfigVersion is the RabbitMQ version this reconcile configures: the
// target version while an upgrade is in flight, otherwise the running one.
func rmqConfigVersion(instance *rabbitmqv1beta1.RabbitMq) string {
	if instance.Spec.TargetVersion != nil && *instance.Spec.TargetVersion != "" {
		return *instance.Spec.TargetVersion
	}
	if instance.Status.CurrentVersion != "" {
		return instance.Status.CurrentVersion
	}
	return DefaultRabbitMQVersion
}

// rejoinEnabled reports whether this cluster can ever need replacement
// recovery: a single broker has no quorum to protect, and the seed-node
// behavior that strands a wiped server-0 arrived in RabbitMQ 4.1.
func rejoinEnabled(instance *rabbitmqv1beta1.RabbitMq) bool {
	if instance.Spec.Replicas == nil || *instance.Spec.Replicas <= 1 {
		return false
	}
	return rabbitmq.IsVersion4_1OrLater(rmqConfigVersion(instance))
}

// RemediationDecision is what this reconcile decided about PVC remediation.
//
// "Is remediation on?" is really two questions, and one boolean cannot answer
// both safely:
//
//   - May we agree to a new PVC deletion? That follows the PodRemediator CR
//     directly. Disabling or deleting the CR has to stop it right away.
//   - Must the safety machinery stay deployed? That is not the same question.
//     Once PodRemediator has committed to deleting a PVC it goes through with
//     it even if its CR is disabled or deleted in the meantime. Taking the
//     startup gate and the Services' serving requirement away at that moment
//     would let the replacement broker, whose data directory is empty, start
//     up and serve clients while the member it is replacing is still in the
//     cluster. That is the failure this whole feature exists to prevent.
type RemediationDecision struct {
	// AllowNewConsent permits marking a stuck PVC safe to delete.
	AllowNewConsent bool
	// RecoveryInFlight means a PVC deletion we already agreed to may still be
	// in progress, so the replacement has to be brought back the safe way.
	// It is what authorizes the destructive rejoin commands.
	RecoveryInFlight bool
}

// KeepMachinery reports whether to deploy the parts that make RabbitMQ depend
// on a running operator: the startup-gate init container, the pods' serving
// label and the serving requirement in the client Services. They are needed
// both to accept a future remediation and to finish one already in progress.
func (d RemediationDecision) KeepMachinery() bool {
	return d.AllowNewConsent || d.RecoveryInFlight
}

// remediationDecisionFor answers both questions once per reconcile.
//
// A cluster that can never need replacement recovery is out of scope. For the
// rest, recovery counts as in progress when status still holds a consented
// record *and* a PodRemediator for this namespace is still there, in any
// state. The CR is the proof: PodRemediator holds its own finalizer until
// every deletion it committed to has finished, so once the CR is really gone,
// nothing can still be running and a record left in status is stale. Acting on
// a stale record would aim forget_cluster_node and join_cluster at a healthy
// cluster.
//
// A lookup error must not turn into a false: see podRemediatorScopeFor.
func (r *Reconciler) remediationDecisionFor(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq) (RemediationDecision, error) {
	if !rejoinEnabled(instance) {
		return RemediationDecision{}, nil
	}
	scope, err := r.podRemediatorScopeFor(ctx, instance.Namespace)
	if err != nil {
		return RemediationDecision{}, err
	}
	return RemediationDecision{
		AllowNewConsent:  scope.Accepting,
		RecoveryInFlight: scope.Present && hasConsentedRecovery(instance),
	}, nil
}

// hasConsentedRecovery reports whether status holds a PVC whose deletion this
// operator already agreed to. From that point PodRemediator can delete the
// claim at any moment, so recovery counts as started even while the claim is
// still there.
func hasConsentedRecovery(instance *rabbitmqv1beta1.RabbitMq) bool {
	for _, recovery := range instance.Status.PVCRemediation {
		if recovery.Consented() {
			return true
		}
	}
	return false
}

// podReady reports whether a pod is Running with its PodReady condition true.
// The kubelet sets PodReady only when all containers are ready, so this is the
// single source of truth for "healthy serving broker".
func podReady(pod *corev1.Pod) bool {
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

// heldReplacement describes a pod that is held out of client Services because
// the claim it runs on was consented for deletion.
type heldReplacement struct {
	// PVCName is the data claim being remediated.
	PVCName string
	// ReplacementUID is the UID of the new claim, once it exists and is not
	// terminating. It stays empty while the consented claim is still going
	// away, because until then there is no replacement data to rejoin with.
	ReplacementUID string
}

// heldReplacementPods maps pod name to the replacement it is waiting on, for
// every consented remediation whose original claim is gone. Those pods must
// stay out of client Services until rejoin and queue recovery are verified.
// Recovery records are persisted before deletion consent is published, so a
// replacement is recognizable on its very first reconcile.
//
// The claim reads are uncached: a lagging PVC watch could still show the
// original UID while the replacement pod is already up, which would admit an
// unverified replacement to client traffic.
func (r *Reconciler) heldReplacementPods(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
) (map[string]heldReplacement, error) {
	held := make(map[string]heldReplacement)
	if !rejoinEnabled(instance) {
		return held, nil
	}
	for pvcName, remediation := range instance.Status.PVCRemediation {
		ordinal, ok := rmqPVCOrdinal(pvcName)
		if !ok || !remediation.Consented() {
			continue
		}
		claim := &corev1.PersistentVolumeClaim{}
		err := r.safetyReader().Get(ctx,
			types.NamespacedName{Namespace: instance.Namespace, Name: pvcName}, claim)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("reading replacement PVC %s: %w", pvcName, err)
		}
		live := err == nil && claim.DeletionTimestamp == nil
		if live && string(claim.UID) == remediation.PVCUID {
			// The consented claim is still in place: nothing was replaced yet.
			continue
		}
		entry := heldReplacement{PVCName: pvcName}
		if live {
			entry.ReplacementUID = string(claim.UID)
		}
		held[rmqPodName(instance, ordinal)] = entry
	}
	return held, nil
}

// admitPods brings every pod's serving label in line with whether the
// controller has admitted it to client traffic: a held replacement is always
// excluded, and any other pod is admitted once its startup is approved.
//
// It reports whether anything changed. A patch conflict counts as a change: the
// pod moved under us, and because the Pod watch predicate filters out routine
// kubelet updates the caller must requeue rather than wait for an event.
func (r *Reconciler) admitPods(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	pods []corev1.Pod,
	held map[string]heldReplacement,
) (bool, error) {
	changed := false
	for i := range pods {
		pod := &pods[i]
		serving := false
		if _, isHeld := held[pod.Name]; !isHeld {
			allowed, startupChanged, err := r.ensurePodStartup(ctx, instance, pod)
			if err != nil {
				return changed, err
			}
			serving = allowed
			changed = changed || startupChanged
		}
		updated, err := r.setPodServing(ctx, pod, serving)
		if err != nil {
			return changed, err
		}
		changed = changed || updated
	}
	return changed, nil
}

// ensurePodsServingLabeled admits ordinary pods and backfills the serving label
// on pre-existing pods before the client Service selector is applied, so that
// an operator upgrade never drops a running broker out of the Service.
//
// It does nothing when the machinery is not deployed. No Service selects on
// the label there, so writing it would only patch every pod for a value
// nothing reads.
func (r *Reconciler) ensurePodsServingLabeled(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq, keepMachinery bool) error {
	if !keepMachinery {
		return nil
	}
	pods, err := r.rmqListPods(ctx, instance, r.Client)
	if err != nil {
		return err
	}
	held, err := r.heldReplacementPods(ctx, instance)
	if err != nil {
		return err
	}
	_, err = r.admitPods(ctx, instance, pods, held)
	return err
}

// podServing reports whether the controller has admitted a pod to client Services.
func podServing(pod *corev1.Pod) bool {
	return pod.Labels[rabbitmqv1beta1.RabbitMQServingLabel] == rabbitmqv1beta1.RabbitMQServingTrue
}

// podRabbitMQContainerReady reports whether the pod's RabbitMQ container is
// running and ready, ignoring the pod's overall readiness.
//
// A replacement held out of client traffic (serving=false) is not Ready as a
// pod, but its broker is running and can be reached by exec. This is the check
// to use before running CLI commands against such a pod; podReady is the check
// for whether a broker is serving clients.
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

// rabbitmqCommand runs a RabbitMQ CLI command in the pod's rabbitmq container
// under a bounded timeout. Both output streams are captured so that a failure
// can be reported with whatever the CLI wrote to either of them.
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
	if err != nil {
		detail := strings.TrimSpace(output.stdout + "\n" + output.stderr)
		if detail != "" {
			return output, fmt.Errorf("%w (output: %.4096s)", err, detail)
		}
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
	podNode := rmqNodeName(instance, pod.Name)
	if !membership.Running[podNode] {
		return clusterMembership{}, fmt.Errorf("cluster_status from %s does not report its own node %s as running", pod.Name, podNode)
	}
	return membership, nil
}

// ReconcileNodeRejoin drives a replacement broker back into the cluster.
//
// It acts only on a pod whose claim is recorded in RabbitMq status under the
// same PodRemediator request ID that granted that claim's deletion, which
// keeps the destructive steps scoped to one authorized remediation. The pod
// stays out of client Services until the surviving brokers and the pod itself
// agree on cluster membership and every quorum queue holds an online replica
// on the new node.
//
// recoveryInFlight is the caller's decision about whether a PVC deletion we
// agreed to may still be in progress; see RemediationDecision. When false this
// does nothing: a status.pvcRemediation record left over from a remediation
// that cannot still be running must never drive forget_cluster_node,
// join_cluster or any other destructive command against a healthy cluster. It
// stays true while the PodRemediator CR is only disabled or being deleted,
// because the deletion it committed to still goes through and the replacement
// still has to be brought back the safe way.
func (r *Reconciler) ReconcileNodeRejoin(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	recoveryInFlight bool,
) (requeue bool, err error) {
	if !recoveryInFlight || !rejoinEnabled(instance) {
		return false, nil
	}
	log := r.GetLogger(ctx)
	pods, err := r.rmqListPods(ctx, instance, r.Client)
	if err != nil {
		return false, err
	}
	podsByName := make(map[string]*corev1.Pod, len(pods))
	for i := range pods {
		podsByName[pods[i].Name] = &pods[i]
	}

	held, err := r.heldReplacementPods(ctx, instance)
	if err != nil {
		return true, err
	}

	// Release replacements that already finished recovery, so the admission
	// pass below does not pull a verified broker back out of client Services.
	for _, podName := range sortedKeys(held) {
		pod := podsByName[podName]
		remediation := instance.Status.PVCRemediation[held[podName].PVCName]
		if pod == nil || !remediation.Phase.AtLeast(rabbitmqv1beta1.PVCRemediationRecovered) || !podServing(pod) {
			continue
		}
		switch pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] {
		case "":
			// Recovery is complete; let the PVC handshake release the slot.
			delete(held, podName)
		case remediation.RequestID:
			if remediation.RequestID == "" {
				continue
			}
			if err := r.clearRejoinAnnotation(ctx, pod); err != nil {
				return true, err
			}
			return true, nil
		}
	}

	// Admit ordinary pods, including pods that start with serving=false. Held
	// replacements stay outside client Services until recovery is verified.
	changed, err := r.admitPods(ctx, instance, pods, held)
	if err != nil {
		return true, err
	}

	// Stamp the authorizing request ID on each replacement pod. The in-pod
	// recovery commands are destructive, so they run only against a pod that
	// carries the request ID that consented to its claim's deletion.
	for _, podName := range sortedKeys(held) {
		replacement := held[podName]
		pod := podsByName[podName]
		remediation := instance.Status.PVCRemediation[replacement.PVCName]
		if remediation.RequestID == "" {
			return true, fmt.Errorf("PVC %s recovery has no recorded PodRemediator request ID", replacement.PVCName)
		}
		// Without a replacement claim there is no new data directory to rejoin.
		if pod == nil || replacement.ReplacementUID == "" || !podUsesPVC(pod, replacement.PVCName) ||
			pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] == remediation.RequestID {
			continue
		}
		base := pod.DeepCopy()
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] = remediation.RequestID
		if err := r.Patch(ctx, pod, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return true, fmt.Errorf("authorizing rejoin on %s: %w", pod.Name, err)
		}
		log.Info("Authorized replacement pod rejoin from PVC consent", "pod", pod.Name, "pvc", replacement.PVCName)
		return true, nil
	}

	if changed {
		return true, nil
	}
	if len(held) == 0 {
		return false, nil
	}

	// Process one authorized replacement at a time.
	var target *corev1.Pod
	var targetPVCName string
	for i := range pods {
		pod := &pods[i]
		replacement, exists := held[pod.Name]
		if !exists {
			continue
		}
		requestID := instance.Status.PVCRemediation[replacement.PVCName].RequestID
		if requestID == "" || pod.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != requestID {
			continue
		}
		target = pod
		targetPVCName = replacement.PVCName
		break
	}
	if target == nil {
		return true, nil
	}
	quorum := quorumOf(*instance.Spec.Replicas)
	targetNode := rmqNodeName(instance, target.Name)
	var anchor *corev1.Pod
	var anchorMembership clusterMembership
	for i := range pods {
		peer := &pods[i]
		if peer.Name == target.Name || !podReady(peer) {
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

	targetName := types.NamespacedName{Namespace: target.Namespace, Name: target.Name}
	anchorNode := rmqNodeName(instance, anchor.Name)
	remediation := instance.Status.PVCRemediation[targetPVCName]
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: targetPVCName}, claim); err != nil {
		return true, err
	}
	if !remediation.Phase.AtLeast(rabbitmqv1beta1.PVCRemediationMemberRemoved) ||
		remediation.ReplacementPVCUID != string(claim.UID) {
		return true, r.removeOldMember(ctx, instance, target, anchor, pods, targetPVCName, anchorMembership)
	}
	allowed, startupChanged, err := r.ensurePodStartup(ctx, instance, target)
	if err != nil {
		return true, err
	}
	if !allowed || startupChanged || !podRabbitMQContainerReady(target) {
		log.Info("Authorized rejoin is waiting for replacement startup", "pod", target.Name)
		return true, nil
	}
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

	// The old member was removed and that progress persisted before startup.
	// join_cluster performs the fresh-member reset/add lifecycle on the target.
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

// verifyQuorumQueueReplicasOnline keeps the replacement behind its readiness gate
// until RabbitMQ reports it as an online member of every quorum queue.
func (r *Reconciler) verifyQuorumQueueReplicasOnline(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	pod *corev1.Pod,
	targetNode string,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	creds, err := r.rmqLoadMgmtCreds(ctx, instance)
	if err != nil {
		return fmt.Errorf("reading management credentials: %w", err)
	}

	// Record "soft" conditions and decide only after a complete pass, so the
	// reported reason is not whichever queue happened to be scanned first.
	missingReplicaQueue := ""
	offlineReplicaQueue := ""
	if err := r.scanQueues(ctx, instance, pod, creds,
		"name,vhost,type,members,online",
		func(queue rmqQueueStatus) error {
			if queue.Type != "quorum" {
				return nil
			}
			members, online, err := quorumQueueMembers(queue)
			if err != nil {
				return err
			}
			if !members[targetNode] {
				missingReplicaQueue = queue.Vhost + "/" + queue.Name
				return nil
			}
			if !online[targetNode] {
				offlineReplicaQueue = queue.Vhost + "/" + queue.Name
			}
			return nil
		}); err != nil {
		return err
	}
	if missingReplicaQueue != "" {
		return fmt.Errorf("RabbitMq quorum queue %s does not include %s yet; waiting for Continuous Membership Reconciliation", missingReplicaQueue, targetNode)
	}
	if offlineReplicaQueue != "" {
		return fmt.Errorf("RabbitMq quorum queue %s replacement member %s is not online yet", offlineReplicaQueue, targetNode)
	}
	return nil
}

// finishNodeRejoin admits a rejoined replacement to client traffic once it
// holds an online replica of every quorum queue.
func (r *Reconciler) finishNodeRejoin(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	pod *corev1.Pod,
	pvcName string,
	requestID string,
) error {
	remediation, ok := instance.Status.PVCRemediation[pvcName]
	if !ok || !remediation.Consented() || remediation.RequestID != requestID || requestID == "" {
		return fmt.Errorf("PVC %s no longer has the consented request for RabbitMQ rejoin", pvcName)
	}
	nodeName := rmqNodeName(instance, pod.Name)
	// Placing the replicas is left to Continuous Membership Reconciliation,
	// which is enabled on exactly these clusters (4.x, multi-replica; see
	// internal/rabbitmq/configmap.go) and fires on node join. The operator only
	// observes the result: until CMR has added this node to every quorum queue
	// and those replicas are online, the replacement stays out of client
	// Services and the caller requeues.
	if err := r.verifyQuorumQueueReplicasOnline(ctx, instance, pod, nodeName); err != nil {
		return fmt.Errorf("verifying quorum queue replicas on %s: %w", pod.Name, err)
	}
	remediation.Phase = rabbitmqv1beta1.PVCRemediationRecovered
	instance.Status.PVCRemediation[pvcName] = remediation
	if _, err := r.setPodServing(ctx, pod, true); err != nil {
		return err
	}
	return r.clearRejoinAnnotation(ctx, pod)
}

// setPodServing sets the serving label that controls whether a pod is included in
// the client-facing Service, patching metadata (not status). It returns whether a
// change was made. New pods wait for admission, while existing pods retain their
// serving label through container restarts and operator downtime.
func (r *Reconciler) setPodServing(ctx context.Context, pod *corev1.Pod, serving bool) (bool, error) {
	wanted := rabbitmqv1beta1.RabbitMQServingFalse
	if serving {
		wanted = rabbitmqv1beta1.RabbitMQServingTrue
	}
	if pod.Labels[rabbitmqv1beta1.RabbitMQServingLabel] == wanted {
		return false, nil
	}
	base := pod.DeepCopy()
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[rabbitmqv1beta1.RabbitMQServingLabel] = wanted
	// Guard with the base resourceVersion: if the StatefulSet recreated this pod
	// after the rejoin/queue checks ran, the patch must conflict rather than mark
	// an unchecked pod as serving.
	err := r.Patch(ctx, pod, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		// The pod moved under us. Report a change so the caller re-reads it,
		// rather than failing a reconcile over an ordinary kubelet update.
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("setting serving label on pod %s: %w", pod.Name, err)
	}
	return true, nil
}

// clearRejoinAnnotation removes the rejoin authorization once recovery is done,
// so the destructive in-pod commands cannot run again for the same request.
func (r *Reconciler) clearRejoinAnnotation(ctx context.Context, pod *corev1.Pod) error {
	base := pod.DeepCopy()
	delete(pod.Annotations, rabbitmqv1beta1.AnnotationRejoinCluster)
	err := r.Patch(ctx, pod, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("clearing rejoin annotation on %s: %w", pod.Name, err)
	}
	return nil
}

// sortedKeys returns a map's keys in a stable order, so that reconciles process
// the same work in the same sequence.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
