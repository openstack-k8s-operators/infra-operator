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
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
)

const (
	dnsMasqDefaultName = "dnsmasq-0"
)

func aggregateShardName(name, data string, shard int) string {
	generation, err := util.ObjectHash(data)
	Expect(err).NotTo(HaveOccurred())
	return dnsmasq.CNAMEShardConfigMapName(name, generation, shard)
}

var _ = Describe("DNSMasq controller", func() {
	var dnsMasqName types.NamespacedName
	var dnsMasqServiceAccountName types.NamespacedName
	var dnsMasqRoleName types.NamespacedName
	var dnsMasqRoleBindingName types.NamespacedName
	var deploymentName types.NamespacedName
	var dnsDataCM types.NamespacedName
	var dnsmasqTopologies []types.NamespacedName

	It("does not overwrite a selected DNSData ConfigMap with the reserved CNAME name", func() {
		name := types.NamespacedName{Namespace: namespace, Name: "collision-dnsmasq"}
		cnameName := types.NamespacedName{Namespace: namespace, Name: aggregateShardName(name.Name, "", 0)}
		Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      cnameName.Name,
				Labels:    map[string]string{networkv1.DNSDataLabelSelectorKey: "dnsdata"},
			},
			Data: map[string]string{cnameName.Name: "192.0.2.1 keep.example.com"},
		})).Should(Succeed())
		DeferCleanup(th.DeleteConfigMap, cnameName)
		instance := CreateDNSMasqWithName(name.Name, namespace, GetDefaultDNSMasqSpec())
		DeferCleanup(th.DeleteInstance, instance)

		Eventually(func(g Gomega) {
			cond := GetDNSMasq(name).Status.Conditions.Get(condition.ServiceConfigReadyCondition)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			g.Expect(cond.Message).To(ContainSubstring("conflicts with the reserved CNAME ConfigMap name"))
		}, timeout, interval).Should(Succeed())
		cm := th.GetConfigMap(cnameName)
		Expect(cm.Data).To(HaveKeyWithValue(cnameName.Name, "192.0.2.1 keep.example.com"))
		Expect(cm.OwnerReferences).To(BeEmpty())
	})

	It("reconciles after an unowned reserved CNAME ConfigMap is removed", func() {
		name := types.NamespacedName{Namespace: namespace, Name: "unowned-cnames"}
		cnameName := types.NamespacedName{Namespace: namespace, Name: aggregateShardName(name.Name, "", 0)}
		Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: cnameName.Name},
			Data:       map[string]string{"keep": "foreign data"},
		})).Should(Succeed())
		DeferCleanup(th.DeleteConfigMap, cnameName)
		instance := CreateDNSMasqWithName(name.Name, namespace, GetDefaultDNSMasqSpec())
		DeferCleanup(th.DeleteInstance, instance)

		Eventually(func(g Gomega) {
			cond := GetDNSMasq(name).Status.Conditions.Get(condition.ServiceConfigReadyCondition)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
			g.Expect(cond.Message).To(ContainSubstring("existing ConfigMap is not controlled by the requested owner"))
		}, timeout, interval).Should(Succeed())
		foreign := th.GetConfigMap(cnameName)
		Expect(foreign.Data).To(HaveKeyWithValue("keep", "foreign data"))
		Expect(foreign.OwnerReferences).To(BeEmpty())
		Expect(th.K8sClient.Delete(ctx, foreign)).Should(Succeed())

		Eventually(func(g Gomega) {
			cm := th.GetConfigMap(cnameName)
			g.Expect(cm.Data).To(HaveKeyWithValue(dnsmasq.CNAMEShardKey(0), ""))
			g.Expect(metav1.IsControlledBy(cm, GetDNSMasq(name))).To(BeTrue())
		}, timeout, interval).Should(Succeed())
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
			deploymentName = types.NamespacedName{
				Namespace: namespace,
				Name:      "dnsmasq-" + dnsMasqName.Name,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]any{
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
			// dnsmasq runs under the default restricted-v2 SCC (random UID),
			// so its Role carries no securitycontextconstraints "use" grant.
			role := th.GetRole(dnsMasqRoleName)
			Expect(role.Rules).To(BeEmpty())

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
			Expect(configData.Data[dnsMasqName.Name]).Should(
				ContainSubstring(fmt.Sprintf("local=/%s.svc/", namespace)))
			Expect(configData.Labels["dnsmasq.openstack.org/name"]).To(Equal(dnsMasqName.Name))
			Expect(configData.Data).NotTo(HaveKey(dnsmasq.CNAMEConfigMapKey))

			cnameData := th.GetConfigMap(types.NamespacedName{Namespace: namespace, Name: aggregateShardName(dnsMasqName.Name, "", 0)})
			Expect(cnameData.Data).To(HaveKeyWithValue(dnsmasq.CNAMEShardKey(0), ""))
			Expect(cnameData.Labels).To(HaveKeyWithValue(dnsmasq.CNAMEShardLabel, dnsMasqName.Name))
			Expect(cnameData.Labels).NotTo(HaveKey(networkv1.DNSDataLabelSelectorKey))
		})

		When("A CNAME alias matches a selected host record after DNSMasq reaches Ready", func() {
			It("filters the alias and keeps DNSMasq Ready", func() {
				cnameSource := types.NamespacedName{Namespace: namespace, Name: "selected-cnames"}
				Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      cnameSource.Name,
						Labels:    map[string]string{networkv1.DNSDataLabelSelectorKey: "dnsdata"},
					},
					Data: map[string]string{dnsmasq.CNAMEConfigMapKey: "cname=shared.example.com,host-one.example.com\n"},
				})).Should(Succeed())
				DeferCleanup(th.DeleteConfigMap, cnameSource)

				otherDNSDataCM := types.NamespacedName{
					Namespace: namespace,
					Name:      "other-dnsdata",
				}
				Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: otherDNSDataCM.Namespace,
						Name:      otherDNSDataCM.Name,
						Labels:    map[string]string{networkv1.DNSDataLabelSelectorKey: "dnsdata"},
					},
					Data: map[string]string{
						otherDNSDataCM.Name:       "172.20.0.81 host-two.example.com",
						dnsmasq.CNAMEConfigMapKey: "cname=other-alias.example.com,host-two.example.com\n",
					},
				})).Should(Succeed())
				DeferCleanup(th.DeleteConfigMap, otherDNSDataCM)

				th.SimulateLoadBalancerServiceIP(deploymentName)
				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					g.Expect(depl.Spec.Template.Spec.Volumes).To(
						ContainElement(HaveField("Name", Equal(otherDNSDataCM.Name))))
				}, timeout, interval).Should(Succeed())
				th.SimulateDeploymentReplicaReady(deploymentName)
				th.ExpectCondition(
					dnsMasqName,
					ConditionGetterFunc(DNSMasqConditionGetter),
					condition.ReadyCondition,
					corev1.ConditionTrue,
				)

				depl := th.GetDeployment(deploymentName)
				originalConfigHash := GetEnvVarValue(depl.Spec.Template.Spec.Containers[0].Env, "CONFIG_HASH", "")
				Expect(originalConfigHash).NotTo(BeEmpty())

				cm := th.GetConfigMap(cnameSource)
				cm.Data[dnsmasq.CNAMEConfigMapKey] = "cname=host-two.example.com,host-one.example.com\n"
				Expect(th.K8sClient.Update(ctx, cm)).Should(Succeed())

				Eventually(func(g Gomega) {
					depl := th.GetDeployment(deploymentName)
					newConfigHash := GetEnvVarValue(depl.Spec.Template.Spec.Containers[0].Env, "CONFIG_HASH", "")
					g.Expect(newConfigHash).NotTo(Equal(originalConfigHash))
				}, timeout, interval).Should(Succeed())

				th.SimulateDeploymentReplicaReady(deploymentName)
				th.ExpectCondition(
					dnsMasqName,
					ConditionGetterFunc(DNSMasqConditionGetter),
					condition.ServiceConfigReadyCondition,
					corev1.ConditionTrue,
				)
				th.ExpectCondition(
					dnsMasqName,
					ConditionGetterFunc(DNSMasqConditionGetter),
					condition.DeploymentReadyCondition,
					corev1.ConditionTrue,
				)
				th.ExpectCondition(
					dnsMasqName,
					ConditionGetterFunc(DNSMasqConditionGetter),
					condition.ReadyCondition,
					corev1.ConditionTrue,
				)

				configData := th.GetConfigMap(dnsMasqName)
				Expect(configData.Data).NotTo(HaveKey(dnsmasq.CNAMEConfigMapKey))
				cnameData := th.GetConfigMap(types.NamespacedName{Namespace: namespace, Name: aggregateShardName(dnsMasqName.Name, "cname=other-alias.example.com,host-two.example.com\n", 0)})
				Expect(cnameData.Data[dnsmasq.CNAMEShardKey(0)]).ShouldNot(
					ContainSubstring("cname=host-two.example.com,host-one.example.com"))
				Expect(cnameData.Data[dnsmasq.CNAMEShardKey(0)]).Should(
					ContainSubstring("cname=other-alias.example.com,host-two.example.com"))
			})
		})

		It("splits large CNAME aggregates and removes stale shards after rollout", func() {
			oldBudget := dnsmasq.CNAMEConfigMapMaxBytes
			dnsmasq.CNAMEConfigMapMaxBytes = 8 * 1024
			DeferCleanup(func() { dnsmasq.CNAMEConfigMapMaxBytes = oldBudget })
			th.SimulateLoadBalancerServiceIP(deploymentName)
			firstDNSDataCM := types.NamespacedName{Namespace: namespace, Name: "large-cname-source"}
			otherDNSDataCM := types.NamespacedName{Namespace: namespace, Name: "other-dnsdata"}

			longDomain := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60)
			makeCNAMEs := func(start, end int) string {
				var data strings.Builder
				for i := start; i < end; i++ {
					fmt.Fprintf(&data, "cname=alias-%04d.%s,target.%s\n", i, longDomain, longDomain)
				}
				return data.String()
			}
			firstHalf := makeCNAMEs(0, 20)
			secondHalf := makeCNAMEs(20, 40)
			oldShard := types.NamespacedName{Namespace: namespace, Name: aggregateShardName(dnsMasqName.Name, firstHalf, 0)}
			shard0 := types.NamespacedName{Namespace: namespace, Name: aggregateShardName(dnsMasqName.Name, firstHalf+secondHalf, 0)}
			shard1 := types.NamespacedName{Namespace: namespace, Name: aggregateShardName(dnsMasqName.Name, firstHalf+secondHalf, 1)}
			Expect(len(firstHalf)).To(BeNumerically("<", dnsmasq.CNAMEConfigMapMaxBytes))
			Expect(len(firstHalf) + len(secondHalf)).To(BeNumerically(">", dnsmasq.CNAMEConfigMapMaxBytes))

			Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      firstDNSDataCM.Name,
					Labels:    map[string]string{networkv1.DNSDataLabelSelectorKey: "dnsdata"},
				},
				Data: map[string]string{dnsmasq.CNAMEConfigMapKey: firstHalf},
			})).Should(Succeed())
			DeferCleanup(th.DeleteConfigMap, firstDNSDataCM)
			Eventually(func(g Gomega) {
				g.Expect(th.GetConfigMap(oldShard).Data[dnsmasq.CNAMEShardKey(0)]).To(Equal(firstHalf))
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				depl := th.GetDeployment(deploymentName)
				g.Expect(depl.Spec.Template.Spec.Volumes).To(ContainElement(
					HaveField("Projected", HaveField("Sources", ContainElement(
						HaveField("ConfigMap", HaveField("Name", Equal(oldShard.Name))))))))
			}, timeout, interval).Should(Succeed())
			Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: shard1.Name},
				Data:       map[string]string{"keep": "foreign data"},
			})).Should(Succeed())
			Expect(th.K8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      otherDNSDataCM.Name,
					Labels:    map[string]string{networkv1.DNSDataLabelSelectorKey: "dnsdata"},
				},
				Data: map[string]string{otherDNSDataCM.Name: "", dnsmasq.CNAMEConfigMapKey: secondHalf},
			})).Should(Succeed())
			DeferCleanup(th.DeleteConfigMap, otherDNSDataCM)
			Eventually(func(g Gomega) {
				cond := GetDNSMasq(dnsMasqName).Status.Conditions.Get(condition.ServiceConfigReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("existing ConfigMap is not controlled"))
			}, timeout, interval).Should(Succeed())
			Expect(th.GetConfigMap(oldShard).Data[dnsmasq.CNAMEShardKey(0)]).To(Equal(firstHalf))
			Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.Volumes).To(ContainElement(
				HaveField("Projected", HaveField("Sources", ContainElement(
					HaveField("ConfigMap", HaveField("Name", Equal(oldShard.Name))))))))
			Expect(th.K8sClient.Delete(ctx, th.GetConfigMap(shard1))).Should(Succeed())
			Eventually(func(g Gomega) {
				first := th.GetConfigMap(shard0).Data[dnsmasq.CNAMEShardKey(0)]
				second := th.GetConfigMap(shard1).Data[dnsmasq.CNAMEShardKey(1)]
				g.Expect(first + second).To(Equal(firstHalf + secondHalf))
				g.Expect(len(first)).To(BeNumerically("<=", dnsmasq.CNAMEConfigMapMaxBytes))
				g.Expect(len(second)).To(BeNumerically("<=", dnsmasq.CNAMEConfigMapMaxBytes))
				depl := th.GetDeployment(deploymentName)
				found := false
				for _, volume := range depl.Spec.Template.Spec.Volumes {
					if volume.Projected != nil {
						found = true
						g.Expect(volume.Projected.Sources).To(HaveLen(2))
						g.Expect(volume.Projected.Sources[0].ConfigMap.Name).To(Equal(shard0.Name))
						g.Expect(volume.Projected.Sources[1].ConfigMap.Name).To(Equal(shard1.Name))
					}
				}
				g.Expect(found).To(BeTrue(), "projected CNAME volume not found")
			}, timeout, interval).Should(Succeed())
			th.ExpectCondition(dnsMasqName, ConditionGetterFunc(DNSMasqConditionGetter), condition.ServiceConfigReadyCondition, corev1.ConditionTrue)
			th.SimulateDeploymentReplicaReady(deploymentName)

			other := th.GetConfigMap(otherDNSDataCM)
			other.Data[dnsmasq.CNAMEConfigMapKey] = ""
			Expect(th.K8sClient.Update(ctx, other)).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.Volumes).To(ContainElement(
					HaveField("Projected", HaveField("Sources", HaveLen(1)))))
			}, timeout, interval).Should(Succeed())
			Expect(th.GetConfigMap(shard1).Data).To(HaveKey(dnsmasq.CNAMEShardKey(1)))
			Expect(th.GetConfigMap(oldShard).Data[dnsmasq.CNAMEShardKey(0)]).To(Equal(firstHalf))
			th.SimulateDeploymentReplicaReady(deploymentName)
			Eventually(func() bool {
				return k8s_errors.IsNotFound(th.K8sClient.Get(ctx, shard1, &corev1.ConfigMap{}))
			}, timeout, interval).Should(BeTrue())
			Expect(th.GetConfigMap(oldShard).Data[dnsmasq.CNAMEShardKey(0)]).To(Equal(firstHalf))
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
				g.Expect(depl.Spec.Template.Spec.Volumes).To(ContainElement(And(
					HaveField("Name", Equal("cnames")),
					HaveField("Projected", HaveField("Sources", ContainElement(HaveField("ConfigMap", HaveField("Name", Equal(aggregateShardName(dnsMasqName.Name, "", 0))))))),
				)))
				g.Expect(depl.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(depl.Spec.Template.Spec.InitContainers).To(HaveLen(1))
				g.Expect(depl.Spec.Selector.MatchLabels).To(Equal(map[string]string{"service": "dnsmasq"}))

				container := depl.Spec.Template.Spec.Containers[0]
				g.Expect(container.VolumeMounts).To(HaveLen(3))
				g.Expect(container.VolumeMounts).To(ContainElement(HaveField(
					"MountPath", Equal("/etc/dnsmasq.d/cnames"))))
				g.Expect(container.Image).To(Equal(containerImage))

				g.Expect(container.LivenessProbe.TCPSocket.Port.IntVal).To(Equal(int32(5353)))
				g.Expect(container.ReadinessProbe.TCPSocket.Port.IntVal).To(Equal(int32(5353)))

				// the service container runs quiet (no verbose logging), while the
				// init container validates the config with --log-debug --test
				g.Expect(container.Args).ToNot(ContainElement(ContainSubstring("--log-debug")))
				initContainer := depl.Spec.Template.Spec.InitContainers[0]
				g.Expect(initContainer.Args).To(ContainElement(ContainSubstring("--log-debug --test")))
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

	When("A DNSMasq is created with a custom local option", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSMasqSpec()
			spec["options"] = any([]networkv1.DNSMasqOption{
				{
					Key:    "server",
					Values: []string{"1.1.1.1"},
				},
				{
					Key:    "no-negcache",
					Values: []string{},
				},
				{
					Key:    "local",
					Values: []string{"/custom.svc/"},
				},
			})
			instance := CreateDNSMasq(namespace, spec)
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]any{
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

		It("uses the custom local value and does not add the default", func() {
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionTrue,
			)

			configData := th.GetConfigMap(dnsMasqName)
			Expect(configData).ShouldNot(BeNil())
			Expect(configData.Data[dnsMasqName.Name]).Should(
				ContainSubstring("local=/custom.svc/"))
			Expect(configData.Data[dnsMasqName.Name]).ShouldNot(
				ContainSubstring(fmt.Sprintf("local=/%s.svc/", namespace)))
		})
	})

	When("A DNSMasq is created with log-queries opted in", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSMasqSpec()
			spec["options"] = any([]networkv1.DNSMasqOption{
				{
					Key:    "server",
					Values: []string{"1.1.1.1"},
				},
				{
					// value-less flag: Values omitted entirely
					Key: "log-queries",
				},
			})
			instance := CreateDNSMasq(namespace, spec)
			dnsMasqName = types.NamespacedName{
				Name:      instance.GetName(),
				Namespace: namespace,
			}

			dnsDataCM = types.NamespacedName{
				Namespace: namespace,
				Name:      "some-dnsdata",
			}

			th.CreateConfigMap(dnsDataCM, map[string]any{
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

		It("renders log-queries as a bare directive in the config", func() {
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ServiceConfigReadyCondition,
				corev1.ConditionTrue,
			)

			configData := th.GetConfigMap(dnsMasqName)
			Expect(configData).ShouldNot(BeNil())
			// value-less option renders as a bare directive, no trailing "="
			Expect(configData.Data[dnsMasqName.Name]).Should(
				ContainSubstring("log-queries\n"))
			Expect(configData.Data[dnsMasqName.Name]).ShouldNot(
				ContainSubstring("log-queries="))
		})
	})

	When("Deployment rollout is progressing", func() {
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

			th.CreateConfigMap(dnsDataCM, map[string]any{
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
			th.SimulateDeploymentProgressing(deploymentName)
		})

		It("shows the deployment progressing in DeploymentReadyCondition", func() {
			th.ExpectConditionWithDetails(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)

			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("still shows the deployment progressing in DeploymentReadyCondition when rollout hits ProgressDeadlineExceeded", func() {
			th.SimulateDeploymentProgressDeadlineExceeded(deploymentName)
			th.ExpectConditionWithDetails(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("reaches Ready when deployment rollout finished", func() {
			th.ExpectConditionWithDetails(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)

			th.SimulateDeploymentReplicaReady(deploymentName)
			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionTrue,
			)

			th.ExpectCondition(
				dnsMasqName,
				ConditionGetterFunc(DNSMasqConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("A DNSMasq is created with nodeSelector", func() {
		BeforeEach(func() {
			spec := GetDefaultDNSMasqSpec()
			spec["nodeSelector"] = map[string]any{
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

			th.CreateConfigMap(dnsDataCM, map[string]any{
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
		var topologyRef, topologyRefAlt *topologyv1.TopoRef
		BeforeEach(func() {
			dnsmasqTopologies = GetTopologyRef(dnsMasqDefaultName, namespace)
			// Create Test Topologies
			for _, t := range dnsmasqTopologies {
				// Build topologySpec
				topologySpec, _ := GetSampleTopologySpec(dnsMasqDefaultName)
				CreateTopology(t, topologySpec)
			}
			// Define the two topology references used in this test
			topologyRef = &topologyv1.TopoRef{
				Name:      dnsmasqTopologies[0].Name,
				Namespace: dnsmasqTopologies[0].Namespace,
			}
			topologyRefAlt = &topologyv1.TopoRef{
				Name:      dnsmasqTopologies[1].Name,
				Namespace: dnsmasqTopologies[1].Namespace,
			}

			spec := GetDefaultDNSMasqSpec()
			spec["topologyRef"] = map[string]any{
				"name": topologyRef.Name,
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

			th.CreateConfigMap(dnsDataCM, map[string]any{
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
			Eventually(func(g Gomega) {
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRef.Name,
					Namespace: topologyRef.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(HaveLen(1))
				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology).ToNot(BeNil())
				g.Expect(dnsmasq.Status.LastAppliedTopology).To(Equal(topologyRef))
				g.Expect(finalizers).To(ContainElement(
					fmt.Sprintf("openstack.org/dnsmasq-%s", dnsMasqName.Name)))
			}, timeout, interval).Should(Succeed())
		})
		It("sets topology in CR deployment", func() {
			Eventually(func(g Gomega) {
				_, topologySpecObj := GetSampleTopologySpec(dnsMasqName.Name)
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.TopologySpreadConstraints).ToNot(BeNil())
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.TopologySpreadConstraints).To(Equal(topologySpecObj))
				g.Expect(th.GetDeployment(deploymentName).Spec.Template.Spec.Affinity).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})

		It("updates topology when the reference changes", func() {
			dnsmasqTopologies = GetTopologyRef(dnsMasqName.Name, namespace)
			Eventually(func(g Gomega) {
				dnsmasq := GetDNSMasq(dnsMasqName)
				dnsmasq.Spec.TopologyRef.Name = topologyRefAlt.Name
				g.Expect(k8sClient.Update(ctx, dnsmasq)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRefAlt.Name,
					Namespace: topologyRefAlt.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(HaveLen(1))

				dnsmasq := GetDNSMasq(dnsMasqName)
				g.Expect(dnsmasq.Status.LastAppliedTopology).ToNot(BeNil())
				g.Expect(dnsmasq.Status.LastAppliedTopology).To(Equal(topologyRefAlt))
				g.Expect(finalizers).To(ContainElement(
					fmt.Sprintf("openstack.org/dnsmasq-%s", dnsMasqName.Name)))
			}, timeout, interval).Should(Succeed())
		})
		It("checks the previous topology has no reference anymore", func() {
			Eventually(func(g Gomega) {
				// Verify the previous referenced topology has no finalizers
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRef.Name,
					Namespace: topologyRef.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(BeEmpty())
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

			// Verify the existing topologies have no finalizer anymore
			Eventually(func(g Gomega) {
				for _, topology := range dnsmasqTopologies {
					tp := GetTopology(types.NamespacedName{
						Name:      topology.Name,
						Namespace: topology.Namespace,
					})
					finalizers := tp.GetFinalizers()
					g.Expect(finalizers).To(BeEmpty())
				}
			}, timeout, interval).Should(Succeed())
		})
	})
})
