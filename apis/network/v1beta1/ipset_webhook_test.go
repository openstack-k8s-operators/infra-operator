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

	. "github.com/onsi/gomega" //revive:disable:dot-imports
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

func TestIPSetValiateIPSetNetwork(t *testing.T) {
	tests := []struct {
		name      string
		expectErr bool
		c         *IPSet
		n         NetConfigSpec
	}{
		{
			name:      "should succeed with good values",
			expectErr: false,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("172.17.0.10"),
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail with network not in NetConfig",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "foo",
							SubnetName: "subnet1",
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail with subnet not in network of the NetConfig",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "foo",
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "[IPv4] should fail with bad FixedIP",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("172.17.0.0.10"),
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "[IPv6] should fail with bad FixedIP",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("fd00:fd00:fd00:20000::10"),
						},
					},
				},
			},
			n: getDefaultIPv6NetConfigSpec(),
		},
		{
			name:      "[IPv4] should fail with subnet IPv4 and FixedIP IPv6",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("fd00:fd00:fd00:2000::10"),
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "[IPv6] should fail with subnet IPv6 and FixedIP IPv4",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("172.17.0.10"),
						},
					},
				},
			},
			n: getDefaultIPv6NetConfigSpec(),
		},
		{
			name:      "[IPv4] should fail when the FixedIP is ouside the CIDR",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("172.17.1.10"),
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "[IPv6] should fail when the FixedIP is ouside the CIDR",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:       "net1",
							SubnetName: "subnet1",
							FixedIP:    ptr.To("fd00:fd00:fd00:2001::10"),
						},
					},
				},
			},
			n: getDefaultIPv6NetConfigSpec(),
		},
		{
			name:      "should fail with multiple networks with same IP family have DefaultRoute flag",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:         "net1",
							SubnetName:   "subnet1",
							DefaultRoute: ptr.To(true),
						},
						{
							Name:         "net2",
							SubnetName:   "subnet3",
							DefaultRoute: ptr.To(true),
						},
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should succeed with multiple networks with different IP family have DefaultRoute flag",
			expectErr: false,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:         "net1",
							SubnetName:   "subnet1",
							DefaultRoute: ptr.To(true),
						},
						{
							Name:         "net2",
							SubnetName:   "subnet1",
							DefaultRoute: ptr.To(true),
						},
					},
				},
			},
			n: getDefaultIPv4IPv6NetConfigSpec(),
		},
		{
			name:      "should fail when defaultRoute is requested but not configured on the subnet",
			expectErr: true,
			c: &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "netcfg",
					Namespace: "foo",
				},
				Spec: IPSetSpec{
					Networks: []IPSetNetwork{
						{
							Name:         "net1",
							SubnetName:   "subnet2",
							DefaultRoute: ptr.To(true),
						},
					},
				},
			},
			n: getDefaultIPv4IPv6NetConfigSpec(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			basePath := field.NewPath("spec")

			var err error
			allErrs := valiateIPSetNetwork(tt.c.Spec.Networks, basePath, &tt.n)
			if len(allErrs) > 0 {
				err = apierrors.NewInvalid(GroupVersion.WithKind("NetConfig").GroupKind(), tt.c.Name, allErrs)
			}

			if tt.expectErr {
				g.Expect(err).NotTo(Succeed())

			} else {
				g.Expect(err).To(Succeed())
			}
		})
	}
}

func TestIPSetUpdateValidation(t *testing.T) {
	tests := []struct {
		name      string
		expectErr bool
		newSpec   *IPSetSpec
		oldSpec   *IPSetSpec
		n         NetConfigSpec
	}{
		{
			name:      "should succeed when values and templates correct",
			expectErr: false,
			newSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
					{
						Name:       "net2",
						SubnetName: "subnet3",
						FixedIP:    ptr.To("172.18.0.10"),
					},
				},
			},
			oldSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
					{
						Name:       "net2",
						SubnetName: "subnet3",
						FixedIP:    ptr.To("172.18.0.10"),
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail if network was removed",
			expectErr: true,
			newSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
				},
			},
			oldSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
					{
						Name:       "net2",
						SubnetName: "subnet3",
						FixedIP:    ptr.To("172.18.0.10"),
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail if a subnet in a network changed",
			expectErr: true,
			newSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
					{
						Name:       "net2",
						SubnetName: "subnet3",
						FixedIP:    ptr.To("172.18.0.10"),
					},
				},
			},
			oldSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet2",
						FixedIP:      ptr.To("172.17.1.10"),
						DefaultRoute: ptr.To(true),
					},
					{
						Name:       "net2",
						SubnetName: "subnet3",
						FixedIP:    ptr.To("172.18.0.10"),
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail when fixedIP changes",
			expectErr: true,
			newSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.11"),
						DefaultRoute: ptr.To(true),
					},
				},
			},
			oldSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
		{
			name:      "should fail when defaultRoute changes",
			expectErr: true,
			newSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:         "net1",
						SubnetName:   "subnet1",
						FixedIP:      ptr.To("172.17.0.10"),
						DefaultRoute: ptr.To(true),
					},
				},
			},
			oldSpec: &IPSetSpec{
				Networks: []IPSetNetwork{
					{
						Name:       "net1",
						SubnetName: "subnet1",
						FixedIP:    ptr.To("172.17.0.10"),
					},
				},
			},
			n: getDefaultIPv4NetConfigSpec(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			basePath := field.NewPath("spec")

			newCfg := &IPSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "foo",
				},
				Spec: *tt.newSpec,
			}

			var err error

			allErrs := valiateIPSetNetwork(tt.newSpec.Networks, basePath, &tt.n)
			if len(allErrs) > 0 {
				err = apierrors.NewInvalid(GroupVersion.WithKind("IPSet").GroupKind(), newCfg.Name, allErrs)
			}

			allErrs = valiateIPSetChanged(tt.newSpec.Networks, tt.oldSpec.Networks, basePath)
			if len(allErrs) > 0 {
				err = apierrors.NewInvalid(GroupVersion.WithKind("IPSet").GroupKind(), newCfg.Name, allErrs)
			}

			if tt.expectErr {
				g.Expect(err).NotTo(Succeed())

			} else {
				g.Expect(err).To(Succeed())
			}

		})
	}
}
