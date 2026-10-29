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

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
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

	When("A Service is created with the cnames annotation", func() {
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

			svc := CreateLoadBalancerService(serviceName, true, "custom.example.com", "invalid alias")

			DeferCleanup(th.DeleteInstance, svc)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("should register valid CNAME aliases and ignore invalid aliases", func() {
			svc := th.GetService(serviceName)
			Expect(svc).To(Not(BeNil()))

			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts[0].IP).To(Equal("172.20.0.80"))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames).To(ConsistOf(
					fmt.Sprintf("some-service.%s.svc", namespace)))
				g.Expect(dnsdata.Spec.Hosts[0].CNAMEs).To(ConsistOf("custom.example.com"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("Two Services share the same LoadBalancer IP and one sets the cnames annotation", func() {
		var otherServiceName types.NamespacedName

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
			otherServiceName = types.NamespacedName{
				Name:      "other-service",
				Namespace: namespace,
			}
			svcDNSData = types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc", dnsMasqName.Name),
				Namespace: namespace,
			}

			svc := CreateLoadBalancerService(serviceName, true, "custom.example.com")
			otherSvc := CreateLoadBalancerService(otherServiceName, true)

			DeferCleanup(th.DeleteInstance, svc)
			DeferCleanup(th.DeleteInstance, otherSvc)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("drops the CNAMEs instead of producing an entry with more than one canonical hostname", func() {
			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts).To(HaveLen(1))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames).To(ConsistOf(
					fmt.Sprintf("some-service.%s.svc", namespace),
					fmt.Sprintf("other-service.%s.svc", namespace)))
				g.Expect(dnsdata.Spec.Hosts[0].CNAMEs).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("Two Services share the same LoadBalancer IP and hostname", func() {
		var otherServiceName types.NamespacedName

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
			otherServiceName = types.NamespacedName{
				Name:      "other-service",
				Namespace: namespace,
			}
			svcDNSData = types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc", dnsMasqName.Name),
				Namespace: namespace,
			}

			svc := CreateLoadBalancerService(serviceName, true, "custom-one.example.com")
			CreateLoadBalancerService(otherServiceName, true, "custom-two.example.com")
			otherSvc := th.GetService(otherServiceName)
			otherSvc.Annotations[service.AnnotationHostnameKey] = fmt.Sprintf("%s.%s.svc", serviceName.Name, namespace)
			Expect(k8sClient.Update(ctx, otherSvc)).Should(Succeed())

			DeferCleanup(th.DeleteInstance, svc)
			DeferCleanup(th.DeleteInstance, otherSvc)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("deduplicates the hostname and keeps aliases for the shared canonical name", func() {
			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts).To(HaveLen(1))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames).To(Equal(
					[]string{fmt.Sprintf("some-service.%s.svc", namespace)}))
				g.Expect(dnsdata.Spec.Hosts[0].CNAMEs).To(Equal(
					[]string{"custom-one.example.com", "custom-two.example.com"}))
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

	When("A Service has an invalid hostname annotation", func() {
		var validServiceName types.NamespacedName

		BeforeEach(func() {
			instance := CreateDNSMasq(namespace, GetDefaultDNSMasqSpec())
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}
			serviceName = types.NamespacedName{
				Name:      "invalid-service",
				Namespace: namespace,
			}
			validServiceName = types.NamespacedName{
				Name:      "valid-service",
				Namespace: namespace,
			}
			svcDNSData = types.NamespacedName{
				Name:      fmt.Sprintf("%s-svc", dnsMasqName.Name),
				Namespace: namespace,
			}

			invalidService := CreateLoadBalancerService(serviceName, false)
			invalidService.Annotations = map[string]string{
				service.AnnotationHostnameKey: "invalid hostname",
			}
			Expect(k8sClient.Update(ctx, invalidService)).To(Succeed())
			validService := CreateLoadBalancerService(validServiceName, true)

			DeferCleanup(th.DeleteInstance, invalidService)
			DeferCleanup(th.DeleteInstance, validService)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("skips the invalid Service without blocking valid DNSData", func() {
			Eventually(func(g Gomega) {
				dnsdata := GetDNSData(svcDNSData)
				g.Expect(dnsdata).To(Not(BeNil()))
				g.Expect(dnsdata.Spec.Hosts).To(HaveLen(1))
				g.Expect(dnsdata.Spec.Hosts[0].IP).To(Equal("172.20.0.80"))
				g.Expect(dnsdata.Spec.Hosts[0].Hostnames).To(ConsistOf(
					fmt.Sprintf("valid-service.%s.svc", namespace)))
			}, timeout, interval).Should(Succeed())
		})
	})
})
