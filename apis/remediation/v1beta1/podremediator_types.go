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

package v1beta1

import (
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodRemediatorSpec defines the desired state of PodRemediator
type PodRemediatorSpec struct {
	// +kubebuilder:validation:Optional
	// Namespaces to watch for pods with local PVCs. Empty means the CR's namespace only.
	Namespaces []string `json:"namespaces,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	// Disabled allows disabling automated PVC deletion (monitoring only), matching spec.disabled in Instance HA.
	// Default false: applying the CR enables PVC remediation when NHC/SNR are present; set disabled to true to turn it off.
	Disabled bool `json:"disabled,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// MaxUnhealthyNodes is a cluster-wide disruption budget for PVC remediation: when greater than zero, the
	// controller does not delete PVCs while the number of NotReady nodes is strictly greater than this value.
	// Zero means no limit (legacy behavior). For a three-member Galera StatefulSet, operators often set this
	// to 1 so volumes are not stripped across multiple simultaneous node failures (similar in intent to
	// Instance HA THRESHOLD protecting against mass-incident actions).
	MaxUnhealthyNodes int32 `json:"maxUnhealthyNodes,omitempty"`
}

// PodRemediatorStatus defines the observed state of PodRemediator
type PodRemediatorStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].message",description="Message"

// PodRemediator is the Schema for the podremediators API.
// When present, the controller watches worker nodes and pods with local PVCs; when NHC/SNR
// mark a node for remediation, it deletes the corresponding PVCs so workloads can respawn.
// NHC and SNR must be installed and configured; otherwise the controller sets ReadyCondition False.
type PodRemediator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodRemediatorSpec   `json:"spec,omitempty"`
	Status PodRemediatorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PodRemediatorList contains a list of PodRemediator
type PodRemediatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodRemediator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodRemediator{}, &PodRemediatorList{})
}
