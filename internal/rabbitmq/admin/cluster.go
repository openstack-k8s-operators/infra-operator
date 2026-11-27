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
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// NodeStatus is one broker as the cluster sees it.
//
// Running says whether that broker is up. A broker that is down still belongs
// to the cluster, and still counts when working out whether a majority is
// present.
type NodeStatus struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

// Nodes returns the brokers in the cluster, as reported by the broker in pod.
//
// The answer is checked before it is returned. A broker with no name, or the
// same broker listed twice, means the list cannot be counted reliably, so this
// returns an error rather than handing back numbers the caller would trust.
func (c *Client) Nodes(ctx context.Context, pod *corev1.Pod) ([]NodeStatus, error) {
	var nodes []NodeStatus
	if err := c.get(ctx, pod, "/api/nodes?columns=name,running", requestTimeout, &nodes); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.Name == "" {
			return nil, fmt.Errorf("management API returned a node without a name")
		}
		if seen[node.Name] {
			return nil, fmt.Errorf("management API returned duplicate node %q", node.Name)
		}
		seen[node.Name] = true
	}
	return nodes, nil
}

// RunningNodes picks out the brokers that are up.
func RunningNodes(nodes []NodeStatus) map[string]bool {
	running := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.Running {
			running[node.Name] = true
		}
	}
	return running
}
