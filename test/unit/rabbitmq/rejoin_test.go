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

package rabbitmq_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqcontroller "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"
	rabbitmqresources "github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	testName      = "rabbitmq"
	testNS        = "openstack"
	testPVCName   = "persistence-rabbitmq-server-0"
	testRequestID = "podremediator-request-123"
	oldPVCUID     = "old-pvc-uid"
	newPVCUID     = "new-pvc-uid"
)

func node(pod string) string {
	return fmt.Sprintf("rabbit@%s.%s-nodes.%s", pod, testName, testNS)
}

type clusterStatusForTest struct {
	DiskNodes    []string `json:"disk_nodes"`
	RAMNodes     []string `json:"ram_nodes"`
	RunningNodes []string `json:"running_nodes"`
}

// fakeExec records RabbitMQ CLI invocations and answers cluster_status from
// configured and running membership maps that it mutates as repair progresses.
type fakeExec struct {
	running            map[string][]string        // podName -> running node names it reports
	configured         map[string][]string        // podName -> disk_nodes + ram_nodes it reports
	quorumQueueMembers map[string]map[string]bool // queue name -> node names holding a replica
	quorumQueueOnline  map[string]map[string]bool // queue name -> online replica node names
	queueGrowOnline    bool                       // whether a successful grow has finished syncing
	calls              []string                   // "<pod>:<command> <args...>" in invocation order
	queueGrowErr       error
	queueGrowOutput    string            // per-queue failures can be reported with exit status 0
	queueGrowStderr    string            // RabbitMQ CLI may report per-queue failures on stderr
	statusOutput       map[string]string // optional raw cluster_status response by pod name
	statusErr          map[string]error  // optional cluster_status exec error by pod name
	postForgetRunning  map[string][]string
	consistent         map[string][]string // optional leader membership, independent of the local cache
	quorumReadErr      error
	quorumReadOutput   string
}

