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
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

// IPv4
var (
	ipv4Subnet1 = Subnet{
		Name:    "subnet1",
		Cidr:    "172.17.0.0/24",
		Gateway: pointer.String("172.17.0.1"),
		AllocationRanges: []AllocationRange{
			{
				Start: "172.17.0.1",
				End:   "172.17.0.10",
			},
			{
				Start: "172.17.0.20",
				End:   "172.17.0.30",
			},
		},
		ExcludeAddresses: []string{
			"172.17.0.5",
			"172.17.0.15",
		},
		Routes: []Route{
			{
				Destination: "172.18.0.0/24",
				Nexthop:     "172.17.0.254",
			},
		},
	}
	ipv4subnet2 = Subnet{
		Name: "subnet2",
		Cidr: "172.17.1.0/24",
		AllocationRanges: []AllocationRange{
			{
				Start: "172.17.1.1",
				End:   "172.17.1.10",
			},
		},
		ExcludeAddresses: []string{
			"172.17.1.5",
			"172.17.1.15",
		},
	}
	ipv6Subnet1 = Subnet{
		Name:    "subnet1",
		Cidr:    "fd00:fd00:fd00:2000::/64",
		Gateway: pointer.String("fd00:fd00:fd00:2000::1"),
		AllocationRanges: []AllocationRange{
			{
				Start: "fd00:fd00:fd00:2000::1",
				End:   "fd00:fd00:fd00:2000::200",
			},
			{
				Start: "fd00:fd00:fd00:2000:ffff:ffff:ffff:1",
				End:   "fd00:fd00:fd00:2000:ffff:ffff:ffff:fffe",
			},
		},
		ExcludeAddresses: []string{
			"fd00:fd00:fd00:2000::5",
			"fd00:fd00:fd00:2000::15",
		},
		Routes: []Route{
			{
				Destination: "fd00:fd00:fd00:2001::/64",
				Nexthop:     "fd00:fd00:fd00:2000::5",
			},
		},
	}
	ipv6subnet2 = Subnet{
		Name: "subnet2",
		Cidr: "fd00:fd00:fd00:2001::/64",
		AllocationRanges: []AllocationRange{
			{
				Start: "fd00:fd00:fd00:2001::10",
				End:   "fd00:fd00:fd00:2001:ffff:ffff:ffff:fffe",
			},
		},
		ExcludeAddresses: []string{
			"fd00:fd00:fd00:2001::5",
			"fd00:fd00:fd00:2001::15",
		},
	}
)

// subnet1 with bad gateway address
func subnet1BadCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Cidr = "172.17.0.0.0/24"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Cidr = "fd00:fd00:fd00:20000::/64"
	}

	return *subnet
}

// subnet1 with bad gateway address
func subnet1BadGatewayFormat(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("172.17.0.0.1")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("fd00:fd00:fd00:20000::1")
	}

	return *subnet
}

// subnet1 with gateway address
func subnet1GatewayWrongIPVersion(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("fd00:fd00:fd00:2000::1")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("172.17.0.1")
	}

	return *subnet
}

// subnet1 with gateway address outside CIDR
func subnet1GatewayOutsideCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("172.17.1.1")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Gateway = pointer.String("fd00:fd00:fd00:2001::1")
	}

	return *subnet
}

// subnet1 with bad allocation start address
func subnet1BadAllocationStart(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "172.17.0.0.1"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "fd00:fd00:fd00:20000::1"

	}
	return *subnet
}

// subnet1 with bad allocation end address
func subnet1BadAllocationEnd(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].End = "172.17.0.0.10"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].End = "fd00:fd00:fd00:20000::200"
	}

	return *subnet
}

// subnet1 with allocation range wrong IP version
func subnet1AllocationRangeWrongIPVersion(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "fd00:fd00:fd00:2000::10"
		subnet.AllocationRanges[0].End = "fd00:fd00:fd00:2000::200"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "172.17.0.1"
		subnet.AllocationRanges[0].End = "172.17.0.10"
	}

	return *subnet
}

// subnet1 with allocation range start outside CIDR
func subnet1AllocationStartOutsideCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "172.17.1.1"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "fd00:fd00:fd00:2001::1"
	}

	return *subnet
}

// subnet1 with allocation range end outside CIDR
func subnet1AllocationEndOutsideCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].End = "172.17.1.10"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].End = "fd00:fd00:fd00:2001::200"
	}

	return *subnet
}

// subnet1 with allocation range start > end
func subnet1AllocationStartAfterEnd(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "172.17.0.11"
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.AllocationRanges[0].Start = "fd00:fd00:fd00:2000::201"
	}

	return *subnet
}

// subnet1 with bad exludeaddress
func subnet1BadExcludeAddress(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "172.17.0.0.6")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "fd00:fd00:fd00:20000::6")
	}

	return *subnet
}

// subnet1 with exludeaddress address wrong ip version
func subnet1ExcludeAddressWrongIPVersion(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "fd00:fd00:fd00:2000::1")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "172.17.0.6")
	}

	return *subnet
}

// subnet1 with ExcludeAddress outside CIDR
func subnet1ExcludeAddressOutsideCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "172.17.1.6")
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.ExcludeAddresses = append(subnet.ExcludeAddresses, "fd00:fd00:fd00:2001::6")
	}

	return *subnet
}

// subnet1 with bad route nexthop
func subnet1BadRouteNexthop(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "172.17.1.0/24", Nexthop: "172.17.0.0.6"})
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "fd00:fd00:fd00:2001::/64", Nexthop: "fd00:fd00:fd00:20000::6"})
	}

	return *subnet
}

