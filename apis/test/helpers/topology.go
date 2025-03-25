/*
Copyright 2025 Red Hat
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

package helpers

import (
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	t "github.com/onsi/gomega"
)

// CreateTopology - Creates a Topology CR based on the spec passed as input
func (tc *TestHelper) CreateTopology(topology types.NamespacedName, spec map[string]interface{}) (client.Object, topologyv1.TopoRef) {
	raw := map[string]interface{}{
		"apiVersion": "topology.openstack.org/v1beta1",
		"kind":       "Topology",
		"metadata": map[string]interface{}{
			"name":      topology.Name,
			"namespace": topology.Namespace,
		},
		"spec": spec,
	}
	// other than creating the topology based on the raw spec, we return the
	// TopoRef that can be referenced
	topologyRef := topologyv1.TopoRef{
		Name:      topology.Name,
		Namespace: topology.Namespace,
	}
	return tc.CreateUnstructured(raw), topologyRef
}

// GetTopology - Returns the referenced Topology
func (tc *TestHelper) GetTopology(name types.NamespacedName) *topologyv1.Topology {
	instance := &topologyv1.Topology{}
	t.Eventually(func(g t.Gomega) {
		g.Expect(tc.K8sClient.Get(tc.Ctx, name, instance)).Should(t.Succeed())
	}, tc.Timeout, tc.Interval).Should(t.Succeed())
	return instance
}