func (f *fakeExec) run(_ context.Context, _ kubernetes.Interface, _ *rest.Config, podName types.NamespacedName,
	_ string, cmd []string, fun func(*bytes.Buffer, *bytes.Buffer) error) error {
	if len(cmd) >= 5 && cmd[0] == "sh" && cmd[1] == "-c" && cmd[3] == "--" {
		cmd = cmd[4:] // Execute the intended CLI after the image PATH adapter.
	}
	if len(cmd) == 0 {
		return errors.New("empty command")
	}
	command := cmd[0]
	args := cmd[1:]
	f.calls = append(f.calls, podName.Name+":"+strings.Join(cmd, " "))
	if command == "rabbitmq-queues" && len(args) >= 1 && args[0] == "grow" && f.queueGrowErr != nil {
		return f.queueGrowErr
	}
	// Model `rabbitmq-queues grow <node> all --errors-only --silent`: a
	// successful command adds the selected node to every existing quorum
	// queue. Per-queue failures are output while the command itself exits zero.
	if command == "rabbitmq-queues" && len(args) >= 3 && args[0] == "grow" && args[2] == "all" {
		if f.queueGrowOutput != "" || f.queueGrowStderr != "" {
			var out bytes.Buffer
			var stderr bytes.Buffer
			out.WriteString(f.queueGrowOutput)
			stderr.WriteString(f.queueGrowStderr)
			return fun(&out, &stderr)
		}
		for queue, members := range f.quorumQueueMembers {
			members[args[1]] = true
			if f.queueGrowOnline {
				f.quorumQueueOnline[queue][args[1]] = true
			}
		}
	}

	// Model cluster metadata changes across all members.
	if command == "rabbitmqctl" && len(args) >= 2 && args[0] == "forget_cluster_node" {
		staleNode := args[1]
		for pod, nodes := range f.running {
			f.running[pod] = withoutNode(nodes, staleNode)
		}
		for pod, nodes := range f.configured {
			f.configured[pod] = withoutNode(nodes, staleNode)
		}
		for pod, nodes := range f.consistent {
			f.consistent[pod] = withoutNode(nodes, staleNode)
		}
		for pod, nodes := range f.postForgetRunning {
			f.running[pod] = append([]string(nil), nodes...)
		}
		for queue := range f.quorumQueueMembers {
			delete(f.quorumQueueMembers[queue], staleNode)
			delete(f.quorumQueueOnline[queue], staleNode)
		}
	}
	if command == "rabbitmqctl" && len(args) >= 1 && args[0] == "join_cluster" {
		joined := node(podName.Name)
		var clusterNodes []string
		for _, nodes := range f.running {
			for _, member := range nodes {
				clusterNodes = appendUnique(clusterNodes, member)
			}
		}
		clusterNodes = appendUnique(clusterNodes, joined)
		for p := range f.running {
			if p == podName.Name {
				continue
			}
			f.running[p] = appendUnique(f.running[p], joined)
			f.configured[p] = appendUnique(f.configured[p], joined)
		}
		f.running[podName.Name] = clusterNodes
		f.configured[podName.Name] = clusterNodes
	}

	if command == "rabbitmqctl" && len(args) > 0 && args[len(args)-1] == "cluster_status" {
		if err := f.statusErr[podName.Name]; err != nil {
			return err
		}
		if raw, ok := f.statusOutput[podName.Name]; ok {
			var out bytes.Buffer
			out.WriteString(raw)
			return fun(&out, &bytes.Buffer{})
		}
		configured := f.configured[podName.Name]
		if configured == nil {
			// Keep simple test fixtures concise while ensuring emitted status has
			// the same configured/running distinction as the RabbitMQ CLI.
			configured = append([]string(nil), f.running[podName.Name]...)
		}
		status := clusterStatusForTest{DiskNodes: configured, RAMNodes: []string{}, RunningNodes: f.running[podName.Name]}
		b, _ := json.Marshal(status)
		var out bytes.Buffer
		out.Write(b)
		return fun(&out, &bytes.Buffer{})
	}
	if command == "rabbitmqctl" && len(args) > 1 && args[0] == "-q" && args[1] == "eval" {
		if len(args) > 2 && strings.Contains(args[2], `QUEUE_CONSENSUS=`) {
			if f.quorumReadErr != nil {
				return f.quorumReadErr
			}
			bracketedGenerator := regexp.MustCompile(`\|\| \{V,N,Expected,Nodes\}<-\[`)
			if !bracketedGenerator.MatchString(args[2]) {
				return errors.New("malformed queue consensus generator")
			}
			output := f.quorumReadOutput
			if output == "" {
				type participant struct {
					Node       string `json:"node"`
					State      string `json:"state"`
					Membership string `json:"membership"`
					Term       int64  `json:"term"`
					Leader     string `json:"leader"`
					Members    int    `json:"members"`
					Error      bool   `json:"error"`
				}
				type result struct {
					Vhost           string        `json:"vhost"`
					Name            string        `json:"name"`
					ExpectedMembers int           `json:"expected_members"`
					Acknowledged    int           `json:"acknowledged"`
					Participants    []participant `json:"participants"`
				}
				queuePattern := regexp.MustCompile(`\{base64:decode\(<<"([A-Za-z0-9+/=]*)">>\),base64:decode\(<<"([A-Za-z0-9+/=]*)">>\),([0-9]+),\[((?:base64:decode\(<<"[A-Za-z0-9+/=]*">>\),?)*)\]\}`)
				nodePattern := regexp.MustCompile(`base64:decode\(<<"([A-Za-z0-9+/=]*)">>\)`)
				matches := queuePattern.FindAllStringSubmatch(args[2], -1)
				queues := make([]result, 0, len(matches))
				for _, match := range matches {
					vhostBytes, _ := base64.StdEncoding.DecodeString(match[1])
					nameBytes, _ := base64.StdEncoding.DecodeString(match[2])
					expected, _ := strconv.Atoi(match[3])
					nodeMatches := nodePattern.FindAllStringSubmatch(match[4], -1)
					nodes := make([]string, 0, len(nodeMatches))
					for _, nodeMatch := range nodeMatches {
						decoded, _ := base64.StdEncoding.DecodeString(nodeMatch[1])
						nodes = append(nodes, string(decoded))
					}
					item := result{Vhost: string(vhostBytes), Name: string(nameBytes), ExpectedMembers: expected, Acknowledged: len(nodes)}
					leader := nodes[len(nodes)-1]
					for _, current := range nodes {
						item.Participants = append(item.Participants, participant{
							Node: current, Membership: "voter", Term: 1,
							Leader: leader, Members: expected,
						})
						if current == leader {
							item.Participants[len(item.Participants)-1].State = "leader"
						} else {
							item.Participants[len(item.Participants)-1].State = "follower"
						}
					}
					queues = append(queues, item)
				}
				encoded, _ := json.Marshal(queues)
				output = "QUEUE_CONSENSUS=" + string(encoded) + "\nok\n"
			}
			return fun(bytes.NewBufferString(output), &bytes.Buffer{})
		}
		members := f.configured[podName.Name]
		if authoritative, ok := f.consistent[podName.Name]; ok {
			members = authoritative
		}
		if members == nil {
			members = f.running[podName.Name]
		}
		b, _ := json.Marshal(members)
		var out bytes.Buffer
		out.WriteString("MEMBERS=" + string(b) + "\nok\n")
		return fun(&out, &bytes.Buffer{})
	}
	return nil
}

func TestFakeExecRejectsMalformedQueueGenerator(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	vhost := base64.StdEncoding.EncodeToString([]byte("/"))
	queue := base64.StdEncoding.EncodeToString([]byte("orders"))
	first := node(testName + "-server-1")
	second := node(testName + "-server-2")
	firstJSON := base64.StdEncoding.EncodeToString([]byte(first))
	secondJSON := base64.StdEncoding.EncodeToString([]byte(second))
	expression := `Results=[Process(V,N,Expected,Nodes) || {V,N,Expected,Nodes}<-` +
		`{base64:decode(<<"` + vhost + `">>),base64:decode(<<"` + queue + `">>),2,[` +
		`base64:decode(<<"` + firstJSON + `">>),base64:decode(<<"` + secondJSON + `">>)]}],` +
		`io:format("QUEUE_CONSENSUS=~ts~n",[rabbit_json:encode(Results)]),ok.`
	cmd := []string{"rabbitmqctl", "-q", "eval", expression}
	if err := fx.run(context.Background(), kubernetesfake.NewSimpleClientset(), nil,
		types.NamespacedName{Name: testName + "-server-1", Namespace: testNS}, "",
		cmd, func(*bytes.Buffer, *bytes.Buffer) error { return nil }); err == nil {
		t.Fatal("malformed unbracketed queue generator must be rejected")
	}
}

