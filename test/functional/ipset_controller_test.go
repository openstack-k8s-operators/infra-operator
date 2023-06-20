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

package functional_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/openstack-k8s-operators/lib-common/modules/test/helpers"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
)

var _ = Describe("IPSet controller", func() {
	var ipSetName types.NamespacedName

	When("an IPSet gets created with no NetConfig available", func() {
		BeforeEach(func() {
			// create IPSet
			ipset := CreateIPSet(namespace, GetDefaultIPSetSpec())
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
		})

		It("is not Ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.InputReadyCondition,
				corev1.ConditionFalse,
			)

			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				networkv1.ReservationReadyCondition,
				corev1.ConditionUnknown,
			)

			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
		})

		When("the NetConfig is created with all the expected fields", func() {
			BeforeEach(func() {
				netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())

				DeferCleanup(th.DeleteInstance, netCfg)
			})

			It("reports that input is ready", func() {
				th.ExpectCondition(
					ipSetName,
					ConditionGetterFunc(IPSetConditionGetter),
					condition.InputReadyCondition,
					corev1.ConditionTrue,
				)
			})

			It("reports the reservation is ready", func() {
				th.ExpectCondition(
					ipSetName,
					ConditionGetterFunc(IPSetConditionGetter),
					networkv1.ReservationReadyCondition,
					corev1.ConditionTrue,
				)
			})

			It("reports the overall state is ready", func() {
				th.ExpectCondition(
					ipSetName,
					ConditionGetterFunc(IPSetConditionGetter),
					condition.ReadyCondition,
					corev1.ConditionTrue,
				)
			})
		})
	})

	When("a GetDefaultIPSetSpec IPSet gets created", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			ipset := CreateIPSet(namespace, GetDefaultIPSetSpec())

			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created an IPSet", func() {
			Eventually(func(g Gomega) {
				res := GetReservationFromNet(ipSetName, "net-1")
				g.Expect(res.Address).To(Equal("172.17.0.100"))
				g.Expect(res.DNSDomain).To(Equal("net-1.example.com"))
			}, timeout, interval).Should(Succeed())
		})

		It("reports the overall state is ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("an IPSet with FixedIP inside AllocationRange gets created", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			ipset := CreateIPSet(namespace, GetIPSetSpec(GetIPSetNet1WithFixedIP("172.17.0.150")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created an IPSet with IP 172.17.0.150 on net-1", func() {
			Eventually(func(g Gomega) {
				res := GetReservationFromNet(ipSetName, "net-1")
				g.Expect(res.Address).To(Equal("172.17.0.150"))
			}, timeout, interval).Should(Succeed())
		})

		It("reports the overall state is ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("an IPSet with FixedIP outside AllocationRange gets created", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			ipset := CreateIPSet(namespace, GetIPSetSpec(GetIPSetNet1WithFixedIP("172.17.0.220")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created an IPSet with IP 172.17.0.220 on net-1", func() {
			Eventually(func(g Gomega) {
				res := GetReservationFromNet(ipSetName, "net-1")
				g.Expect(res.Address).To(Equal("172.17.0.220"))
			}, timeout, interval).Should(Succeed())
		})

		It("reports the overall state is ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("an IPSet with FixedIP requests IP listed in ExcludeAddresses", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			ipset := CreateIPSet(namespace, GetIPSetSpec(GetIPSetNet1WithFixedIP("172.17.0.201")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created an IPSet with IP 172.17.0.201 on net-1", func() {
			Eventually(func(g Gomega) {
				res := GetReservationFromNet(ipSetName, "net-1")
				g.Expect(res).To(Equal(networkv1.IPSetReservation{}))
			}, timeout, interval).Should(Succeed())
		})

		It("reports that input is ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.InputReadyCondition,
				corev1.ConditionTrue,
			)
		})

		It("reports the reservation is false", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				networkv1.ReservationReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("reports the overall state is false", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
		})
	})

	When("a GetDefaultIPSetSpec IPSet gets created using a custom NetConfig", func() {
		BeforeEach(func() {
			netSpec := GetNetSpec(net1, GetSubnet1(subnet1))
			netSpec.Subnets[0].DNSDomain = pointer.String("subnet1.net-1.example.com")
			netCfg := CreateNetConfig(namespace, GetNetConfigSpec(netSpec))
			ipset := CreateIPSet(namespace, GetDefaultIPSetSpec())

			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, ipset)
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created an IPSet with DNSDomain from subnet", func() {
			Eventually(func(g Gomega) {
				res := GetReservationFromNet(ipSetName, "net-1")
				g.Expect(res.Address).To(Equal("172.17.0.100"))
				g.Expect(res.DNSDomain).To(Equal("subnet1.net-1.example.com"))
			}, timeout, interval).Should(Succeed())
		})

		It("reports the overall state is ready", func() {
			th.ExpectCondition(
				ipSetName,
				ConditionGetterFunc(IPSetConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})
})
