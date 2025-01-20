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
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
	// +kubebuilder:validation:Optional
	// TopologySpreadConstraints exposes topologySpreadConstraints that are
	// applied to the StatefulSet
	TopologySpreadConstraints *[]corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// Affinity exposes PodAffinity, PodAntiaffinity and NodeAffinity overrides
	// that are applied to StatefulSet/Deployments
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	//TODO: We could add NodeSelector here as it belongs to the same APIGroup
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Topology is the Schema for the topologies API
type Topology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec   TopologySpec   `json:"spec,omitempty"`
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

// ValidateTopologyNamespace - returns a field.Error when the Service
// references a Topoology deployed on a different namespace
func ValidateTopologyNamespace(refNs string, basePath field.Path, validNs string) (*field.Error) {
	if refNs != "" &&  refNs != validNs {
		topologyNamespace := basePath.Child("topology").Key("namespace")
		return field.Invalid(topologyNamespace, "namespace", "Customizing namespace field is not supported")
	}
	return nil
}