func appendUnique(nodes []string, node string) []string {
	for _, current := range nodes {
		if current == node {
			return nodes
		}
	}
	return append(nodes, node)
}

func withoutNode(nodes []string, unwanted string) []string {
	filtered := make([]string, 0, len(nodes))
	for _, current := range nodes {
		if current != unwanted {
			filtered = append(filtered, current)
		}
	}
	return filtered
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

func (f *fakeExec) lastOrderOf(substr string) int {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if strings.Contains(f.calls[i], substr) {
			return i
		}
	}
	return -1
}

func newFakeExecWithQuorumQueues(running map[string][]string) *fakeExec {
	configured := make(map[string][]string, len(running))
	for pod, nodes := range running {
		configured[pod] = append([]string(nil), nodes...)
	}
	return &fakeExec{
		running:    running,
		configured: configured,
		quorumQueueMembers: map[string]map[string]bool{
			"orders": {
				node(testName + "-server-1"): true,
				node(testName + "-server-2"): true,
			},
			"notifications": {
				node(testName + "-server-1"): true,
				node(testName + "-server-2"): true,
			},
		},
		quorumQueueOnline: map[string]map[string]bool{
			"orders": {
				node(testName + "-server-1"): true,
				node(testName + "-server-2"): true,
			},
			"notifications": {
				node(testName + "-server-1"): true,
				node(testName + "-server-2"): true,
			},
		},
		queueGrowOnline:   true,
		statusOutput:      map[string]string{},
		statusErr:         map[string]error{},
		postForgetRunning: map[string][]string{},
	}
}

func modelOfflineStaleSeed(fx *fakeExec) {
	seed := node(testName + "-server-0")
	fx.running[testName+"-server-0"] = []string{seed}
	fx.configured[testName+"-server-0"] = []string{seed}
	for _, ordinal := range []string{"1", "2"} {
		pod := testName + "-server-" + ordinal
		fx.running[pod] = []string{node(testName + "-server-1"), node(testName + "-server-2")}
		fx.configured[pod] = []string{seed, node(testName + "-server-1"), node(testName + "-server-2")}
	}
}

func modelRemovedSeed(fx *fakeExec) {
	modelOfflineStaleSeed(fx)
	for _, ordinal := range []string{"1", "2"} {
		pod := testName + "-server-" + ordinal
		fx.configured[pod] = append([]string(nil), fx.running[pod]...)
	}
}

func assertQuorumQueueMembership(t *testing.T, fx *fakeExec, nodeName string, wantMember bool) {
	t.Helper()
	for queue, members := range fx.quorumQueueMembers {
		if got := members[nodeName]; got != wantMember {
			t.Errorf("quorum queue %q membership for %q = %t, want %t", queue, nodeName, got, wantMember)
		}
	}
}

// clusterPod builds a Running pod with its rabbitmq container ready. serving
// controls the serving label (membership in the client Service) and the PodReady
// condition: a pod serving=true is Ready and in client traffic; serving=false is
// held out (e.g. a replacement the controller is still verifying).
func clusterPod(name string, serving bool) *corev1.Pod {
	podReady := corev1.ConditionFalse
	servingValue := rabbitmqv1beta1.RabbitMQServingFalse
	if serving {
		podReady = corev1.ConditionTrue
		servingValue = rabbitmqv1beta1.RabbitMQServingTrue
	}
	labels := rabbitmqresources.SelectorLabels(testName)
	labels[rabbitmqv1beta1.RabbitMQServingLabel] = servingValue
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels:    labels,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "rabbitmq",
				Ready: true,
			}},
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: podReady},
			},
		},
	}
	return p
}

func replacementPod(requestID string) *corev1.Pod {
	pod := clusterPod(testName+"-server-0", false)
	pod.Spec.Volumes = []corev1.Volume{{
		Name: "persistence",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: testPVCName,
		}},
	}}
	if requestID != "" {
		pod.Annotations = map[string]string{rabbitmqv1beta1.AnnotationRejoinCluster: requestID}
	}
	return pod
}

func replacementPVC(uid string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPVCName,
			Namespace: testNS,
			UID:       types.UID(uid),
		},
	}
}

func managementClientFor(fx *fakeExec) *http.Client {
	return &http.Client{Transport: rabbitMQManagementTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/queues/detailed" || request.URL.Query().Get("page") != "1" ||
			request.URL.Query().Get("page_size") != "500" {
			return nil, fmt.Errorf("unexpected management request %s", request.URL.String())
		}
		type queue struct {
			Name    string   `json:"name"`
			Vhost   string   `json:"vhost"`
			Type    string   `json:"type"`
			Members []string `json:"members"`
			Online  []string `json:"online"`
		}
		queues := make([]queue, 0, len(fx.quorumQueueMembers))
		for name, members := range fx.quorumQueueMembers {
			item := queue{Name: name, Vhost: "/", Type: "quorum"}
			for member := range members {
				item.Members = append(item.Members, member)
			}
			for member := range fx.quorumQueueOnline[name] {
				item.Online = append(item.Online, member)
			}
			queues = append(queues, item)
		}
		body, err := json.Marshal(map[string]interface{}{
			"page": 1, "page_count": 1, "page_size": 500, "item_count": len(queues),
			"filtered_count": len(queues), "items": queues,
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))),
			Header: make(http.Header), Request: request,
		}, nil
	})}
}

