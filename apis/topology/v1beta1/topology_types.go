/*
Copyright 2024.

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

package v1beta1

import (
	"context"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
	// +kubebuilder:validation:Optional
	// APITopologySpreadConstraint exposes topologySpreadConstraint that are
	// applied to the StatefulSet
	TopologySpreadConstraint *[]corev1.TopologySpreadConstraint `json:"topologySpreadConstraint,omitempty"`

	// APIAffinity exposes PodAffinity and PodAntiaffinity overrides that are applied
	// to StatefulSet/Deployments
	// +optional
	APIAffinity *affinity.OverrideSpec `json:"affinity,omitempty"`

	//TODO: We could add NodeSelector here as it belongs to the same APIGroup
}

// TopologyStatus defines the observed state of Topology
type TopologyStatus struct {

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the opentack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Topology is the Schema for the topologies API
type Topology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TopologySpec   `json:"spec,omitempty"`
	Status TopologyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TopologyList contains a list of Topology
type TopologyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Topology `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Topology{}, &TopologyList{})
}

// GetTopologyByName - a function exposed to the service operators
// that need to retrieve the referenced topology by name
func GetTopologyByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*Topology, string, error) {

	topology := &Topology{}
	var hash string = ""

	err := h.GetClient().Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, topology)
	if err != nil {
		return topology, "", err
	}
	hash, err = util.ObjectHash(topology.Spec)
	if err != nil {
		return topology, "", err
	}
	return topology, hash, nil
}
