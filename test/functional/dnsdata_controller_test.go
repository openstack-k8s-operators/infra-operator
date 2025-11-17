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

package functional_test

import (
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
)

var _ = Describe("DNSData controller", func() {
	var dnsDataName types.NamespacedName

	When("A DNSData is created", func() {
		BeforeEach(func() {
			instance := CreateDNSData(namespace, GetDefaultDNSDataSpec())
			dnsDataName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, instance)
		})

		It("should have the Spec and Status fields initialized", func() {
			instance := GetDNSData(dnsDataName)
			Expect(instance.Status.Hash).To(BeEmpty())
			Expect(instance.Spec.DNSDataLabelSelectorValue).To(Equal("someselector"))
			Expect(instance.Spec.Hosts).To(HaveLen(2))
		})

		It("generated a ConfigMap holding dnsdata", func() {
			th.ExpectCondition(
				dnsDataName,
				ConditionGetterFunc(DNSDataConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionTrue,
			)

			configData := th.GetConfigMap(dnsDataName)
			Expect(configData).ShouldNot(BeNil())
			Expect(configData.Data[dnsDataName.Name]).Should(
				ContainSubstring("host-ip-1 host1"))
			// validate thet hosts are sorted
			Expect(configData.Data[dnsDataName.Name]).Should(
				ContainSubstring("host-ip-2 host2 host3"))
			Expect(configData.Labels["dnsmasqhosts"]).To(Equal("someselector"))
		})

		It("stored the input hash in the Status", func() {
			Eventually(func(g Gomega) {
				instance := GetDNSData(dnsDataName)
				g.Expect(instance.Status.Hash).To(Not(BeEmpty()))
			}, timeout, interval).Should(Succeed())
		})

		It("is Ready", func() {
			th.ExpectCondition(
				dnsDataName,
				ConditionGetterFunc(DNSDataConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})

		When("the CR is deleted", func() {
			It("deletes the generated ConfigMaps", func() {
				th.ExpectCondition(
					dnsDataName,
					ConditionGetterFunc(DNSDataConditionGetter),
					condition.ServiceConfigReadyCondition,
					corev1.ConditionTrue,
				)

				th.DeleteInstance(GetDNSData(dnsDataName))

				Eventually(func() []corev1.ConfigMap {
					return th.ListConfigMaps(dnsDataName.Name).Items
				}, timeout, interval).Should(BeEmpty())
			})
		})
	})
})