func newReconciler(t *testing.T, executor *fakeExec, objs ...client.Object) *rabbitmqcontroller.Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rabbitmqv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testName + "-default-user", Namespace: testNS},
		Data:       map[string][]byte{"username": []byte("operator"), "password": []byte("test-password")},
	}
	objs = append(objs, secret)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1.Pod{}).WithObjects(objs...).Build()
	return &rabbitmqcontroller.Reconciler{
		Client: c, Scheme: scheme, PodCommandExecutor: executor.run, ManagementHTTPClient: managementClientFor(executor),
	}
}

func instanceWith(replicas int32) *rabbitmqv1beta1.RabbitMq {
	return &rabbitmqv1beta1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS},
		Spec: rabbitmqv1beta1.RabbitMqSpec{
			RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{Replicas: ptr.To(replicas)},
		},
		Status: rabbitmqv1beta1.RabbitMqStatus{CurrentVersion: "4.2"},
	}
}

func instanceWithRecovery(replicas int32) *rabbitmqv1beta1.RabbitMq {
	instance := instanceWith(replicas)
	instance.Status.PVCRemediation = map[string]rabbitmqv1beta1.PVCRemediationStatus{
		testPVCName: {
			PVCUID:       oldPVCUID,
			RequestID:    testRequestID,
			StuckNode:    "worker-0",
			ConsentState: rabbitmqv1beta1.PVCRemediationConsentGranted,
		},
	}
	return instance
}

// Post-start rejoin/queue tests begin after the separate pre-start removal
// phase. startup_test.go exercises that phase and its durable boundary.
func instanceAfterMemberRemoval(replicas int32) *rabbitmqv1beta1.RabbitMq {
	instance := instanceWithRecovery(replicas)
	entry := instance.Status.PVCRemediation[testPVCName]
	entry.MemberRemovalState = rabbitmqv1beta1.PVCRemediationMemberRemoved
	entry.ReplacementPVCUID = newPVCUID
	instance.Status.PVCRemediation[testPVCName] = entry
	return instance
}

// podRejoinReady reports whether the controller has returned a pod to client
// traffic (serving label == "true"). Kept under its original name so the many
// call sites read the same; semantics now follow the serving label.
func podRejoinReady(pod *corev1.Pod) bool {
	return pod.Labels[rabbitmqv1beta1.RabbitMQServingLabel] == rabbitmqv1beta1.RabbitMQServingTrue
}

// realClusterStatus42 is a sample of `rabbitmqctl cluster_status --formatter
// json` from RabbitMQ 4.2.9. It includes fields the controller deliberately
// ignores so this exercises parsing through the public rejoin entry point.
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

func TestRejoinAcceptsRabbitMQ42ClusterStatus(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})
	fx.statusOutput[testName+"-server-0"] = realClusterStatus42
	fx.statusOutput[testName+"-server-1"] = realClusterStatus42
	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("RabbitMQ 4.2 cluster status was rejected: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue after queue growth and readiness update")
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), true)
}

func TestRejoinWithoutConsentDoesNotExec(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}

	r := newReconciler(t, fx,
		clusterPod(testName+"-server-0", true),
		clusterPod(testName+"-server-1", true))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceWith(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requeue {
		t.Error("expected no requeue when there is no pending PVC remediation")
	}
	if len(fx.calls) != 0 {
		t.Errorf("expected no exec calls, got %v", fx.calls)
	}
}

func TestRejoinCLICommandsHaveFiniteTimeout(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}
	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true))
	started := time.Now()
	var deadline time.Time
	r.PodCommandExecutor = func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config,
		_ types.NamespacedName, _ string, _ []string, _ func(*bytes.Buffer, *bytes.Buffer) error,
	) error {
		deadline, _ = ctx.Deadline()
		return errors.New("fake command failure")
	}

	_, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("expected failed peer probe to defer rejoin, got: %v", err)
	}
	remaining := time.Until(deadline)
	if deadline.IsZero() || remaining <= 0 || deadline.After(started.Add(3*time.Minute)) {
		t.Fatalf("RabbitMQ CLI execution did not receive a finite timeout: deadline=%v started=%v", deadline, started)
	}
}

// TestRejoinHoldsFreshReplacementOutOfService verifies the serving-label hold:
// a replacement from an older template may still start serving=true, and the
// controller actively flips it to serving=false to keep it out of client traffic
// until it has rejoined. A stale rejoin annotation makes the controller rebind and
// requeue without running the full rejoin, isolating the hold behaviour.
func TestRejoinHoldsFreshReplacementOutOfService(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}

	target := replacementPod("stale-request")
	// Simulate a replacement created before the template started using serving=false.
	target.Labels[rabbitmqv1beta1.RabbitMQServingLabel] = rabbitmqv1beta1.RabbitMQServingTrue
	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		target,
		clusterPod(testName+"-server-1", true))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue after holding the fresh replacement and rebinding its request")
	}
	if len(fx.calls) != 0 {
		t.Fatalf("must not run rejoin CLI before binding the current request, calls=%v", fx.calls)
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if podRejoinReady(got) {
		t.Fatal("expected the controller to hold the fresh replacement out of client traffic (serving=false)")
	}
	if got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != testRequestID {
		t.Fatalf("rejoin annotation = %q, want the consent request ID %q", got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster], testRequestID)
	}
}

