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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
)

const (
	dnsMasqDefaultName = "dnsmasq-0"
)

var _ = Describe("DNSMasq controller", func() {
	var dnsMasqName types.NamespacedName
	var dnsMasqServiceAccountName types.NamespacedName
	var dnsMasqRoleName types.NamespacedName
	var dnsMasqRoleBindingName types.NamespacedName
	var deploymentName types.NamespacedName
	var dnsDataCM types.NamespacedName
	var dnsmasqTopologies []types.NamespacedName

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
			deploymentName = types.NamespacedName{
				Namespace: namespace,
				Name:      "dnsmasq-" + dnsMasqName.Name,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]interface{}{
				dnsDataCM.Name: "172.20.0.80 keystone-internal.openstack.svc",
			})
			cm := th.GetConfigMap(dnsDataCM)
			cm.Labels = util.MergeStringMaps(cm.Labels, map[string]string{
				"dnsmasqhosts": "dnsdata",
			})
			Expect(th.K8sClient.Update(ctx, cm)).Should(Succeed())

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
			Expect(configData.Data[dnsMasqName.Name]).Should(
				ContainSubstring("no-negcache\n"))
			Expect(configData.Labels["dnsmasq.openstack.org/name"]).To(Equal(dnsMasqName.Name))
		})

		It("stored the input hash in the Status", func() {
			Eventually(func(g Gomega) {
				instance := GetDNSMasq(dnsMasqName)
				g.Expect(instance.Status.Hash).Should(HaveKeyWithValue("input", Not(BeEmpty())))
			}, timeout, interval).Should(Succeed())
		})

		It("exposes the service", func() {
			th.SimulateLoadBalancerServiceIP(deploymentName)
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.CreateServiceReadyCondition,
				corev1.ConditionTrue,
			)

			svc := th.GetService(types.NamespacedName{
				Namespace: namespace,
				Name:      fmt.Sprintf("dnsmasq-%s", dnsMasqName.Name)})
			Expect(svc.Labels["service"]).To(Equal("dnsmasq"))
		})

		It("creates a Deployment for the service", func() {
			th.SimulateLoadBalancerServiceIP(deploymentName)
			th.ExpectConditionWithDetails(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)

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

				g.Expect(container.LivenessProbe.TCPSocket.Port.IntVal).To(Equal(int32(5353)))
				g.Expect(container.ReadinessProbe.TCPSocket.Port.IntVal).To(Equal(int32(5353)))
			}, timeout, interval).Should(Succeed())
		})

		When("the DNSData CM gets updated", func() {
			It("the CONFIG_HASH on the deployment changes", func() {
				th.SimulateLoadBalancerServiceIP(deploymentName)
				cm := th.GetConfigMap(dnsDataCM)
				configHash := ""
				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					g.Expect(depl.Spec.Template.Spec.Volumes).To(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))

					container := depl.Spec.Template.Spec.Containers[0]
					g.Expect(container.VolumeMounts).To(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))

					configHash = GetEnvVarValue(container.Env, "CONFIG_HASH", "")
					g.Expect(configHash).To(Not(Equal("")))
				}, timeout, interval).Should(Succeed())

				// Update the cm providing dnsdata
				cm.Data[dnsDataCM.Name] = "172.20.0.80 keystone-internal.openstack.svc some-other-node"
				Expect(th.K8sClient.Update(ctx, cm)).Should(Succeed())

				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					container := depl.Spec.Template.Spec.Containers[0]
					newConfigHash := GetEnvVarValue(container.Env, "CONFIG_HASH", "")
					g.Expect(newConfigHash).To(Not(Equal("")))
					g.Expect(newConfigHash).To(Not(Equal(configHash)))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("the DNSData CM gets deleted", func() {
			It("the ConfigMap gets removed from the deployment", func() {
				th.SimulateLoadBalancerServiceIP(deploymentName)
				th.GetConfigMap(dnsDataCM)
				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					g.Expect(depl.Spec.Template.Spec.Volumes).To(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))

					container := depl.Spec.Template.Spec.Containers[0]
					g.Expect(container.VolumeMounts).To(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))
				}, timeout, interval).Should(Succeed())

				// Delete the cm providing dnsdata
				th.DeleteConfigMap(dnsDataCM)
				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					g.Expect(depl.Spec.Template.Spec.Volumes).NotTo(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))

					container := depl.Spec.Template.Spec.Containers[0]
					g.Expect(container.VolumeMounts).NotTo(
						ContainElement(HaveField("Name", Equal(dnsDataCM.Name))))
				}, timeout, interval).Should(Succeed())
			})
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

	When("A DNSMasq is created with nodeSelector", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSMasqSpec()
			spec["nodeSelector"] = map[string]interface{}{
				"foo": "bar",
			}
			instance := CreateDNSMasq(namespace, spec)
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
			deploymentName = types.NamespacedName{
				Namespace: namespace,
				Name:      "dnsmasq-" + dnsMasqName.Name,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]interface{}{
				dnsDataCM.Name: "172.20.0.80 keystone-internal.openstack.svc",
			})
			cm := th.GetConfigMap(dnsDataCM)
			cm.Labels = util.MergeStringMaps(cm.Labels, map[string]string{
				"dnsmasqhosts": "dnsdata",
			})
			Expect(th.K8sClient.Update(ctx, cm)).Should(Succeed())

			DeferCleanup(th.DeleteConfigMap, dnsDataCM)
			DeferCleanup(th.DeleteInstance, instance)
			th.SimulateLoadBalancerServiceIP(deploymentName)
		})

		It("sets nodeSelector in resource specs", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(Equal(map[string]string{"foo": "bar"}))
			}, timeout, interval).Should(Succeed())
		})

		It("updates nodeSelector in resource specs when changed", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(Equal(map[string]string{"foo": "bar"}))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				newNodeSelector := map[string]string{
					"foo2": "bar2",
				}
				dnsmasq.Spec.NodeSelector = &newNodeSelector
				g.Expect(k8sClient.Update(ctx, dnsmasq)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(Equal(map[string]string{"foo2": "bar2"}))
			}, timeout, interval).Should(Succeed())
		})

		It("removes nodeSelector from resource specs when cleared", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(Equal(map[string]string{"foo": "bar"}))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				emptyNodeSelector := map[string]string{}
				dnsmasq.Spec.NodeSelector = &emptyNodeSelector
				g.Expect(k8sClient.Update(ctx, dnsmasq)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})

		It("removes nodeSelector from resource specs when nilled", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(Equal(map[string]string{"foo": "bar"}))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				dnsmasq.Spec.NodeSelector = nil
				g.Expect(k8sClient.Update(ctx, dnsmasq)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.NodeSelector).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})
	})
	When("A DNSMasq is created with topologyref", func() {
		BeforeEach(func() {

			dnsmasqTopologies = GetTopologyRef(dnsMasqDefaultName, namespace)
			// Create Test Topologies
			topologySpec := GetSampleTopologySpec(dnsMasqDefaultName)
			for _, t := range dnsmasqTopologies {
				CreateTopology(t, topologySpec)
			}

			spec := GetDefaultDNSMasqSpec()
			spec["topologyRef"] = map[string]interface{}{
				"name": dnsmasqTopologies[0].Name,
			}
			instance := CreateDNSMasqWithName(dnsMasqDefaultName, namespace, spec)
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
			deploymentName = types.NamespacedName{
				Namespace: namespace,
				Name:      "dnsmasq-" + dnsMasqName.Name,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]interface{}{
				dnsDataCM.Name: "172.20.0.80 keystone-internal.openstack.svc",
			})
			cm := th.GetConfigMap(dnsDataCM)
			cm.Labels = util.MergeStringMaps(cm.Labels, map[string]string{
				"dnsmasqhosts": "dnsdata",
			})
			Expect(th.K8sClient.Update(ctx, cm)).Should(Succeed())

			DeferCleanup(th.DeleteConfigMap, dnsDataCM)
			DeferCleanup(th.DeleteInstance, instance)
			th.SimulateLoadBalancerServiceIP(deploymentName)
		})

		It("sets topology in CR status", func() {
			dnsmasqTopologies = GetTopologyRef(dnsMasqName.Name, namespace)
			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology.Name).To(Equal(dnsmasqTopologies[0].Name))
			}, timeout, interval).Should(Succeed())
		})
		It("sets topology in CR deployment", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.TopologySpreadConstraints).ToNot(BeNil())
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.Affinity).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})

		It("updates topology when the reference changes", func() {
			dnsmasqTopologies = GetTopologyRef(dnsMasqName.Name, namespace)
			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				dnsmasq.Spec.TopologyRef.Name = dnsmasqTopologies[1].Name
				g.Expect(k8sClient.Update(ctx, dnsmasq)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology.Name).To(Equal(dnsmasqTopologies[1].Name))
			}, timeout, interval).Should(Succeed())
		})
		It("removes topologyRef from the spec", func() {
			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				// Remove the TopologyRef from the existing DNSMasq .Spec
				dnsmasq.Spec.TopologyRef = nil
				g.Expect(k8sClient.Update(ctx, dnsmasq)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology).Should(BeNil())
			}, timeout, interval).Should(Succeed())
		})
	})
})
