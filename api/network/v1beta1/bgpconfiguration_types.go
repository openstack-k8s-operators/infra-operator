/*
Copyright 2023.

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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FRRNodeConfigurationSelectorType -
type FRRNodeConfigurationSelectorType struct {
	// +kubebuilder:validation:Optional
	// NodeName -  name of the node object as seen by running the `oc get nodes` command
	NodeName string `json:"nodeName,omitempty"`

	// +kubebuilder:validation:Optional
	// NodeSelector  to identify the correct FRRConfiguration from spec.nodeSelector
	NodeSelector metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// BGPConfigurationSpec defines the desired state of BGPConfiguration
type BGPConfigurationSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="metallb-system"
	// FRRConfigurationNamespace - namespace where to create the FRRConfiguration. Defaults to metallb-system.
	FRRConfigurationNamespace string `json:"frrConfigurationNamespace"`

	// +kubebuilder:validation:Optional
	// FRRNodeConfigurationSelector - per default the FRRConfiguration per node within the FRRConfigurationNamespace
	// gets queried using the FRRConfiguration.spec.NodeSelector `kubernetes.io/hostname: worker-0`. In case a more
	// specific
	FRRNodeConfigurationSelector []FRRNodeConfigurationSelectorType `json:"frrNodeConfigurationSelector,omitempty"`
}

// BGPConfigurationStatus defines the observed state of BGPConfiguration
type BGPConfigurationStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// BGPConfiguration is the Schema for the bgpconfigurations API
type BGPConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BGPConfigurationSpec   `json:"spec,omitempty"`
	Status BGPConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BGPConfigurationList contains a list of BGPConfiguration
type BGPConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BGPConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BGPConfiguration{}, &BGPConfigurationList{})
}
