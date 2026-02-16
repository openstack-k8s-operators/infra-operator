/*
Copyright 2025.

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

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

var _ = Describe("RabbitMQ Proxy Sidecar", func() {
	var rabbitmqName types.NamespacedName

	BeforeEach(func() {
		rabbitmqName = types.NamespacedName{
			Name:      "rabbitmq-proxy-test",
			Namespace: namespace,
		}
		clusterCm := types.NamespacedName{Name: "cluster-config-v1", Namespace: "kube-system"}
		th.CreateConfigMap(
			clusterCm,
			map[string]any{
				"install-config": "fips: false",
			},
		)
		DeferCleanup(th.DeleteConfigMap, clusterCm)
	})

	When("RabbitMQ is being upgraded from 3.9 to 4.2 with Mirrored to Quorum migration", func() {
		It("should add proxy sidecar during upgrade", func() {
			// Start with Mirrored queues on 3.9
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored" // Start with Mirrored
			spec["tls"] = map[string]any{
				"secretName": "rabbitmq-tls",
			}

			// Create TLS secret
			tlsSecretName := types.NamespacedName{Name: "rabbitmq-tls", Namespace: namespace}
			tlsSecret := th.CreateSecret(
				tlsSecretName,
				map[string][]byte{
					"tls.crt": []byte("fake-cert"),
					"tls.key": []byte("fake-key"),
				},
			)
			DeferCleanup(th.DeleteInstance, tlsSecret)

			// Create RabbitMQ with Mirrored queues (simulating 3.9 cluster)
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Wait for initial reconciliation
			var instance *rabbitmqv1.RabbitMq
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Step 1: Set status to simulate we're on 3.9 with Mirrored queues
			// Status.QueueType is initially empty - we set it to "Mirrored" to simulate pre-upgrade state
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Status.CurrentVersion = "3.9.0"
				instance.Status.QueueType = "Mirrored"
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Step 2: Update spec to trigger "upgrade" to Quorum with target version
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations[rabbitmqv1.AnnotationTargetVersion] = "4.2.0"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Step 3: Set upgrade phase (controller would do this, but we simulate it)
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Status.UpgradePhase = "WaitingForCluster"
				// Make sure these are still set (controller might have changed them)
				instance.Status.CurrentVersion = "3.9.0"
				instance.Status.QueueType = "Mirrored"
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Trigger reconciliation
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy sidecar was added
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(cluster).ToNot(BeNil())

				// Check for proxy container
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					podSpec := cluster.Spec.Override.StatefulSet.Spec.Template.Spec

					// Look for proxy container
					foundProxy := false
					for _, container := range podSpec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							// Verify container configuration
							g.Expect(container.Image).ToNot(BeEmpty(), "proxy container image should be set")
							g.Expect(container.Command).To(ContainElement("python3"))
							g.Expect(container.Command).To(ContainElement("/scripts/proxy.py"))
							g.Expect(container.Args).To(ContainElement("--backend"))
							g.Expect(container.Args).To(ContainElement("localhost:5673"))
							g.Expect(container.Args).To(ContainElement("--listen"))
							g.Expect(container.Args).To(ContainElement("0.0.0.0:5671"))

							// Verify TLS args
							g.Expect(container.Args).To(ContainElement("--tls-cert"))
							g.Expect(container.Args).To(ContainElement("/etc/rabbitmq-tls/tls.crt"))
							g.Expect(container.Args).To(ContainElement("--tls-key"))
							g.Expect(container.Args).To(ContainElement("/etc/rabbitmq-tls/tls.key"))

							// Verify volume mounts
							foundScriptMount := false
							foundTLSMount := false
							for _, mount := range container.VolumeMounts {
								if mount.Name == "proxy-script" && mount.MountPath == "/scripts" {
									foundScriptMount = true
								}
								if mount.Name == "rabbitmq-tls" && mount.MountPath == "/etc/rabbitmq-tls" {
									foundTLSMount = true
								}
							}
							g.Expect(foundScriptMount).To(BeTrue(), "proxy-script volume mount not found")
							g.Expect(foundTLSMount).To(BeTrue(), "rabbitmq-tls volume mount not found")
							break
						}
					}
					g.Expect(foundProxy).To(BeTrue(), "amqp-proxy container not found in StatefulSet")

					// Verify proxy-script volume exists
					foundVolume := false
					for _, vol := range podSpec.Volumes {
						if vol.Name == "proxy-script" {
							foundVolume = true
							g.Expect(vol.ConfigMap).ToNot(BeNil())
							g.Expect(vol.ConfigMap.Name).To(Equal(rabbitmqName.Name + "-proxy-script"))
							break
						}
					}
					g.Expect(foundVolume).To(BeTrue(), "proxy-script volume not found")
				}

				// Verify RabbitMQ backend port configuration
				g.Expect(cluster.Spec.Rabbitmq.AdditionalConfig).To(ContainSubstring("listeners.tcp.1 = 127.0.0.1:5673"))
			}, timeout, interval).Should(Succeed())

			// Verify ConfigMap was created
			Eventually(func(g Gomega) {
				cm := &corev1.ConfigMap{}
				cmName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-proxy-script",
					Namespace: rabbitmqName.Namespace,
				}
				g.Expect(k8sClient.Get(ctx, cmName, cm)).To(Succeed())
				g.Expect(cm.Data).To(HaveKey("proxy.py"))
				g.Expect(cm.Data["proxy.py"]).To(ContainSubstring("AMQP"))
				g.Expect(cm.Data["proxy.py"]).To(ContainSubstring("durable"))
			}, timeout, interval).Should(Succeed())
		})

		It("should remove proxy sidecar when clients-reconfigured annotation is set", func() {
			// Start with Mirrored queues on 3.9
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"

			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Wait for initial reconciliation
			var instance *rabbitmqv1.RabbitMq
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Set status to simulate 3.9 with Mirrored queues
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Status.CurrentVersion = "3.9.0"
				instance.Status.QueueType = "Mirrored"
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Update spec to trigger upgrade
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations[rabbitmqv1.AnnotationTargetVersion] = "4.2.0"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Set upgrade phase
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				instance.Status.UpgradePhase = "WaitingForCluster"
				instance.Status.CurrentVersion = "3.9.0"
				instance.Status.QueueType = "Mirrored"
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy was added
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					foundProxy := false
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							break
						}
					}
					g.Expect(foundProxy).To(BeTrue())
				}
			}, timeout, interval).Should(Succeed())

			// Mark clients as reconfigured
			// This annotation takes precedence in shouldEnableProxy() logic,
			// so proxy will be removed regardless of upgrade phase
			// Use Eventually to handle conflicts from controller reconciliation
			Eventually(func(g Gomega) {
				instance = GetRabbitMQ(rabbitmqName)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations["rabbitmq.openstack.org/clients-reconfigured"] = "true"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Trigger reconciliation
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy was removed
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					foundProxy := false
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							break
						}
					}
					g.Expect(foundProxy).To(BeFalse(), "proxy should be removed after clients-reconfigured")
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("enable-proxy annotation is set", func() {
		It("should enable proxy even without upgrade phase", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Quorum"

			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Set enable-proxy annotation
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations["rabbitmq.openstack.org/enable-proxy"] = "true"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy was added
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					foundProxy := false
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							break
						}
					}
					g.Expect(foundProxy).To(BeTrue(), "proxy should be enabled with enable-proxy annotation")
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("cluster is already using Quorum queues", func() {
		It("should NOT add proxy sidecar during upgrade", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Quorum"

			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Set upgrade phase but already using Quorum queues (no migration needed)
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations[rabbitmqv1.AnnotationTargetVersion] = "4.2.0"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				instance.Status.UpgradePhase = "WaitingForCluster"
				instance.Status.CurrentVersion = "4.0.0"
				instance.Status.QueueType = "Quorum" // Already using Quorum
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy was NOT added
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					foundProxy := false
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							break
						}
					}
					g.Expect(foundProxy).To(BeFalse(), "proxy should NOT be added when already using Quorum")
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("upgrading within 4.x versions", func() {
		It("should NOT add proxy sidecar", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Quorum"

			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Upgrading from 4.0 to 4.2 (not 3.x to 4.x)
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations[rabbitmqv1.AnnotationTargetVersion] = "4.2.0"
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				instance.Status.UpgradePhase = "WaitingForCluster"
				instance.Status.CurrentVersion = "4.0.0"
				instance.Status.QueueType = "Mirrored"
				g.Expect(k8sClient.Status().Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy was NOT added (not upgrading from 3.x)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				if cluster.Spec.Override.StatefulSet != nil &&
					cluster.Spec.Override.StatefulSet.Spec != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template != nil &&
					cluster.Spec.Override.StatefulSet.Spec.Template.Spec != nil {
					foundProxy := false
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers {
						if container.Name == "amqp-proxy" {
							foundProxy = true
							break
						}
					}
					g.Expect(foundProxy).To(BeFalse(), "proxy should NOT be added when not upgrading from 3.x to 4.x")
				}
			}, timeout, interval).Should(Succeed())
		})
	})
})
