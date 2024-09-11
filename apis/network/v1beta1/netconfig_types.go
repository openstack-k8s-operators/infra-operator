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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:Pattern="^[a-zA-Z0-9][a-zA-Z0-9\\-_]*[a-zA-Z0-9]$"

// NetNameStr is used for validation of a net name.
type NetNameStr string

// Network definition
type Network struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ctlplane;internalapi;external;storage;storagemgmt;tenant
	// Name of the network, could be one of ctlplane, internalapi, external, storage, storagemgmt or tenant
	Name NetNameStr `json:"name"`

	// +kubebuilder:validation:Required
	// DNSDomain name of the Network
	DNSDomain string `json:"dnsDomain"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1500
	// MTU of the network
	MTU int `json:"mtu"`

	// +kubebuilder:validation:Required
	// Subnets of the network
	Subnets []Subnet `json:"subnets"`
}

// Subnet definition
type Subnet struct {
	// +kubebuilder:validation:Required
	// Name of the subnet
	Name NetNameStr `json:"name"`

	// +kubebuilder:validation:Required
	// Cidr the cidr to use for this network
	Cidr string `json:"cidr"`

	// +kubebuilder:validation:Optional
	// DNSDomain name of the subnet, allows to overwrite the DNSDomain of the Network
	DNSDomain *string `json:"dnsDomain,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Maximum=4094
	// Vlan ID
	Vlan *int `json:"vlan,omitempty"`

	// +kubebuilder:validation:Required
	// AllocationRanges a list of AllocationRange for assignment. Allocation will start
	// from first range, first address.
	AllocationRanges []AllocationRange `json:"allocationRanges"`

	// +kubebuilder:validation:Optional
	// ExcludeAddresses a set of IPs that should be excluded from used as reservation, for both dynamic
	// and static via IPSet FixedIP parameter
	ExcludeAddresses []string `json:"excludeAddresses,omitempty"`

	// +kubebuilder:validation:Optional
	// Gateway optional gateway for the network
	Gateway *string `json:"gateway,omitempty"`

	// +kubebuilder:validation:Optional
	// Routes, list of networks that should be routed via network gateway.
	Routes []Route `json:"routes,omitempty"`
}

// AllocationRange definition
type AllocationRange struct {
	// +kubebuilder:validation:Required
	// Start IP for the AllocationRange
	Start string `json:"start"`

	// +kubebuilder:validation:Required
	// End IP for the AllocationRange
	End string `json:"end"`
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

// NetConfigSpec defines the desired state of NetConfig
type NetConfigSpec struct {
	// +kubebuilder:validation:Required
	// Networks, list of all networks of the deployment
	Networks []Network `json:"networks"`
}

// NetConfigStatus defines the observed state of NetConfig
type NetConfigStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=netcfg;netscfg

// NetConfig is the Schema for the netconfigs API
type NetConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetConfigSpec   `json:"spec,omitempty"`
	Status NetConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// NetConfigList contains a list of NetConfig
type NetConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetConfig{}, &NetConfigList{})
}

// GetNet returns the network with name
func (instance NetConfig) GetNet(name NetNameStr) (*Network, error) {
	for _, net := range instance.Spec.Networks {
		if strings.EqualFold(string(net.Name), string(name)) {
			return &net, nil
		}
	}
	return nil, fmt.Errorf("no network with name: %s", name)
}

// GetNetAndSubnet returns the network and subnet with name
func (instance NetConfig) GetNetAndSubnet(name NetNameStr, subnetName NetNameStr) (*Network, *Subnet, error) {
	net, err := instance.GetNet(name)
	if err != nil {
		return nil, nil, err
	}
	for _, subnet := range net.Subnets {
		if strings.EqualFold(string(subnet.Name), string(subnetName)) {
			return net, &subnet, nil
		}
	}
	return nil, nil, fmt.Errorf("no subnet found with name: %s in network: %s", subnetName, name)
}
