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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("Service controller", func() {

	var serviceName types.NamespacedName
	var dnsMasqName types.NamespacedName
	var svcDNSData types.NamespacedName

	When("A Service is created with dnsmasq annotation", func() {
		BeforeEach(func() {
			instance := CreateDNSMasq(namespace, GetDefaultDNSMasqSpec())
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}
			serviceName = types.NamespacedName{
				Name:      "some-service",
				Namespace: namespace,
			}
			svcDNSData = types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc", dnsMasqName.Name),
				Namespace: namespace,
			}

			svc := CreateLoadBalancerService(serviceName, true)

			DeferCleanup(th.DeleteInstance, svc)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("should have created a DNSData with hosts data", func() {
			svc := th.GetService(serviceName)
			Expect(svc).To(Not(BeNil()))

			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.DNSDataLabelSelectorValue).To(Equal("dnsdata"))
				g.Expect(dnsdata.Spec.Hosts).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts[0].IP).To(Equal("172.20.0.80"))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames[0]).To(Equal(fmt.Sprintf("some-service.%s.svc", namespace)))
			}, timeout, interval).Should(Succeed())
		})

		It("should cleanup host DNSData when service gets deleted", func() {
			svc := th.GetService(serviceName)
			Expect(svc).To(Not(BeNil()))

			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.DNSDataLabelSelectorValue).To(Equal("dnsdata"))
				g.Expect(dnsdata.Spec.Hosts).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts[0].IP).To(Equal("172.20.0.80"))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames[0]).To(Equal(fmt.Sprintf("some-service.%s.svc", namespace)))
			}, timeout, interval).Should(Succeed())

			th.DeleteService(serviceName)

			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("A Service is created without dnsmasq annotation", func() {
		BeforeEach(func() {
			instance := CreateDNSMasq(namespace, GetDefaultDNSMasqSpec())
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}
			serviceName = types.NamespacedName{
				Name:      "some-service",
				Namespace: namespace,
			}
			svcDNSData = types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc", dnsMasqName.Name),
				Namespace: namespace,
			}
			svc := CreateLoadBalancerService(serviceName, false)

			DeferCleanup(th.DeleteInstance, svc)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("should not have created a DNSData without hosts data", func() {
			svc := th.GetService(serviceName)
			Expect(svc).To(Not(BeNil()))

			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.DNSDataLabelSelectorValue).To(Equal("dnsdata"))
				g.Expect(dnsdata.Spec.Hosts).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})
	})
})