func TestRejoinBindsAnnotationToRecordedRequestID(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}

	target := replacementPod("true")
	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		target,
		clusterPod(testName+"-server-1", true))

	instance := instanceAfterMemberRemoval(3)
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue after binding the annotation to the consent request")
	}
	if len(fx.calls) != 0 {
		t.Fatalf("stale annotation must not authorize rejoin, calls=%v", fx.calls)
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != testRequestID {
		t.Fatalf("rejoin annotation = %q, want the consent request ID %q", got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster], testRequestID)
	}
	if podRejoinReady(got) {
		t.Fatal("readiness gate must stay closed until membership is verified")
	}
}

func TestRejoinWaitsForReplacementPVC(t *testing.T) {
	fx := &fakeExec{running: map[string][]string{}}

	r := newReconciler(t, fx, replacementPod(testRequestID))
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue while waiting for replacement PVC")
	}
	if len(fx.calls) != 0 {
		t.Fatalf("must not exec before the replacement PVC exists, calls=%v", fx.calls)
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != testRequestID {
		t.Fatalf("request annotation = %q, want %q", got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster], testRequestID)
	}
	if podRejoinReady(got) {
		t.Fatal("readiness gate must stay closed while waiting for the replacement PVC")
	}
}

func TestRejoinRefusesWithoutHealthyPeer(t *testing.T) {
	// Only the target is present/ready; it cannot safely rejoin without a healthy peer.
	fx := &fakeExec{running: map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
	}}

	r := newReconciler(t, fx, replacementPVC(newPVCUID), replacementPod(testRequestID))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue while deferring")
	}
	if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 {
		t.Errorf("must not change membership without a healthy peer, calls=%v", fx.calls)
	}
	if fx.orderOf("rabbitmq-queues grow") >= 0 {
		t.Errorf("must not grow queue replicas before a safe rejoin, calls=%v", fx.calls)
	}
}

func TestRejoinAfterPersistedMemberRemoval(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")}, // replacement is standalone
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	// This is the actual deleted-seed state: server-0 remains configured in
	// the surviving cluster but is absent from its running membership.
	modelRemovedSeed(fx)

	target := replacementPod(testRequestID)
	r := newReconciler(t, fx, target,
		replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))

	instance := instanceAfterMemberRemoval(3)
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue after performing a rejoin")
	}

	// Member removal was persisted before this broker started. Do not repeat it.
	if fx.orderOf("forget_cluster_node") >= 0 {
		t.Errorf("must not forget a member after allowing startup, calls=%v", fx.calls)
	}
	if fx.orderOf(testName+"-server-0:rabbitmqctl join_cluster "+node(testName+"-server-1")) < 0 &&
		fx.orderOf(testName+"-server-0:rabbitmqctl join_cluster "+node(testName+"-server-2")) < 0 {
		t.Errorf("expected join_cluster on target, calls=%v", fx.calls)
	}
	for _, unwanted := range []string{"stop_app", "reset", "start_app"} {
		if fx.orderOf(unwanted) != -1 {
			t.Errorf("did not expect %q on 4.1+, calls=%v", unwanted, fx.calls)
		}
	}
	queueGrowth := testName + "-server-0:rabbitmq-queues grow " + node(testName+"-server-0") + " all --errors-only --silent"
	if fx.orderOf(queueGrowth) < 0 {
		t.Errorf("expected quorum queue replicas to grow on the replacement node, calls=%v", fx.calls)
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), true)
	if fx.lastOrderOf("rabbitmqctl --formatter json cluster_status") >= fx.orderOf(queueGrowth) {
		t.Errorf("queue growth must follow cluster membership verification, calls=%v", fx.calls)
	}
	if instance.Status.PVCRemediation[testPVCName].QueueReplicaState != rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Error("expected successful queue growth to be recorded in PVC remediation status")
	}

	// Annotation cleared after successful verify.
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster]; ok {
		t.Error("expected rejoin annotation to be cleared")
	}
	if !podRejoinReady(got) {
		t.Error("expected readiness gate to open after verifying membership")
	}
}

