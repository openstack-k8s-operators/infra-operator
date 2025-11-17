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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPAddress -
type IPAddress struct {
	// Network name
	Network NetNameStr `json:"network"`

	// Subnet name
	Subnet NetNameStr `json:"subnet"`

	// Address contains the IP address
	Address string `json:"address"`
}

// ReservationSpec defines the desired state of Reservation
type ReservationSpec struct {
	// IPSetRef points to the IPSet object the IPs were created for.
	IPSetRef corev1.ObjectReference `json:"ipSetRef"`

	// +kubebuilder:validation:Required
	// Reservation, map (index network name) with reservation
	Reservation map[string]IPAddress `json:"reservation"`
}

// ReservationStatus defines the observed state of Reservation
type ReservationStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Reservation",type="string",JSONPath=".spec.reservation",description="Reservation"

// Reservation is the Schema for the reservations API
type Reservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReservationSpec   `json:"spec,omitempty"`
	Status ReservationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ReservationList contains a list of Reservation
type ReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Reservation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Reservation{}, &ReservationList{})
}
