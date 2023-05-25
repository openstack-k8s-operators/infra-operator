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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
)

// IPSetNetwork Type
type IPSetNetwork struct {
	// +kubebuilder:validation:Required
	// Network Name
	Name NetNameStr `json:"name"`

	// +kubebuilder:validation:Required
	// Subnet Name
	SubnetName NetNameStr `json:"subnetName"`

	// +kubebuilder:validation:Optional
	// Fixed Ip
	// TODO: add validation to webhook
	FixedIP *string `json:"fixedIP,omitempty"`

	// +kubebuilder:validation:Optional
	// Use gateway from subnet as default route. There can only be one default route defined per IPSet.
	DefaultRoute *bool `json:"defaultRoute,omitempty"`
}

// IPSetSpec defines the desired state of IPSet
type IPSetSpec struct {
	// Networks used to request IPs for
	Networks []IPSetNetwork `json:"networks"`
}

// IPSetStatus defines the observed state of IPSet
type IPSetStatus struct {
	// Reservation
	Reservation []IPAddress `json:"reservation,omitempty" optional:"true"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[0].status",description="Ready"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"
//+kubebuilder:printcolumn:name="Reservation",type="string",JSONPath=".status.reservation",description="Reservation"

// IPSet is the Schema for the ipsets API
type IPSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPSetSpec   `json:"spec,omitempty"`
	Status IPSetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// IPSetList contains a list of IPSet
type IPSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IPSet{}, &IPSetList{})
}

// GetConditions returns the list of conditions from the status
func (s IPSetStatus) GetConditions() condition.Conditions {
	return s.Conditions
}