func TestReconcilePersistsQuorumQueueGrowthStatus(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelRemovedSeed(fx)

	scheme := runtime.NewScheme()
	for _, addToScheme := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		policyv1.AddToScheme,
		rbacv1.AddToScheme,
		configv1.AddToScheme,
		rabbitmqv1beta1.AddToScheme,
	} {
		if err := addToScheme(scheme); err != nil {
			t.Fatal(err)
		}
	}

	instance := instanceAfterMemberRemoval(3)
	instance.Generation = 7
	instance.Status.ObservedGeneration = 6
	instance.Spec.ContainerImage = "rabbitmq:4.2"
	instance.UID = types.UID("rabbitmq-uid")
	instance.Finalizers = []string{"openstack.org/rabbitmq"}
	instance.Status.OldCRCleaned = "True"

	objects := []client.Object{
		instance,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true),
		&configv1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.NetworkStatus{
				ClusterNetwork: []configv1.ClusterNetworkEntry{{CIDR: "10.128.0.0/14"}},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-config-v1", Namespace: "kube-system"},
			Data:       map[string]string{"install-config": "{}"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testName + "-default-user", Namespace: testNS},
			Data:       map[string][]byte{"username": []byte("operator"), "password": []byte("test-password")},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&rabbitmqv1beta1.RabbitMq{}, &corev1.Pod{}).
		WithObjects(objects...).Build()
	// The Pod watch can overtake the RabbitMq status cache. Recovery/admission
	// must use the API reader's durable record, not this stale cached view.
	cached := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := delegate.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if cr, ok := obj.(*rabbitmqv1beta1.RabbitMq); ok {
				cr.Status.PVCRemediation = nil
			}
			return nil
		},
	})
	r := &rabbitmqcontroller.Reconciler{
		Client:               cached,
		APIReader:            c,
		Kclient:              kubernetesfake.NewSimpleClientset(),
		Scheme:               scheme,
		PodCommandExecutor:   fx.run,
		ManagementHTTPClient: managementClientFor(fx),
	}

	completed := false
	for attempt := 0; attempt < 30; attempt++ {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
		})
		if err != nil {
			t.Fatalf("reconcile attempt %d failed before rejoin: %v", attempt+1, err)
		}
		if fx.orderOf("rabbitmq-queues grow") >= 0 {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatalf("full RabbitMq reconcile did not reach queue growth, calls=%v", fx.calls)
	}

	persisted := &rabbitmqv1beta1.RabbitMq{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.PVCRemediation[testPVCName].QueueReplicaState != rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Fatal("deferred reconcile status patch did not persist queueReplicaState=Grown")
	}
	if persisted.Status.ObservedGeneration != persisted.Generation {
		t.Fatalf("rejoin deferral left observedGeneration=%d behind generation=%d", persisted.Status.ObservedGeneration, persisted.Generation)
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), true)
}

func TestRejoinKeepsReadinessClosedUntilQuorumReplicasAreOnline(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelOfflineStaleSeed(fx)
	fx.queueGrowOnline = false

	target := replacementPod(testRequestID)
	r := newReconciler(t, fx, target,
		replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))
	instance := instanceAfterMemberRemoval(3)

	if _, err := r.ReconcileNodeRejoin(context.Background(), instance); err == nil || !strings.Contains(err.Error(), "not online yet") {
		t.Fatalf("expected rejoin to wait for online queue replicas, got %v", err)
	}
	if instance.Status.PVCRemediation[testPVCName].QueueReplicaState != rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Fatal("successful growth command should be recorded while replicas synchronize")
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if podRejoinReady(got) {
		t.Fatal("readiness gate opened before all quorum replicas were online")
	}

	for queue := range fx.quorumQueueMembers {
		fx.quorumQueueOnline[queue][node(target.Name)] = true
	}
	if requeue, err := r.ReconcileNodeRejoin(context.Background(), instance); err != nil || !requeue {
		t.Fatalf("rejoin did not finish after replicas became online: requeue=%v err=%v", requeue, err)
	}
	if fx.orderOf("rabbitmq-queues grow") != fx.lastOrderOf("rabbitmq-queues grow") {
		t.Fatalf("growth command should not repeat while waiting for replica sync, calls=%v", fx.calls)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if !podRejoinReady(got) {
		t.Fatal("readiness gate should open after every quorum replica is online")
	}
}

func TestRejoinGrowsQuorumQueuesCreatedDuringRecovery(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelOfflineStaleSeed(fx)
	fx.queueGrowOnline = false

	target := replacementPod(testRequestID)
	r := newReconciler(t, fx, target,
		replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))
	instance := instanceAfterMemberRemoval(3)

	if _, err := r.ReconcileNodeRejoin(context.Background(), instance); err == nil || !strings.Contains(err.Error(), "not online yet") {
		t.Fatalf("expected rejoin to wait for initial replicas to sync, got %v", err)
	}
	if instance.Status.PVCRemediation[testPVCName].QueueReplicaState != rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Fatal("successful initial growth command should be recorded while replicas synchronize")
	}

	// A queue declared while the replacement pod is held unready has no member
	// on that node, even though the initial grow command already completed.
	fx.quorumQueueMembers["late-queue"] = map[string]bool{
		node(testName + "-server-1"): true,
		node(testName + "-server-2"): true,
	}
	fx.quorumQueueOnline["late-queue"] = map[string]bool{
		node(testName + "-server-1"): true,
		node(testName + "-server-2"): true,
	}
	fx.queueGrowOnline = true

	if requeue, err := r.ReconcileNodeRejoin(context.Background(), instance); err != nil || !requeue {
		t.Fatalf("rejoin did not grow a queue created during recovery: requeue=%v err=%v", requeue, err)
	}
	assertQuorumQueueMembership(t, fx, node(target.Name), true)
	if !fx.quorumQueueMembers["late-queue"][node(target.Name)] || !fx.quorumQueueOnline["late-queue"][node(target.Name)] {
		t.Fatal("late quorum queue did not gain an online replacement replica")
	}
	growCalls := 0
	for _, call := range fx.calls {
		if strings.Contains(call, "rabbitmq-queues grow") {
			growCalls++
		}
	}
	if growCalls != 2 {
		t.Fatalf("growth should run once initially and once for the late queue, got %d calls: %v", growCalls, fx.calls)
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if !podRejoinReady(got) {
		t.Fatal("readiness gate should open after late queue replica is online")
	}
}

