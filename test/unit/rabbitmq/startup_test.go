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

package rabbitmq_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqresources "github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const startupApproval = "rabbitmq.openstack.org/startup-approved"

func gatedReplacement() *corev1.Pod {
	p := replacementPod(testRequestID)
	p.UID = types.UID("new-pod-uid")
	p.Spec.InitContainers = []corev1.Container{{Name: "startup-gate", Image: "rabbitmq:4.2"}}
	p.Status.Phase = corev1.PodPending
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "startup-gate", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	p.Status.ContainerStatuses[0].Ready = false
	p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}
	return p
}

func rawRecovery() *rabbitmqv1.RabbitMq {
	r := instanceWithRecovery(3)
	// This test starts at the original consent, before member removal.
	entry := r.Status.PVCRemediation[testPVCName]
	entry.ReplacementPVCUID = ""
	entry.Phase = rabbitmqv1.PVCRemediationConsented
	r.Status.PVCRemediation[testPVCName] = entry
	return r
}

func TestNewRabbitMQPodsWaitForStartupApproval(t *testing.T) {
	sts := rabbitmqresources.StatefulSet(instanceWith(3), "config", nil, nil, "4.2", false, rabbitmqresources.ProxyConfig{}, true)
	if len(sts.Spec.Template.Spec.InitContainers) == 0 || sts.Spec.Template.Spec.InitContainers[0].Name != "startup-gate" {
		t.Fatal("new pods must block before any RabbitMQ setup or server process")
	}
	if sts.Spec.Template.Annotations[startupApproval] != "" {
		t.Fatal("the template must not approve future pod UIDs")
	}
	// A ResourceQuota that tracks cpu or memory rejects a pod when any of its
	// containers leaves that resource unset, init containers included, so an
	// unset gate would make turning remediation on break deployment in a
	// quota'd namespace.
	gate := sts.Spec.Template.Spec.InitContainers[0]
	for _, resourceName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if _, ok := gate.Resources.Requests[resourceName]; !ok {
			t.Errorf("the startup gate must request %s", resourceName)
		}
		if _, ok := gate.Resources.Limits[resourceName]; !ok {
			t.Errorf("the startup gate must limit %s", resourceName)
		}
	}
}

func TestStartupScriptRequiresItsOwnPodUID(t *testing.T) {
	sts := rabbitmqresources.StatefulSet(instanceWith(3), "config", nil, nil, "4.2", false, rabbitmqresources.ProxyConfig{}, true)
	gate := sts.Spec.Template.Spec.InitContainers[0]
	if gate.Name != "startup-gate" {
		t.Fatal("startup gate is missing")
	}
	approval := filepath.Join(t.TempDir(), "startupApproved")
	if err := os.WriteFile(approval, []byte("previous-pod-uid"), 0600); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(gate.Args[0], "/etc/pod-info/startupApproved", approval)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", script)
	command.Env = append(os.Environ(), "POD_UID=current-pod-uid")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("an older pod's permission released startup: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(approval, []byte("current-pod-uid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("matching pod UID did not release startup: %v", err)
	}
}

