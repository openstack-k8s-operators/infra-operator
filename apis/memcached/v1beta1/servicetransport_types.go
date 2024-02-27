/*
Copyright 2022.

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

// ServiceTransportSpec defines the desired state of ServiceTransport
type ServiceTransportSpec struct {
	// +kubebuilder:validation:Required
	// Name of the service targeted by the transport
	ServiceName string `json:"serviceName"`
}

// ServiceTransportStatus defines the observed state of ServiceTransport
type ServiceTransportStatus struct {

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// SecretName - name of the secret containing the service transport URL
	SecretName string `json:"secretName,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// ServiceTransport is the Schema for the servicetransports API
type ServiceTransport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceTransportSpec   `json:"spec,omitempty"`
	Status ServiceTransportStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ServiceTransportList contains a list of ServiceTransport
type ServiceTransportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceTransport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServiceTransport{}, &ServiceTransportList{})
}

// IsReady - returns true if service is ready to serve requests
func (instance ServiceTransport) IsReady() bool {
	return instance.Status.Conditions.IsTrue(ServiceTransportReadyCondition)
}
