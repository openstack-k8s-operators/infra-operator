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
	"strings"
	"testing"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testName = "rabbitmq"
	testNS   = "openstack"
)

func node(pod string) string {
	return fmt.Sprintf("rabbit@%s.%s-nodes.%s", pod, testName, testNS)
}

// fakeExec records rabbitmqctl invocations and answers cluster_status from a
// per-pod running-nodes map that it mutates as the repair progresses.
type fakeExec struct {
	running map[string][]string // podName -> running node names it reports
	calls   []string            // "<pod>:<args...>" in invocation order
}

func (f *fakeExec) run(_ context.Context, _ kubernetes.Interface, _ *rest.Config, podName types.NamespacedName,
	_ string, cmd []string, fun func(*bytes.Buffer, *bytes.Buffer) error) error {
	args := cmd[1:] // drop "rabbitmqctl"
	f.calls = append(f.calls, podName.Name+":"+strings.Join(args, " "))

	// Simulate the target joining: after join_cluster the target becomes a member
	// of every other node's view.
	if len(args) >= 1 && args[0] == "join_cluster" {
		joined := node(podName.Name)
		for p := range f.running {
			if p == podName.Name {
				continue
			}
			f.running[p] = append(f.running[p], joined)
		}
		f.running[podName.Name] = nil
	}

	if last := args[len(args)-1]; last == "cluster_status" {
		status := clusterStatus{RunningNodes: f.running[podName.Name]}
		b, _ := json.Marshal(status)
		var out bytes.Buffer
		out.Write(b)
		return fun(&out, &bytes.Buffer{})
	}
	return nil
}

func (f *fakeExec) issued(call string) bool {
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

// orderOf returns the index of the first call matching substr, or -1.
func (f *fakeExec) orderOf(substr string) int {
	for i, c := range f.calls {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

func readyPod(name string, annotated bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels:    rabbitmq.SelectorLabels(testName),
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if annotated {
		p.Annotations = map[string]string{rabbitmqv1beta1.AnnotationRejoinCluster: "true"}
	}
	return p
}

func newReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rabbitmqv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Reconciler{Client: c, Scheme: scheme}
}

func instanceWith(replicas int32) *rabbitmqv1beta1.RabbitMq {
	return &rabbitmqv1beta1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS},
		Spec: rabbitmqv1beta1.RabbitMqSpec{
			RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{Replicas: ptr.To(replicas)},
		},
	}
}

// swapExec installs the fake executor and returns a restore func for defer.
func swapExec(f *fakeExec) func() {
	orig := execInPod
	execInPod = f.run
	return func() { execInPod = orig }
}

// realClusterStatus42 is a trimmed-but-faithful sample of
// `rabbitmqctl cluster_status --formatter json` from RabbitMQ 4.2.9. It keeps the
// surrounding fields we deliberately ignore (listeners, versions, partitions,
// feature_flags) to prove the parser tolerates them.
const realClusterStatus42 = `{
  "alarms": [],
  "cluster_tags": [],
  "listeners": {"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack": [{"port": 5671, "protocol": "amqp/ssl"}]},
  "cpu_cores": {"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack": 24},
  "cluster_name": "rabbitmq",
  "disk_nodes": ["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],
  "ram_nodes": [],
  "running_nodes": ["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],
  "versions": {"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack": {"rabbitmq_version": "4.2.9"}},
  "partitions": {},
  "feature_flags": [{"name": "khepri_db", "state": "enabled"}]
}`

func TestClusterStatusParsing42(t *testing.T) {
	var status clusterStatus
	if err := json.Unmarshal([]byte(realClusterStatus42), &status); err != nil {
		t.Fatalf("failed to parse real 4.2 cluster_status: %v", err)
	}
	want := []string{
		node("rabbitmq-server-0"),
		node("rabbitmq-server-1"),
		node("rabbitmq-server-2"),
	}
	if len(status.RunningNodes) != len(want) {
		t.Fatalf("running_nodes = %v, want %v", status.RunningNodes, want)
	}
	for i, n := range want {
		if status.RunningNodes[i] != n {
			t.Errorf("running_nodes[%d] = %q, want %q", i, status.RunningNodes[i], n)
		}
	}
}

func TestRejoinNoAnnotationIsNoop(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}
	defer swapExec(fx)()

	r := newReconciler(t,
		readyPod(testName+"-server-0", false),
		readyPod(testName+"-server-1", false))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue {
		t.Error("expected no requeue when no pod requests rejoin")
	}
	if len(fx.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", fx.calls)
	}
}

func TestRejoinRefusesWithoutHealthyPeer(t *testing.T) {
	// Only the target is present/ready; resetting it would destroy the last node.
	fx := &fakeExec{running: map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
	}}
	defer swapExec(fx)()

	r := newReconciler(t, readyPod(testName+"-server-0", true))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue while deferring")
	}
	if fx.issued(testName + "-server-0:reset") {
		t.Error("must never reset the last surviving node")
	}
}

func TestRejoinHappyPath(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")}, // standalone
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	}}
	defer swapExec(fx)()

	target := readyPod(testName+"-server-0", true)
	r := newReconciler(t, target,
		readyPod(testName+"-server-1", false),
		readyPod(testName+"-server-2", false))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue after performing a rejoin")
	}

	// forget happens on a peer, targeting the stale node; join happens on target.
	// On 4.1+ join_cluster self-prepares, so no stop_app/reset/start_app.
	forgetCall := testName + "-server-1:forget_cluster_node " + node(testName+"-server-0")
	if !fx.issued(forgetCall) {
		t.Errorf("expected forget_cluster_node on peer, calls=%v", fx.calls)
	}
	joinCall := testName + "-server-0:join_cluster " + node(testName+"-server-1")
	if !fx.issued(joinCall) {
		t.Errorf("expected join_cluster on target, calls=%v", fx.calls)
	}
	for _, unwanted := range []string{"stop_app", "reset", "start_app"} {
		if fx.orderOf(unwanted) != -1 {
			t.Errorf("did not expect %q on 4.1+, calls=%v", unwanted, fx.calls)
		}
	}
	// Ordering: forget (on peer) precedes join (on target).
	if !(fx.orderOf("forget_cluster_node") < fx.orderOf("join_cluster")) {
		t.Errorf("repair steps out of order: %v", fx.calls)
	}

	// Annotation cleared after successful verify.
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster]; ok {
		t.Error("expected rejoin annotation to be cleared")
	}
}

func TestRejoinAlreadyMemberJustClears(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1")},
	}}
	defer swapExec(fx)()

	target := readyPod(testName+"-server-0", true)
	r := newReconciler(t, target, readyPod(testName+"-server-1", false))

	_, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fx.issued(testName + "-server-0:reset") {
		t.Error("must not reset a node that is already a cluster member")
	}
	got := &corev1.Pod{}
	_ = r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, got)
	if _, ok := got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster]; ok {
		t.Error("expected annotation cleared when already a member")
	}
}

func TestRejoinRefusesNonStandaloneTarget(t *testing.T) {
	// Target reports a multi-node view: it still holds real data, must not reset.
	fx := &fakeExec{running: map[string][]string{
		testName + "-server-0": {node(testName + "-server-0"), node(testName + "-server-2")},
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
	}}
	defer swapExec(fx)()

	r := newReconciler(t,
		readyPod(testName+"-server-0", true),
		readyPod(testName+"-server-1", false))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue while deferring")
	}
	if fx.issued(testName + "-server-0:reset") {
		t.Error("must not reset a target that is not standalone")
	}
}