func TestReplacementForgetsOldMemberBeforeBrokerStartup(t *testing.T) {
	ctx := context.Background()
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	for _, peer := range []string{testName + "-server-1", testName + "-server-2"} {
		fx.configured[peer] = []string{node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")}
	}
	instance := rawRecovery()
	target := gatedReplacement()
	r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
	if _, err := r.ReconcileNodeRejoin(ctx, instance, true); err != nil {
		t.Fatal(err)
	}
	if fx.orderOf("forget_cluster_node "+node(target.Name)) < 0 {
		t.Fatalf("the old member must be removed while the replacement is still in init, calls=%v", fx.calls)
	}
	if fx.orderOf(target.Name+":") >= 0 {
		t.Fatalf("must not execute broker commands in a blocked replacement, calls=%v", fx.calls)
	}
	current := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[startupApproval] != "" {
		t.Fatal("startup must wait until member-removal progress has been persisted")
	}
	entry, _ := json.Marshal(instance.Status.PVCRemediation[testPVCName])
	var progress map[string]interface{}
	_ = json.Unmarshal(entry, &progress)
	if progress["phase"] != "MemberRemoved" || progress["replacementPVCUID"] != newPVCUID {
		t.Fatalf("removal must be recorded against this replacement claim: %s", entry)
	}
	if _, err := r.ReconcileNodeRejoin(ctx, instance, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[startupApproval] != "" {
		t.Fatal("in-memory progress must not authorize startup before the API stores it")
	}
	// Model persistence by the main reconcile's deferred status patch, then a
	// controller restart. Do not reuse its in-memory progress as authorization.
	if err := r.Update(ctx, instance); err != nil {
		t.Fatal(err)
	}
	fresh := &rabbitmqv1.RabbitMq{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
		t.Fatal(err)
	}
	count := len(fx.calls)
	if _, err := r.ReconcileNodeRejoin(ctx, fresh, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[startupApproval] != string(current.UID) {
		t.Fatal("durable removal progress must authorize only this pod UID")
	}
	for _, call := range fx.calls[count:] {
		if call == testName+"-server-1:rabbitmqctl forget_cluster_node "+node(target.Name) {
			t.Fatal("controller restart must not forget the member again")
		}
	}
	if podRejoinReady(current) {
		t.Fatal("startup approval must not admit client traffic")
	}
}

func TestStartupRemovalUsesAuthoritativeMembership(t *testing.T) {
	ctx := context.Background()
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	// Both local caches omit the old member, but the leader still includes it.
	fx.consistent = map[string][]string{
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	}
	instance := rawRecovery()
	target := gatedReplacement()
	r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
	if _, err := r.ReconcileNodeRejoin(ctx, instance, true); err != nil {
		t.Fatal(err)
	}
	if fx.orderOf("forget_cluster_node") < 0 {
		t.Fatal("a local membership-cache omission must not be treated as completed removal")
	}
}

func TestStartupResumesAfterRemovalBeforeStatusWasPersisted(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	instance := rawRecovery() // A previous process removed the member but crashed before recording it.
	r := newReconciler(t, fx, instance, gatedReplacement(), replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
	if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err != nil {
		t.Fatal(err)
	}
	if fx.orderOf("forget_cluster_node") >= 0 {
		t.Fatal("already completed removal must be verified rather than repeated")
	}
	if instance.Status.PVCRemediation[testPVCName].Phase != rabbitmqv1.PVCRemediationMemberRemoved {
		t.Fatal("verified progress must be restored after a controller restart")
	}
}

func TestStartupRemovalRequiresAnActiveQueueQuorum(t *testing.T) {
	for _, response := range []string{"read times out", "missing confirmation"} {
		t.Run(response, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			if response == "read times out" {
				fx.quorumReadErr = errors.New("queue Ra group has no elected leader")
			} else {
				fx.quorumReadOutput = "ok\n"
			}
			instance := rawRecovery()
			target := gatedReplacement()
			r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID),
				clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
			if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err == nil {
				t.Fatal("online replica metadata must not replace an active quorum-confirmed read")
			}
			if fx.orderOf("forget_cluster_node") >= 0 {
				t.Fatal("must verify a working queue quorum before removing the old member")
			}
		})
	}
}

func TestStartupRemovalDoesNotCountCandidateInQueueMajority(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	candidate := node(testName + "-server-0")
	for queue := range fx.quorumQueueMembers {
		fx.quorumQueueMembers[queue][candidate] = true
		fx.quorumQueueOnline[queue][candidate] = true
	}
	queues := []map[string]interface{}{}
	for queue, members := range fx.quorumQueueMembers {
		results := []map[string]interface{}{}
		for member := range members {
			state := "follower"
			if member == node(testName+"-server-1") {
				state = "leader"
			}
			results = append(results, map[string]interface{}{
				"node":       member,
				"error":      false,
				"state":      state,
				"membership": "voter",
				"term":       1,
				"leader":     node(testName + "-server-1"),
				"members":    len(members),
			})
		}
		queues = append(queues, map[string]interface{}{
			"vhost": "/", "name": queue, "expected_members": len(members),
			"acknowledged": 1, "participants": results,
		})
	}
	encoded, err := json.Marshal(queues)
	if err != nil {
		t.Fatal(err)
	}
	fx.quorumReadOutput = "QUEUE_CONSENSUS=" + string(encoded) + "\nok\n"
	instance := rawRecovery()
	target := gatedReplacement()
	r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
	if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err == nil ||
		!strings.Contains(err.Error(), "lacks a candidate-excluded heartbeat majority") {
		t.Fatalf("reconciliation must fail at the candidate-excluded quorum proof, got %v", err)
	}
	if fx.lastOrderOf("QUEUE_CONSENSUS=") < 0 {
		t.Fatal("the candidate-exclusion case must reach the queue consensus proof")
	}
	if fx.orderOf("forget_cluster_node") >= 0 {
		t.Fatal("must not remove the candidate without a candidate-excluded queue majority")
	}
	candidateJSON := base64.StdEncoding.EncodeToString([]byte(candidate))
	for _, call := range fx.calls {
		if strings.Contains(call, candidateJSON) {
			t.Fatal("queue consensus query included the deletion candidate")
		}
	}
}

func TestQueueConsensusProofCountsOnlySafeVoters(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	instance := rawRecovery()
	r := newReconciler(t, fx, instance, gatedReplacement(), replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
	if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err != nil {
		t.Fatal(err)
	}
	proof := ""
	for _, call := range fx.calls {
		if strings.Contains(call, "QUEUE_CONSENSUS=") {
			proof = call
			break
		}
	}
	if proof == "" {
		t.Fatal("queue consensus proof was not invoked")
	}
	if !strings.Contains(proof, `VoterNodes=[B || #{node:=B,membership:=<<"voter">>,error:=false} <- Participants]`) {
		t.Fatal("queue consensus proof must select only live voters")
	}
	if !strings.Contains(proof, `length([B || B<-VoterNodes,`) {
		t.Fatal("queue consensus acknowledgements must count only the selected voters")
	}
}

func TestStartupRemovalRefusesOnlineOldMemberOrUnsafeQueues(t *testing.T) {
	for _, scenario := range []string{"old member online", "queue below quorum"} {
		t.Run(scenario, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			if scenario == "old member online" {
				for peer := range fx.running {
					fx.running[peer] = append(fx.running[peer], node(testName+"-server-0"))
					fx.configured[peer] = append(fx.configured[peer], node(testName+"-server-0"))
				}
			} else {
				fx.quorumQueueMembers["orders"][node(testName+"-server-0")] = true
				delete(fx.quorumQueueOnline["orders"], node(testName+"-server-2"))
				fx.quorumReadErr = errors.New("safe queue replica cannot participate")
			}
			instance := rawRecovery()
			target := gatedReplacement()
			r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID),
				clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
			if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err == nil {
				t.Fatal("expected unsafe removal to fail closed")
			}
			got := &corev1.Pod{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(target), got); err != nil {
				t.Fatal(err)
			}
			if got.Annotations[startupApproval] != "" || fx.orderOf("forget_cluster_node") >= 0 {
				t.Fatalf("unsafe recovery opened startup or changed membership: %v", fx.calls)
			}
		})
	}
}

func TestStartupGateStaysClosedWithoutSurvivingQuorum(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1")},
	})
	target := gatedReplacement()
	instance := rawRecovery()
	r := newReconciler(t, fx, instance, target, replacementPVC(newPVCUID), clusterPod(testName+"-server-1", true))
	if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(target), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[startupApproval] != "" || fx.orderOf("forget_cluster_node") >= 0 {
		t.Fatalf("neither startup nor membership changes are safe without quorum, calls=%v", fx.calls)
	}
}