// subnet1 with route nexthop address wrong ip version
func subnet1RouteNexthopWrongIPVersion(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "172.17.1.0/24", Nexthop: "fd00:fd00:fd00:2000::1"})
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "fd00:fd00:fd00:2001::/64", Nexthop: "172.17.0.1"})
	}

	return *subnet
}

// subnet1 with route nexthop outside CIDR
func subnet1RouteNexthopOutsideCIDR(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "172.17.1.0/24", Nexthop: "172.17.1.6"})
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "fd00:fd00:fd00:2001::/64", Nexthop: "fd00:fd00:fd00:2001::6"})
	}

	return *subnet
}

// subnet1 with bad route destination
func subnet1BadRouteDestination(ipv4 bool) Subnet {
	var subnet *Subnet

	if ipv4 {
		subnet = ipv4Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "172.17.1.0.0/24", Nexthop: "172.17.0.6"})
	} else {
		subnet = ipv6Subnet1.DeepCopy()
		subnet.Routes = append(subnet.Routes, Route{Destination: "fd00:fd00:fd00:20001::/64", Nexthop: "fd00:fd00:fd00:2000::6"})
	}

	return *subnet
}

func TestNetConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		expectErr bool
		c         *NetConfig
	}{
		{
			name:      "[IPv4] should succeed with good values",
			expectErr: false,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								ipv4Subnet1,
								ipv4subnet2,
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should succeed with good values",
			expectErr: false,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								ipv6Subnet1,
								ipv6subnet2,
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad subnet cidr",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad subnet cidr",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad gateway",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadGatewayFormat(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad gateway",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadGatewayFormat(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with subnet IPv4 and gateway IPv6",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1GatewayWrongIPVersion(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with subnet IPv6 and gateway IPv4",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1GatewayWrongIPVersion(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the gatway is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1GatewayOutsideCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the gatway is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1GatewayOutsideCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad subnet allocation start address",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadAllocationStart(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad subnet allocation start address",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadAllocationStart(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad subnet allocation end address",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadAllocationEnd(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad subnet allocation end address",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadAllocationEnd(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with subnet CIDR ipv4 but allocation range ipv6",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationRangeWrongIPVersion(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with subnet CIDR ipv6 but allocation range ipv4",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationRangeWrongIPVersion(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the allocation range start is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationStartOutsideCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the allocation range start is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationStartOutsideCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the allocation range end is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationEndOutsideCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the allocation range end is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationEndOutsideCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the allocation range start is > end",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationStartAfterEnd(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the allocation range start is > end",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1AllocationStartAfterEnd(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad excludeAddress",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadExcludeAddress(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad excludeAddress",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadExcludeAddress(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with subnet IPv4 and excludeAddress IPv6",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1ExcludeAddressWrongIPVersion(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with subnet IPv6 and excludeAddress IPv4",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1ExcludeAddressWrongIPVersion(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the excludeAddress is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1ExcludeAddressOutsideCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the excludeAddress is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1ExcludeAddressOutsideCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad route nexthop",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadRouteNexthop(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad route nexthop",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadRouteNexthop(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with subnet IPv4 and route nexthop IPv6",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1RouteNexthopWrongIPVersion(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with subnet IPv6 and route nexthop IPv4",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1RouteNexthopWrongIPVersion(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail when the route nexthop is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1RouteNexthopOutsideCIDR(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail when the route nexthop is ouside the CIDR",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1RouteNexthopOutsideCIDR(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv4] should fail with bad route destination",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadRouteDestination(true),
							},
						},
					},
				},
			},
		},
		{
			name:      "[IPv6] should fail with bad route destination",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							MTU:  1500,
							Subnets: []Subnet{
								subnet1BadRouteDestination(false),
							},
						},
					},
				},
			},
		},
		{
			name:      "should fail with duplicate network names",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
						},
						{
							Name: "net1",
						},
					},
				},
			},
		},
		{
			name:      "should fail with duplicate subnet names within a network",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
								},
								{
									Name: "subnet1",
									Cidr: "172.17.1.0/24",
								},
							},
						},
					},
				},
			},
		},
		{
			name:      "should succeed with same subnet names in different networks",
			expectErr: false,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
								},
							},
						},
						{
							Name: "net2",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.1.0/24",
								},
							},
						},
					},
				},
			},
		},
		{
			name:      "should fail with duplicate subnet CIDRs within a network",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
								},
								{
									Name: "subnet2",
									Cidr: "172.17.0.0/24",
								},
							},
						},
					},
				},
			},
		},
		{
			name:      "should fail with duplicate subnet CIDRs on different networks",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
								},
							},
						},
						{
							Name: "net2",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
								},
							},
						},
					},
				},
			},
		},
		{
			name:      "should fail with duplicate subnet CIDRs on different networks on different vlans",
			expectErr: true,
			c: &NetConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: NetConfigSpec{
					Networks: []Network{
						{
							Name: "net1",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
									Vlan: pointer.Int(1),
								},
							},
						},
						{
							Name: "net2",
							Subnets: []Subnet{
								{
									Name: "subnet1",
									Cidr: "172.17.0.0/24",
									Vlan: pointer.Int(2),
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			if tt.expectErr {
				g.Expect(tt.c.ValidateCreate()).NotTo(Succeed())
			} else {
				g.Expect(tt.c.ValidateCreate()).To(Succeed())
			}
		})
	}
}