func TestRejoinRetriesFailedQuorumQueueGrowth(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelOfflineStaleSeed(fx)
	fx.queueGrowErr = errors.New("queue quorum unavailable")

	target := replacementPod(testRequestID)
	r := newReconciler(t, fx, target,
		replacementPVC(newPVCUID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))
	instance := instanceAfterMemberRemoval(3)

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err == nil {
		t.Fatal("expected queue growth failure to be returned for retry")
	}
	if !requeue {
		t.Fatal("expected requeue after queue growth failure")
	}
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
		t.Fatal(err)
	}
	if podRejoinReady(got) {
		t.Fatal("readiness gate must remain closed until quorum queue growth succeeds")
	}
	if got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != testRequestID {
		t.Fatal("request annotation must remain until queue growth succeeds")
	}
	if instance.Status.PVCRemediation[testPVCName].QueueReplicaState == rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Fatal("failed queue growth must not be recorded as complete")
	}
	if fx.orderOf("rabbitmq-queues grow") < 0 {
		t.Fatalf("expected queue growth attempt, calls=%v", fx.calls)
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), false)

	fx.queueGrowErr = nil
	requeue, err = r.ReconcileNodeRejoin(context.Background(), instance)
	if err != nil {
		t.Fatalf("queue growth retry failed: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue after successful recovery")
	}
	if fx.orderOf("rabbitmq-queues grow") == fx.lastOrderOf("rabbitmq-queues grow") {
		t.Fatal("expected a second queue growth attempt after the first failed")
	}
	if strings.Count(strings.Join(fx.calls, "\n"), "rabbitmqctl join_cluster") != 1 {
		t.Fatalf("retry should continue the existing cluster membership without joining again, calls=%v", fx.calls)
	}
	if instance.Status.PVCRemediation[testPVCName].QueueReplicaState != rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
		t.Fatal("expected successful queue growth to be recorded")
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), true)
}

func TestRejoinKeepsGateClosedOnPerQueueGrowErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		stdout string
		stderr string
	}{
		{name: "stdout", stdout: "orders\t/orders\terror: quorum unavailable"},
		{name: "stderr", stderr: "orders\t/orders\terror: quorum unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-0": {node(testName + "-server-0")},
				testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
			})
			modelOfflineStaleSeed(fx)
			fx.queueGrowOutput = test.stdout
			fx.queueGrowStderr = test.stderr

			target := replacementPod(testRequestID)
			r := newReconciler(t, fx, target,
				replacementPVC(newPVCUID),
				clusterPod(testName+"-server-1", true),
				clusterPod(testName+"-server-2", true))
			instance := instanceAfterMemberRemoval(3)

			requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
			if err == nil || !strings.Contains(err.Error(), "per-queue errors") {
				t.Fatalf("expected per-queue grow errors to fail rejoin, got err=%v", err)
			}
			if !requeue {
				t.Fatal("expected requeue after per-queue grow errors")
			}
			if fx.orderOf("rabbitmq-queues grow "+node(testName+"-server-0")+" all --errors-only --silent") < 0 {
				t.Fatalf("expected errors-only silent queue growth command, calls=%v", fx.calls)
			}
			assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), false)
			if instance.Status.PVCRemediation[testPVCName].QueueReplicaState == rabbitmqv1beta1.PVCRemediationQueueReplicasGrown {
				t.Fatal("per-queue errors must not be recorded as successful growth")
			}
			got := &corev1.Pod{}
			if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: target.Name}, got); err != nil {
				t.Fatal(err)
			}
			if podRejoinReady(got) {
				t.Fatal("readiness gate must remain closed until every queue replica grows")
			}
			if got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster] != testRequestID {
				t.Fatal("request annotation must remain until every queue replica grows")
			}
		})
	}
}

func TestRejoinAlreadyMemberVerifiesAndClears(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0"), node(testName + "-server-1")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1")},
	})

	target := replacementPod(testRequestID)
	r := newReconciler(t, fx, replacementPVC(newPVCUID), target, clusterPod(testName+"-server-1", true))

	instance := instanceAfterMemberRemoval(3)
	_, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 {
		t.Errorf("must not change membership when target already agrees with cluster, calls=%v", fx.calls)
	}
	queueGrowth := testName + "-server-0:rabbitmq-queues grow " + node(testName+"-server-0") + " all --errors-only --silent"
	if fx.orderOf(queueGrowth) < 0 {
		t.Errorf("expected queue replicas to grow even when cluster membership was already restored, calls=%v", fx.calls)
	}
	assertQuorumQueueMembership(t, fx, node(testName+"-server-0"), true)
	got := &corev1.Pod{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Annotations[rabbitmqv1beta1.AnnotationRejoinCluster]; ok {
		t.Error("expected annotation cleared when already a member")
	}
	if !podRejoinReady(got) {
		t.Error("expected readiness gate to open after verifying existing membership")
	}

	// The remediation record remains until the main PVC handshake observes all
	// replicas again. A verified pod must not be reset or grown a second time.
	callCount := len(fx.calls)
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err != nil {
		t.Fatalf("unexpected error during pending-handshake retry: %v", err)
	}
	if requeue {
		t.Error("expected the verified rejoin to yield to the PVC handshake")
	}
	if len(fx.calls) != callCount {
		t.Errorf("verified node should not repeat RabbitMQ commands, calls=%v", fx.calls[callCount:])
	}
}

