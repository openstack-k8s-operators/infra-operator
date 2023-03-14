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
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpenStackNetSpec defines the desired state of OpenStackNet
type OpenStackNetSpec struct {
	// +kubebuilder:validation:Optional
	//  List of subnets
	Subnets []OpenStackSubnet `json:"subnets,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1500
	// MTU of the network
	MTU int `json:"mtu"`

	// +kubebuilder:validation:Required
	// DomainName the name of the domain for this network, usually lower(Name)."OSNetConfig.Spec.DomainName"
	DomainName string `json:"domainName"`
}

// OpenStackSubnet type
type OpenStackSubnet struct {

	// +kubebuilder:validation:Required
	// Name of the subnet
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	// Cidr the cidr to use for this network
	Cidr string `json:"cidr"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Maximum=4094
	// Vlan ID
	Vlan int `json:"vlan,omitempty"`

	// +kubebuilder:validation:Required
	// AllocationRange a set of IPs for assignment
	AllocationRange AllocationRange `json:"allocationRange"`

	// +kubebuilder:validation:Optional
	// Gateway optional gateway for the network
	Gateway string `json:"gateway,omitempty"`

	// +kubebuilder:validation:Optional
	// Routes, list of networks that should be routed via network gateway.
	Routes []Route `json:"routes,omitempty"`

	// +kubebuilder:validation:Optional
	// IP Reserverations for Subnet
	Reservations map[string]IPReservation `json:"reservations,omitempty"`
}

// AllocationRange type
type AllocationRange struct {
	// +kubebuilder:validation:Required
	// AllocationStart a set of IPs that are reserved and will not be assigned
	AllocationStart string `json:"allocationStart"`

	// +kubebuilder:validation:Required
	// AllocationEnd a set of IPs that are reserved and will not be assigned
	AllocationEnd string `json:"allocationEnd"`
}

// Route definition
type Route struct {
	// +kubebuilder:validation:Required
	// Destination, network CIDR
	Destination string `json:"destination"`

	// +kubebuilder:validation:Required
	// Nexthop, gateway for the destination
	Nexthop string `json:"nexthop"`
}

// IPReservation contains an IP and Deleted flag
type IPReservation struct {
	// +kubebuilder:validation:Required
	IP string `json:"ip"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	VIP bool `json:"vip"`
	// +kubebuilder:validation:Optional
	Gateway string `json:"gateway,omitempty"`
	// +kubebuilder:validation:Optional
	Routes []Route `json:"routes,omitempty"`
}

// OpenStackNetStatus defines the observed state of OpenStackNet
type OpenStackNetStatus struct {
	// All Subnets , Probably can be removed later
	// +kubebuilder:validation:Optional
	Subnets []OpenStackSubnet `json:"subnets,omitempty"`

	// Status Conditions
	// +kubebuilder:validation:Optional
	Conditions condition.Conditions `json:"conditions,omitempty"`
}

//+operator-sdk:csv:customresourcedefinitions:displayName="OpenStack Network`"
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="MTU",type=string,JSONPath=`.spec.mtu`
//+kubebuilder:printcolumn:name="DomainName",type=string,JSONPath=`.spec.domainName`
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// OpenStackNet is the Schema for the openstacknets API
type OpenStackNet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenStackNetSpec   `json:"spec,omitempty"`
	Status OpenStackNetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OpenStackNetList contains a list of OpenStackNet
type OpenStackNetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenStackNet `json:"items"`
}

// InitCondition  Initializes conditions
func (instance OpenStackNet) InitCondition() {
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
	}
	cl := condition.CreateList(
		condition.UnknownCondition(OpenStackNetworkReadyCondition, condition.InitReason, condition.ReadyInitMessage))
	// initialize conditions used later as Status=Unknown
	instance.Status.Conditions.Init(&cl)
}

func init() {
	SchemeBuilder.Register(&OpenStackNet{}, &OpenStackNetList{})
}
