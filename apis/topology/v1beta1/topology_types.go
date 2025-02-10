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
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	Spec              TopologySpec `json:"spec,omitempty"`
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

// TopoRef - it models a Topology reference and it can be included in the
// service operators API. It is used to retrieve the referenced Topology
type TopoRef struct {
	// +kubebuilder:validation:Optional
	// Name - The Topology CR name that the Service references
	Name string `json:"name"`

	// +kubebuilder:validation:Optional
	// Namespace - The Namespace to fetch the Topology CR referenced
	// NOTE: Namespace currently points by default to the same namespace where
	// the Service is deployed. Customizing the namespace is not supported and
	// webhooks prevent editing this field to a value different from the
	// current project
	Namespace string `json:"namespace,omitempty"`
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
func ValidateTopologyNamespace(refNs string, basePath field.Path, validNs string) *field.Error {
	if refNs != "" && refNs != validNs {
		topologyNamespace := basePath.Child("topology").Key("namespace")
		return field.Invalid(topologyNamespace, "namespace", "Customizing namespace field is not supported")
	}
	return nil
}

// EnsureTopologyRef - retrieve the Topology CR referenced and add a finalizer
func EnsureTopologyRef(
	ctx context.Context,
	h *helper.Helper,
	topologyRef *TopoRef,
	finalizer string,
	defaultLabelSelector *metav1.LabelSelector,
) (*Topology, string, error) {

	var err error
	var hash string

	// no Topology is passed at all or it is missing some data
	if topologyRef == nil || (topologyRef.Name == "" || topologyRef.Namespace == "") {
		return nil, "", fmt.Errorf("No valid TopologyRef input passed")
	}

	topology, hash, err := GetTopologyByName(
		ctx,
		h,
		topologyRef.Name,
		topologyRef.Namespace,
	)
	if err != nil {
		return topology, hash, err
	}

	// Add finalizer (if not present) to the resource consumed by the Service
	if controllerutil.AddFinalizer(topology, fmt.Sprintf("%s-%s", h.GetFinalizer(), finalizer)) {
		if err := h.GetClient().Update(ctx, topology); err != nil {
			return topology, hash, err
		}
	}

	if defaultLabelSelector != nil {
		// Set default LabelSelector on topologyConstraints if not set, similar to cluster level default:
		// https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/#cluster-level-default-constraints
		topology = topology.DeepCopy()

		topologyConstraints := topology.Spec.TopologySpreadConstraints
		if topologyConstraints != nil {
			for i := 0; i < len(*topologyConstraints); i++ {
				current := &(*topologyConstraints)[i]
				if current.LabelSelector == nil {
					current.LabelSelector = defaultLabelSelector
				}
			}
		}

		hash, err = util.ObjectHash(topology.Spec)
		if err != nil {
			return topology, hash, err
		}
	}

	return topology, hash, nil
}

// EnsureDeletedTopologyRef - remove the finalizer (passed as input) from the
// referenced topology CR
func EnsureDeletedTopologyRef(
	ctx context.Context,
	h *helper.Helper,
	topologyRef *TopoRef,
	finalizer string,
) (ctrl.Result, error) {

	// no Topology is passed at all or some data is missing
	if topologyRef == nil || (topologyRef.Name == "" || topologyRef.Namespace == "") {
		return ctrl.Result{}, nil
	}

	// Remove the finalizer from the Topology CR
	topology, _, err := GetTopologyByName(
		ctx,
		h,
		topologyRef.Name,
		topologyRef.Namespace,
	)

	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if !k8s_errors.IsNotFound(err) {
		if controllerutil.RemoveFinalizer(topology, fmt.Sprintf("%s-%s", h.GetFinalizer(), finalizer)) {
			err = h.GetClient().Update(ctx, topology)
			if err != nil && !k8s_errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			util.LogForObject(h, "Removed finalizer from Topology", topology)
		}
	}
	return ctrl.Result{}, nil
}