func TestRejoinRefusesNonStandaloneTarget(t *testing.T) {
	// Target reports a multi-node view: it still holds real data, must not reset.
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0"), node(testName + "-server-2")},
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
	})

	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true))

	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Error("expected requeue while deferring")
	}
	if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 {
		t.Errorf("must not change membership when target is not standalone, calls=%v", fx.calls)
	}
	if fx.orderOf("rabbitmq-queues grow") >= 0 {
		t.Errorf("must not grow queue replicas when target membership is inconsistent, calls=%v", fx.calls)
	}
}

func TestRejoinDoesNotForgetTargetThatAnchorReportsRunning(t *testing.T) {
	// A conflicting target view is not evidence that the target is stale. The
	// anchor reports it as running, while the target sees only itself.
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-0"), node(testName + "-server-1"), node(testName + "-server-2")},
	})

	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue while membership views disagree")
	}
	if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 {
		t.Errorf("must not mutate membership when anchor reports target running, calls=%v", fx.calls)
	}
	if fx.orderOf("rabbitmq-queues grow") >= 0 {
		t.Errorf("must not grow queue replicas before membership is verified, calls=%v", fx.calls)
	}
}

func TestRejoinDefersIfAnchorLosesQuorumAfterForget(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelOfflineStaleSeed(fx)
	// Simulate the anchor's view changing between the pre-forget and
	// post-forget checks. Recovery must stop instead of joining through a
	// survivor that no longer has quorum.
	fx.postForgetRunning[testName+"-server-1"] = []string{node(testName + "-server-1")}

	instance := rawRecovery()
	r := newReconciler(t, fx,
		instance,
		replacementPVC(newPVCUID),
		gatedReplacement(),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true))
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instance)
	if err == nil {
		t.Fatal("expected an error after survivor quorum changed")
	}
	if !requeue {
		t.Fatal("expected requeue after anchor quorum changed")
	}
	if fx.orderOf("forget_cluster_node") < 0 {
		t.Fatalf("expected attempt to forget the offline stale member, calls=%v", fx.calls)
	}
	if fx.orderOf("join_cluster") >= 0 || fx.orderOf("rabbitmq-queues grow") >= 0 {
		t.Errorf("must not continue after the anchor loses quorum, calls=%v", fx.calls)
	}
}

func TestRejoinFailsClosedOnAmbiguousTargetStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    string
		statusErr error
	}{
		{name: "missing configured membership", status: `{"running_nodes":["` + node(testName+"-server-0") + `"]}`},
		{name: "running member not configured", status: `{"disk_nodes":["` + node(testName+"-server-1") + `"],"ram_nodes":[],"running_nodes":["` + node(testName+"-server-0") + `"]}`},
		{name: "no running members", status: `{"disk_nodes":["` + node(testName+"-server-0") + `"],"ram_nodes":[],"running_nodes":[]}`},
		{name: "status command error", statusErr: errors.New("cluster status unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-0": {node(testName + "-server-0")},
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			fx.statusOutput[testName+"-server-0"] = test.status
			fx.statusErr[testName+"-server-0"] = test.statusErr

			r := newReconciler(t, fx,
				replacementPVC(newPVCUID),
				replacementPod(testRequestID),
				clusterPod(testName+"-server-1", true),
				clusterPod(testName+"-server-2", true))
			requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
			if err == nil {
				t.Fatal("expected ambiguous target status to fail closed")
			}
			if !requeue {
				t.Fatal("expected requeue after target status failure")
			}
			if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 || fx.orderOf("rabbitmq-queues grow") >= 0 {
				t.Errorf("must not mutate membership or queues with ambiguous status, calls=%v", fx.calls)
			}
		})
	}
}

func TestRejoinDefersOnAmbiguousAnchorStatus(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	fx.statusOutput[testName+"-server-1"] = `{"running_nodes":["` + node(testName+"-server-1") + `","` + node(testName+"-server-2") + `"]}`

	r := newReconciler(t, fx,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true))
	requeue, err := r.ReconcileNodeRejoin(context.Background(), instanceAfterMemberRemoval(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !requeue {
		t.Fatal("expected requeue without a trustworthy quorum anchor")
	}
	if fx.orderOf("forget_cluster_node") >= 0 || fx.orderOf("join_cluster") >= 0 || fx.orderOf("rabbitmq-queues grow") >= 0 {
		t.Errorf("must not mutate membership or queues without complete anchor status, calls=%v", fx.calls)
	}
}
