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
	"strings"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
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

	When("A DNSData is created with a host that sets CNAMEs", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSDataSpec()
			spec["hosts"] = any([]networkv1.DNSHost{
				{
					Hostnames: []string{host1},
					IP:        "host-ip-1",
					CNAMEs:    []string{"alias2", "alias1"},
				},
			})
			instance := CreateDNSData(namespace, spec)
			dnsDataName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, instance)
		})

		It("generates a separate CNAME source ConfigMap", func() {
			th.ExpectCondition(
				dnsDataName,
				ConditionGetterFunc(DNSDataConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionTrue,
			)

			configData := th.GetConfigMap(dnsDataName)
			Expect(configData).ShouldNot(BeNil())
			Expect(configData.Data[dnsDataName.Name]).Should(
				ContainSubstring("host-ip-1 " + host1))
			Expect(configData.Data).NotTo(HaveKey(dnsmasq.CNAMEConfigMapKey))
			cnameName := types.NamespacedName{Namespace: namespace, Name: configData.Data[dnsmasq.CNAMEShardPointerKey]}
			cnameData := th.GetConfigMap(cnameName)
			Expect(cnameData.Labels).To(HaveKeyWithValue(dnsmasq.DNSDataCNAMEShardLabel, string(GetDNSData(dnsDataName).UID)))
			Expect(cnameData.Data[dnsmasq.CNAMEConfigMapKey]).Should(
				ContainSubstring(fmt.Sprintf("cname=alias1,%s\n", host1)))
			Expect(cnameData.Data[dnsmasq.CNAMEConfigMapKey]).Should(
				ContainSubstring(fmt.Sprintf("cname=alias2,%s\n", host1)))
		})

		It("splits large CNAME sources and removes stale shards", func() {
			oldBudget := dnsmasq.CNAMEConfigMapMaxBytes
			dnsmasq.CNAMEConfigMapMaxBytes = 8 * 1024
			DeferCleanup(func() { dnsmasq.CNAMEConfigMapMaxBytes = oldBudget })
			longDomain := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60)
			aliases := make([]string, 40)
			var want strings.Builder
			for i := range aliases {
				aliases[i] = fmt.Sprintf("alias-%04d.%s", i, longDomain)
				fmt.Fprintf(&want, "cname=%s,%s\n", aliases[i], longDomain)
			}
			Expect(want.Len()).To(BeNumerically(">", dnsmasq.CNAMEConfigMapMaxBytes))
			Eventually(func() error {
				current := GetDNSData(dnsDataName)
				current.Spec.Hosts = []networkv1.DNSHost{{IP: "192.0.2.1", Hostnames: []string{longDomain}, CNAMEs: aliases}}
				return th.K8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())
			var shardNames []string
			Eventually(func(g Gomega) {
				shardNames = strings.Split(th.GetConfigMap(dnsDataName).Data[dnsmasq.CNAMEShardPointerKey], ",")
				g.Expect(shardNames).To(HaveLen(2))
				first := th.GetConfigMap(types.NamespacedName{Namespace: namespace, Name: shardNames[0]}).Data[dnsmasq.CNAMEConfigMapKey]
				second := th.GetConfigMap(types.NamespacedName{Namespace: namespace, Name: shardNames[1]}).Data[dnsmasq.CNAMEConfigMapKey]
				g.Expect(first + second).To(Equal(want.String()))
				g.Expect(len(first)).To(BeNumerically("<=", dnsmasq.CNAMEConfigMapMaxBytes))
				g.Expect(len(second)).To(BeNumerically("<=", dnsmasq.CNAMEConfigMapMaxBytes))
			}, timeout, interval).Should(Succeed())
			Expect(th.GetConfigMap(dnsDataName).Data).NotTo(HaveKey(dnsmasq.CNAMEConfigMapKey))

			Eventually(func() error {
				current := GetDNSData(dnsDataName)
				current.Spec.Hosts[0].CNAMEs = aliases[:10]
				return th.K8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())
			var shrunkName string
			Eventually(func(g Gomega) {
				shrunkName = th.GetConfigMap(dnsDataName).Data[dnsmasq.CNAMEShardPointerKey]
				g.Expect(shrunkName).NotTo(BeEmpty())
				g.Expect(shrunkName).NotTo(ContainSubstring(","))
				g.Expect(th.GetConfigMap(types.NamespacedName{Namespace: namespace, Name: shrunkName}).Data[dnsmasq.CNAMEConfigMapKey]).To(
					Equal(strings.Join(strings.SplitAfter(want.String(), "\n")[:10], "")))
			}, timeout, interval).Should(Succeed())
			Eventually(func() bool {
				return k8s_errors.IsNotFound(th.K8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: shardNames[1]}, &corev1.ConfigMap{}))
			}, timeout, interval).Should(BeTrue())
			Eventually(func() error {
				current := GetDNSData(dnsDataName)
				current.Spec.Hosts[0].CNAMEs = nil
				return th.K8sClient.Update(ctx, current)
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(th.GetConfigMap(dnsDataName).Data[dnsmasq.CNAMEShardPointerKey]).To(BeEmpty())
				g.Expect(k8s_errors.IsNotFound(th.K8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: shrunkName}, &corev1.ConfigMap{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			Expect(th.GetConfigMap(dnsDataName).Data).To(HaveKey(dnsDataName.Name))
		})
	})

	When("Invalid DNS names are submitted for DNSData", func() {
		It("rejects an invalid CNAME alias at admission", func() {
			name := &networkv1.DNSData{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-cname", Namespace: namespace},
				Spec: networkv1.DNSDataSpec{Hosts: []networkv1.DNSHost{{
					IP:        "192.0.2.1",
					Hostnames: []string{host1},
					CNAMEs:    []string{"alias.example.com\nserver=/#/203.0.113.9"},
				}}},
			}

			err := k8sClient.Create(ctx, name)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.hosts[0].cnames[0]"))
		})

		It("rejects an invalid hostname at admission", func() {
			name := &networkv1.DNSData{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-hostname", Namespace: namespace},
				Spec: networkv1.DNSDataSpec{Hosts: []networkv1.DNSHost{{
					IP:        "192.0.2.1",
					Hostnames: []string{"host.example.com\nserver=/#/203.0.113.9"},
				}}},
			}

			err := k8sClient.Create(ctx, name)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.hosts[0].hostnames[0]"))
		})

		It("rejects a hostname label longer than 63 characters at admission", func() {
			longLabel := strings.Repeat("a", 64)
			name := &networkv1.DNSData{
				ObjectMeta: metav1.ObjectMeta{Name: "long-host-label", Namespace: namespace},
				Spec: networkv1.DNSDataSpec{Hosts: []networkv1.DNSHost{{
					IP:        "192.0.2.1",
					Hostnames: []string{longLabel + ".example.com"},
				}}},
			}

			err := k8sClient.Create(ctx, name)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.hosts[0].hostnames[0]"))
		})

		It("rejects a CNAME label longer than 63 characters at admission", func() {
			longLabel := strings.Repeat("a", 64)
			name := &networkv1.DNSData{
				ObjectMeta: metav1.ObjectMeta{Name: "long-cname-label", Namespace: namespace},
				Spec: networkv1.DNSDataSpec{Hosts: []networkv1.DNSHost{{
					IP:        "192.0.2.1",
					Hostnames: []string{host1},
					CNAMEs:    []string{longLabel + ".example.com"},
				}}},
			}

			err := k8sClient.Create(ctx, name)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.hosts[0].cnames[0]"))
		})
	})

	When("A DNSData is created with CNAMEs but more than one Hostnames entry", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSDataSpec()
			spec["hosts"] = any([]networkv1.DNSHost{
				{
					Hostnames: []string{host1, "host2"},
					IP:        "host-ip-1",
					CNAMEs:    []string{"alias1"},
				},
				{
					Hostnames: []string{"host3"},
					IP:        "host-ip-2",
					CNAMEs:    []string{"alias2"},
				},
			})
			instance := CreateDNSData(namespace, spec)
			dnsDataName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}

			DeferCleanup(th.DeleteInstance, instance)
		})

		It("sets ServiceConfigReadyCondition to an error instead of emitting malformed config, while still generating config for the other, valid host", func() {
			th.ExpectCondition(
				dnsDataName,
				ConditionGetterFunc(DNSDataConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionFalse,
			)

			configData := th.GetConfigMap(dnsDataName)
			Expect(configData).ShouldNot(BeNil())
			Expect(configData.Data[dnsDataName.Name]).Should(
				ContainSubstring("host-ip-1 " + host1 + " host2"))
			Expect(configData.Data[dnsDataName.Name]).Should(
				ContainSubstring("host-ip-2 host3"))
			Expect(configData.Data).NotTo(HaveKey(dnsmasq.CNAMEConfigMapKey))
			cnameName := types.NamespacedName{Namespace: namespace, Name: configData.Data[dnsmasq.CNAMEShardPointerKey]}
			cnameData := th.GetConfigMap(cnameName)
			Expect(cnameData.Data[dnsmasq.CNAMEConfigMapKey]).Should(
				ContainSubstring("cname=alias2,host3\n"))
			Expect(cnameData.Data[dnsmasq.CNAMEConfigMapKey]).ShouldNot(
				ContainSubstring("alias1"))
		})
	})
})
