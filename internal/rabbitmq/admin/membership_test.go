/*
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

package admin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// recordedCommand is one CLI invocation a fakeExec was asked to run.
type recordedCommand struct {
	pod       types.NamespacedName
	container string
	command   []string
	deadline  time.Time
}

// fakeExec answers CLI commands from a canned script instead of running them,
// and records what it was asked for.
type fakeExec struct {
	// replies is matched against the command joined by spaces: the first key
	// that appears in it wins.
	replies map[string]string
	// replyFunc, when set, answers instead of replies and can give a
	// different answer to each call.
	replyFunc func(command []string) string
	err       error
	calls     []recordedCommand
}

func (f *fakeExec) run(
	ctx context.Context,
	_ kubernetes.Interface,
	_ *rest.Config,
	pod types.NamespacedName,
	container string,
	command []string,
	callback func(*bytes.Buffer, *bytes.Buffer) error,
) error {
	deadline, _ := ctx.Deadline()
	f.calls = append(f.calls, recordedCommand{
		pod: pod, container: container, command: command, deadline: deadline})
	if f.err != nil {
		return f.err
	}
	if f.replyFunc != nil {
		return callback(bytes.NewBufferString(f.replyFunc(command)), &bytes.Buffer{})
	}
	joined := strings.Join(command, " ")
	for key, reply := range f.replies {
		if strings.Contains(joined, key) {
			return callback(bytes.NewBufferString(reply), &bytes.Buffer{})
		}
	}
	return callback(&bytes.Buffer{}, &bytes.Buffer{})
}

func execClient(f *fakeExec) *Client {
	return NewWithCreds(testInstance(), Creds{Username: "u", Password: "p"}).
		WithExec(nil, nil, f.run)
}

func brokerPod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "openstack"}}
}

// The node name has to match what RabbitMQ calls itself, which is the pod's
// fully qualified headless-service hostname and not just the pod name.
func TestNodeName(t *testing.T) {
	got := NodeName(testInstance(), "rabbitmq-server-0")
	want := "rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack"
	if got != want {
		t.Errorf("NodeName = %q, want %q", got, want)
	}
}

func TestCommandRunsInTheBrokerContainerWithADeadline(t *testing.T) {
	f := &fakeExec{replies: map[string]string{"cluster_status": "{}"}}
	started := time.Now()

	out, err := execClient(f).Ctl(context.Background(),
		types.NamespacedName{Namespace: "openstack", Name: "rabbitmq-server-0"},
		"--formatter", "json", "cluster_status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "{}" {
		t.Errorf("stdout = %q, want {}", out)
	}
	if len(f.calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(f.calls))
	}
	call := f.calls[0]
	if call.container != ContainerName {
		t.Errorf("container = %q, want %q", call.container, ContainerName)
	}
	if call.command[0] != "rabbitmqctl" {
		t.Errorf("command = %v, want it to start with rabbitmqctl", call.command)
	}
	// A broker that accepts the exec and then never answers must not hold the
	// caller open, so every command carries a deadline.
	if call.deadline.IsZero() || call.deadline.Before(started) ||
		call.deadline.After(started.Add(3*time.Minute)) {
		t.Errorf("deadline = %v, want a finite one within three minutes of %v", call.deadline, started)
	}
}

// A failing RabbitMQ command explains itself on stdout or stderr, so that text
// has to reach the caller or the error says nothing useful.
func TestCommandErrorCarriesWhatTheCommandPrinted(t *testing.T) {
	f := &fakeExec{err: errors.New("exit status 69"), replies: map[string]string{}}
	_, err := execClient(f).Ctl(context.Background(),
		types.NamespacedName{Name: "rabbitmq-server-0"}, "cluster_status")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "exit status 69") {
		t.Errorf("error lost the underlying failure: %v", err)
	}
}

func TestCommandWithoutAnyWayToRunFails(t *testing.T) {
	client := NewWithCreds(testInstance(), Creds{Username: "u", Password: "p"})
	_, err := client.Ctl(context.Background(), types.NamespacedName{Name: "p"}, "cluster_status")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "without exec") {
		t.Errorf("error does not explain the cause: %v", err)
	}
}

const threeNodeStatus = `{"disk_nodes":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack",
	"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack",
	"rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],
	"ram_nodes":[],
	"running_nodes":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack",
	"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]}`

// A broker that is down stays a configured member until it is forgotten, so
// the two sets have to stay distinct.
func TestMembershipSeparatesConfiguredFromRunning(t *testing.T) {
	f := &fakeExec{replies: map[string]string{"cluster_status": threeNodeStatus}}

	membership, err := execClient(f).Membership(context.Background(), brokerPod("rabbitmq-server-0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(membership.Configured) != 3 {
		t.Errorf("configured = %v, want three members", membership.Configured)
	}
	if len(membership.Running) != 2 {
		t.Errorf("running = %v, want two members", membership.Running)
	}
	if !membership.Configured["rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"] {
		t.Error("a member that is down was dropped from the configured set")
	}
}

// Asking a broker about the cluster and getting back a view it is not part of
// means the answer describes something other than what was asked.
func TestMembershipRequiresThePeerToReportItself(t *testing.T) {
	f := &fakeExec{replies: map[string]string{"cluster_status": threeNodeStatus}}
	_, err := execClient(f).Membership(context.Background(), brokerPod("rabbitmq-server-2"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "does not report its own node") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMembershipRejectsUnusableStatus(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{"not json", `not json at all`},
		{"missing running_nodes", `{"disk_nodes":["rabbit@a"],"ram_nodes":[]}`},
		{"empty configured name", `{"disk_nodes":["rabbit@a",""],"ram_nodes":[],"running_nodes":["rabbit@a"]}`},
		{"no configured nodes", `{"disk_nodes":[],"ram_nodes":[],"running_nodes":[]}`},
		{"running node is not a member", `{"disk_nodes":["rabbit@a"],"ram_nodes":[],"running_nodes":["rabbit@b"]}`},
		{"running node listed twice", `{"disk_nodes":["rabbit@a"],"ram_nodes":[],"running_nodes":["rabbit@a","rabbit@a"]}`},
		{"nothing running", `{"disk_nodes":["rabbit@a"],"ram_nodes":[],"running_nodes":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeExec{replies: map[string]string{"cluster_status": tc.status}}
			if _, err := execClient(f).Membership(context.Background(),
				brokerPod("rabbitmq-server-0")); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestConsistentMembers(t *testing.T) {
	reply := `MEMBERS=["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack",` +
		`"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]`
	f := &fakeExec{replies: map[string]string{"list_consistent_members": reply}}

	members, err := execClient(f).ConsistentMembers(context.Background(), brokerPod("rabbitmq-server-0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("members = %v, want two", members)
	}
}

func TestConsistentMembersRejectsUnusableAnswers(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"no answer line", `something else entirely`},
		{"not json", `MEMBERS=nope`},
		{"duplicate member", `MEMBERS=["rabbit@a","rabbit@a"]`},
		{"empty member", `MEMBERS=["rabbit@a",""]`},
		{"peer is absent from its own answer", `MEMBERS=["rabbit@elsewhere"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeExec{replies: map[string]string{"list_consistent_members": tc.reply}}
			if _, err := execClient(f).ConsistentMembers(context.Background(),
				brokerPod("rabbitmq-server-0")); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestForgetNode(t *testing.T) {
	f := &fakeExec{replies: map[string]string{}}
	if err := execClient(f).ForgetNode(context.Background(),
		brokerPod("rabbitmq-server-1"), "rabbit@gone"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(f.calls))
	}
	joined := strings.Join(f.calls[0].command, " ")
	if !strings.Contains(joined, "forget_cluster_node rabbit@gone") {
		t.Errorf("command = %q", joined)
	}
	// The command has to run on a surviving broker, not the one being removed.
	if f.calls[0].pod.Name != "rabbitmq-server-1" {
		t.Errorf("ran against %q, want the surviving peer", f.calls[0].pod.Name)
	}
}
