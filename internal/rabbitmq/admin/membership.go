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

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	corev1 "k8s.io/api/core/v1"
)

// NodeName is the Erlang node name RabbitMQ uses for a pod, for example
// rabbit@cluster-server-0.cluster-nodes.openstack. It is the same hostname the
// pod certificate is issued for, so that the CLI, the management API and TLS
// all agree on what a broker is called.
func NodeName(instance *rabbitmqv1.RabbitMq, podName string) string {
	return "rabbit@" + rabbitmq.ManagementPodServerName(podName, instance.Name, instance.Namespace)
}

// NodeName is NodeName for the cluster this client was built for.
func (c *Client) NodeName(podName string) string {
	return NodeName(c.instance, podName)
}

// status is the subset of `rabbitmqctl --formatter json cluster_status`
// output that we rely on.
type status struct {
	DiskNodes    []string `json:"disk_nodes"`
	RAMNodes     []string `json:"ram_nodes"`
	RunningNodes []string `json:"running_nodes"`
}

// Membership distinguishes configured members from nodes that are
// currently online. RabbitMQ keeps an offline node in disk_nodes/ram_nodes
// until it is explicitly forgotten.
type Membership struct {
	Configured map[string]bool
	Running    map[string]bool
}

func (s status) membership() (Membership, error) {
	if s.DiskNodes == nil || s.RAMNodes == nil || s.RunningNodes == nil {
		return Membership{}, fmt.Errorf("cluster_status is missing disk_nodes, ram_nodes, or running_nodes")
	}
	membership := Membership{
		Configured: make(map[string]bool, len(s.DiskNodes)+len(s.RAMNodes)),
		Running:    make(map[string]bool, len(s.RunningNodes)),
	}
	for _, node := range append(append([]string{}, s.DiskNodes...), s.RAMNodes...) {
		if node == "" {
			return Membership{}, fmt.Errorf("cluster_status contains an empty configured node name")
		}
		membership.Configured[node] = true
	}
	if len(membership.Configured) == 0 {
		return Membership{}, fmt.Errorf("cluster_status contains no configured nodes")
	}
	for _, node := range s.RunningNodes {
		if node == "" {
			return Membership{}, fmt.Errorf("cluster_status contains an empty running node name")
		}
		if !membership.Configured[node] {
			return Membership{}, fmt.Errorf("running node %q is not listed in disk_nodes or ram_nodes", node)
		}
		if membership.Running[node] {
			return Membership{}, fmt.Errorf("cluster_status lists running node %q more than once", node)
		}
		membership.Running[node] = true
	}
	if len(membership.Running) == 0 {
		return Membership{}, fmt.Errorf("cluster_status contains no running nodes")
	}
	return membership, nil
}

// Membership returns which brokers the cluster counts as members, and which of
// those it reports as running, as read from pod with `rabbitmqctl
// cluster_status`.
//
// The answer is rejected if it does not report pod's own node as running: a
// broker that cannot see itself is not a source worth reporting from.
func (c *Client) Membership(ctx context.Context, pod *corev1.Pod) (Membership, error) {
	out, err := c.Ctl(ctx, podKey(pod),
		"--formatter", "json", "cluster_status")
	if err != nil {
		return Membership{}, err
	}
	var status status
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return Membership{}, fmt.Errorf("parsing cluster_status from %s: %w", pod.Name, err)
	}
	membership, err := status.membership()
	if err != nil {
		return Membership{}, fmt.Errorf("parsing cluster_status from %s: %w", pod.Name, err)
	}
	podNode := c.NodeName(pod.Name)
	if !membership.Running[podNode] {
		return Membership{}, fmt.Errorf("cluster_status from %s does not report its own node %s as running", pod.Name, podNode)
	}
	return membership, nil
}

// ConsistentMembers asks the cluster for its agreed member list instead of
// reading the per-broker cache that `rabbitmqctl cluster_status` reports from.
//
// The two differ when confirming a removal. A node absent from one broker's
// cached view may only be unrefreshed there, which is indistinguishable from a
// node that has been forgotten cluster-wide. Only the agreed list separates
// the two cases; using the cached one can report a member as removed while
// other brokers still count it.
func (c *Client) ConsistentMembers(ctx context.Context, peer *corev1.Pod) (map[string]bool, error) {
	out, err := c.Ctl(ctx, podKey(peer), "-q", "eval",
		`io:format("MEMBERS=~ts~n", [rabbit_json:encode([atom_to_binary(N, utf8) || N <- rabbit_nodes:list_consistent_members()])]), ok.`)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "MEMBERS=") {
			continue
		}
		var names []string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "MEMBERS=")), &names); err != nil {
			return nil, err
		}
		members := make(map[string]bool, len(names))
		for _, name := range names {
			if name == "" || members[name] {
				return nil, fmt.Errorf("ambiguous authoritative RabbitMQ membership")
			}
			members[name] = true
		}
		if !members[c.NodeName(peer.Name)] {
			return nil, fmt.Errorf("authoritative membership excludes its queried peer")
		}
		return members, nil
	}
	return nil, fmt.Errorf("missing authoritative RabbitMQ membership response")
}
