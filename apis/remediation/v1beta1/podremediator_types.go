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
	// +kubebuilder:default=false
	// Disabled stops annotation and deletion and clears pending remediation consent.
	// Default false: applying the CR enables PVC remediation when NHC/SNR are present; set disabled to true to turn it off.
	Disabled bool `json:"disabled,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:items:MinLength=1
	// +listType=set
	// Namespaces is the list of namespaces whose PVCs this PodRemediator watches.
	// If empty, the controller watches only the namespace containing this CR.
	// The effective namespace scope is immutable after CR creation. Delete and
	// recreate the CR to change its scope; deletion cleanup clears pending
	// handshakes in the configured namespaces.
	Namespaces []string `json:"namespaces,omitempty"`

	// +kubebuilder:validation:Optional
	// ConsentPollInterval controls how often the controller re-checks PVCs waiting
	// for SNR fencing confirmation or app-operator safe-to-delete consent.
	// Lower values reduce recovery latency; higher values reduce
	// API load. Overrides the PODREMEDIATOR_CONSENT_POLL_INTERVAL env var.
	// Format: Go duration string, e.g. "2m", "90s". Default: "2m".
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1s')",message="must be a valid duration of at least 1s"
	ConsentPollInterval *metav1.Duration `json:"consentPollInterval,omitempty"`

	// +kubebuilder:validation:Optional
	// PeriodicPollInterval is the safety-net requeue interval when the controller
	// is idle (no unhealthy nodes, disabled, or NHC/SNR missing). Guarantees the
	// controller re-evaluates node health after a pod restart when nodes are already
	// NotReady and no node-transition event fires. Overrides the
	// PODREMEDIATOR_PERIODIC_POLL_INTERVAL env var.
	// Format: Go duration string, e.g. "5m", "2m". Default: "5m".
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1s')",message="must be a valid duration of at least 1s"
	PeriodicPollInterval *metav1.Duration `json:"periodicPollInterval,omitempty"`
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
// When present, the controller watches worker nodes and PVCs in spec.namespaces
// (or only the CR's namespace when the field is empty). When NHC/SNR mark a node
// for remediation, it deletes eligible PVCs so workloads can respawn.
// NHC and SNR must be installed and configured; otherwise the controller sets ReadyCondition False.
// +kubebuilder:validation:XValidation:rule="(has(self.spec) && has(self.spec.namespaces) && size(self.spec.namespaces) > 0) == (has(oldSelf.spec) && has(oldSelf.spec.namespaces) && size(oldSelf.spec.namespaces) > 0) && (!(has(self.spec) && has(self.spec.namespaces) && size(self.spec.namespaces) > 0) || self.spec.namespaces == oldSelf.spec.namespaces)",message="namespace scope cannot be changed after creation; delete and recreate the PodRemediator"
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
