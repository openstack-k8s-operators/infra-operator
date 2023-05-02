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

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"

	. "github.com/openstack-k8s-operators/lib-common/modules/test/helpers"
)

var _ = Describe("DNSMasq controller", func() {
	var dnsMasqName types.NamespacedName
	var dnsMasqServiceAccountName types.NamespacedName
	var dnsMasqRoleName types.NamespacedName
	var dnsMasqRoleBindingName types.NamespacedName
	var deploymentName types.NamespacedName
	var namespace string

	BeforeEach(func() {
		// NOTE(gibi): We need to create a unique namespace for each test run
		// as namespaces cannot be deleted in a locally running envtest. See
		// https://book.kubebuilder.io/reference/envtest.html#namespace-usage-limitation
		namespace = uuid.New().String()
		th.CreateNamespace(namespace)
		// We still request the delete of the Namespace to properly cleanup if
		// we run the test in an existing cluster.
		DeferCleanup(th.DeleteNamespace, namespace)

	})

	When("A DNSMasq is created", func() {
		BeforeEach(func() {
			instance := CreateDNSMasq(namespace, GetDefaultDNSMasqSpec())
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}
			dnsMasqServiceAccountName = types.NamespacedName{
				Namespace: namespace,
				Name:      "dnsmasq-" + dnsMasqName.Name,
			}
			dnsMasqRoleName = types.NamespacedName{
				Namespace: namespace,
				Name:      dnsMasqServiceAccountName.Name + "-role",
			}
			dnsMasqRoleBindingName = types.NamespacedName{
				Namespace: namespace,
				Name:      dnsMasqServiceAccountName.Name + "-rolebinding",
			}

			dnsDataCM := types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}
			CreateDNSdataConfigMap(dnsDataCM)
			DeferCleanup(th.DeleteConfigMap, dnsDataCM)
			DeferCleanup(th.DeleteInstance, instance)
		})

		It("should have the Spec and Status fields initialized", func() {
			instance := GetDNSMasq(dnsMasqName)
			Expect(instance.Status.Hash).To(BeEmpty())
			Expect(instance.Status.ReadyCount).To(Equal(int32(0)))
			Expect(instance.Spec.ContainerImage).Should(Equal(containerImage))
		})

		It("creates service account, role and rolebindig", func() {
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ServiceAccountReadyCondition,
				corev1.ConditionTrue,
			)
			sa := th.GetServiceAccount(dnsMasqServiceAccountName)

			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.RoleReadyCondition,
				corev1.ConditionTrue,
			)
			role := th.GetRole(dnsMasqRoleName)
			Expect(role.Rules).To(HaveLen(2))
			Expect(role.Rules[0].Resources).To(Equal([]string{"securitycontextconstraints"}))
			Expect(role.Rules[1].Resources).To(Equal([]string{"pods"}))

			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.RoleBindingReadyCondition,
				corev1.ConditionTrue,
			)
			binding := th.GetRoleBinding(dnsMasqRoleBindingName)
			Expect(binding.RoleRef.Name).To(Equal(role.Name))
			Expect(binding.Subjects).To(HaveLen(1))
			Expect(binding.Subjects[0].Name).To(Equal(sa.Name))
		})

		It("generated a ConfigMap holding dnsmasq key=>value config options", func() {
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionTrue,
			)

			configData := th.GetConfigMap(dnsMasqName)
			Expect(configData).ShouldNot(BeNil())
			Expect(configData.Data[dnsMasqName.Name]).Should(
				ContainSubstring("server=1.1.1.1"))
			Expect(configData.Labels["dnsmasq.openstack.org/name"]).To(Equal(dnsMasqName.Name))
		})

		It("stored the input hash in the Status", func() {
			Eventually(func(g Gomega) {
				instance := GetDNSMasq(dnsMasqName)
				g.Expect(instance.Status.Hash).Should(HaveKeyWithValue("input", Not(BeEmpty())))
			}, timeout, interval).Should(Succeed())
		})

		It("exposes the service", func() {
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ExposeServiceReadyCondition,
				corev1.ConditionTrue,
			)

			svc := th.GetService(types.NamespacedName{
				Namespace: namespace,
				Name:      fmt.Sprintf("dnsmasq-%s-ctlplane", dnsMasqName.Name)})
			Expect(svc.Labels["service"]).To(Equal("dnsmasq"))
		})

		It("creates a Deployment for the service", func() {

			th.ExpectConditionWithDetails(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)

			deploymentName = types.NamespacedName{
				Name:      fmt.Sprintf("dnsmasq-%s", dnsMasqName.Name),
				Namespace: namespace,
			}

			Eventually(func(g Gomega) {
				depl := th.GetDeployment(deploymentName)

				g.Expect(int(*depl.Spec.Replicas)).To(Equal(1))
				g.Expect(depl.Spec.Template.Spec.Volumes).To(HaveLen(3))
				g.Expect(depl.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(depl.Spec.Template.Spec.InitContainers).To(HaveLen(1))
				g.Expect(depl.Spec.Selector.MatchLabels).To(Equal(map[string]string{"service": "dnsmasq"}))

				container := depl.Spec.Template.Spec.Containers[0]
				g.Expect(container.VolumeMounts).To(HaveLen(3))
				g.Expect(container.Image).To(Equal(containerImage))

				g.Expect(container.LivenessProbe.TCPSocket.Port.IntVal).To(Equal(int32(53)))
				g.Expect(container.ReadinessProbe.TCPSocket.Port.IntVal).To(Equal(int32(53)))
			}, timeout, interval).Should(Succeed())
		})

		When("the CR is deleted", func() {
			It("deletes the generated ConfigMaps", func() {
				th.ExpectCondition(
					dnsMasqName,
					ConditionGetterFunc(DNSMasqConditionGetter),
					condition.ServiceConfigReadyCondition,
					corev1.ConditionTrue,
				)

				th.DeleteInstance(GetDNSMasq(dnsMasqName))

				Eventually(func() []corev1.ConfigMap {
					return th.ListConfigMaps(dnsMasqName.Name).Items
				}, timeout, interval).Should(BeEmpty())
			})
		})
	})
})