func TestStartupApprovalRequiresTheDurableRecoveryRequest(t *testing.T) {
	for _, scenario := range []string{"record removed", "request changed", "consent withdrawn", "different replacement"} {
		t.Run(scenario, func(t *testing.T) {
			instance := instanceAfterMemberRemoval(3)
			stored := instance.DeepCopy()
			entry := stored.Status.PVCRemediation[testPVCName]
			switch scenario {
			case "record removed":
				stored.Status.PVCRemediation = nil
			case "request changed":
				entry.RequestID = "another-request"
			case "consent withdrawn":
				entry.Phase = rabbitmqv1.PVCRemediationPending
			case "different replacement":
				entry.ReplacementPVCUID = "another-claim"
			}
			if scenario != "record removed" {
				stored.Status.PVCRemediation[testPVCName] = entry
			}
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			target := gatedReplacement()
			r := newReconciler(t, fx, stored, target, replacementPVC(newPVCUID),
				clusterPod(testName+"-server-1", true), clusterPod(testName+"-server-2", true))
			if _, err := r.ReconcileNodeRejoin(context.Background(), instance, true); err != nil {
				t.Fatal(err)
			}
			got := &corev1.Pod{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(target), got); err != nil {
				t.Fatal(err)
			}
			if got.Annotations[startupApproval] != "" {
				t.Fatal("a withdrawn or different recovery record authorized startup")
			}
		})
	}
}
