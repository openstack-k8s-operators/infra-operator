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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
)

var _ = Describe("IPSet controller", func() {
	var ipSetName types.NamespacedName
	var netCfgName types.NamespacedName

	When("an IPSet gets created with no NetConfig available", func() {
		It("it gets blocked by the webhook and fail", func() {

			raw := map[string]interface{}{
				"apiVersion": "network.openstack.org/v1beta1",
				"kind":       "IPSet",
				"metadata": map[string]interface{}{
					"name":      "foo",
					"namespace": namespace,
				},
				"spec": GetDefaultIPSetSpec(),
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			_, err := controllerutil.CreateOrPatch(
				th.Ctx, th.K8sClient, unstructuredObj, func() error { return nil })
			Expect(err).To(HaveOccurred())
		})
	})

	When("a GetDefaultIPSetSpec IPSet gets created", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			netCfgName.Name = netCfg.GetName()
			netCfgName.Namespace = netCfg.GetNamespace()

			Eventually(func(g Gomega) {
				res := GetNetConfig(netCfgName)
				g.Expect(res).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())
			ipset := CreateIPSet(namespace, GetDefaultIPSetSpec())

			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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
			netCfgName.Name = netCfg.GetName()
			netCfgName.Namespace = netCfg.GetNamespace()

			Eventually(func(g Gomega) {
				res := GetNetConfig(netCfgName)
				g.Expect(res).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			ipset := CreateIPSet(namespace, GetIPSetSpec(false, GetIPSetNet1WithFixedIP("172.17.0.150")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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
			netCfgName.Name = netCfg.GetName()
			netCfgName.Namespace = netCfg.GetNamespace()

			Eventually(func(g Gomega) {
				res := GetNetConfig(netCfgName)
				g.Expect(res).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			ipset := CreateIPSet(namespace, GetIPSetSpec(false, GetIPSetNet1WithFixedIP("172.17.0.220")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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
			netCfgName.Name = netCfg.GetName()
			netCfgName.Namespace = netCfg.GetNamespace()

			Eventually(func(g Gomega) {
				res := GetNetConfig(netCfgName)
				g.Expect(res).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			ipset := CreateIPSet(namespace, GetIPSetSpec(false, GetIPSetNet1WithFixedIP("172.17.0.201")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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

	When("an IPSet with Immutable flag gets created", func() {
		BeforeEach(func() {
			net1Spec := GetNetSpec(net1, GetSubnet1(subnet1))
			net2Spec := GetNetSpec(net2, GetSubnet2(subnet1))
			netCfg := CreateNetConfig(namespace, GetNetConfigSpec(net1Spec, net2Spec))
			netCfgName.Name = netCfg.GetName()
			netCfgName.Namespace = netCfg.GetNamespace()

			Eventually(func(g Gomega) {
				res := GetNetConfig(netCfgName)
				g.Expect(res).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			ipset := CreateIPSet(namespace, GetIPSetSpec(true, GetIPSetNet1WithFixedIP("172.17.0.220")))
			ipSetName = types.NamespacedName{
				Name:      ipset.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(func(ctx SpecContext) {
				th.DeleteInstance(ipset)
				th.DeleteInstance(netCfg)
			}, NodeTimeout(timeout))
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

		When("the IPSet Spec.Networks gets updates", func() {
			It("gets blocked by the webhook and fail", func() {
				instance := &networkv1.IPSet{}

				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, ipSetName, instance)).Should(Succeed())
					instance.Spec.Networks = append(instance.Spec.Networks, GetIPSetNet2())
					err := th.K8sClient.Update(ctx, instance)
					g.Expect(err.Error()).Should(ContainSubstring("Forbidden: Invalid value: \"object\": Spec.Networks is immutable"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("the IPSet Spec.Immutable gets flipped", func() {
			BeforeEach(func() {
				Eventually(func(g Gomega) {
					instance := &networkv1.IPSet{}
					g.Expect(k8sClient.Get(ctx, ipSetName, instance)).Should(Succeed())
					instance.Spec.Immutable = false
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())
			})

			It("a network can bet added", func() {
				instance := &networkv1.IPSet{}

				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, ipSetName, instance)).Should(Succeed())
					instance.Spec.Networks = append(instance.Spec.Networks, GetIPSetNet2())
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
