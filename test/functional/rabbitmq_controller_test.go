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
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"

	//revive:disable-next-line:dot-imports

	//. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

const (
	rabbitmqDefaultName = "rabbitmq"
)

var _ = Describe("RabbitMQ Controller", func() {
	var rabbitmqName types.NamespacedName

	BeforeEach(func() {
		rabbitmqName = types.NamespacedName{
			Name:      rabbitmqDefaultName,
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

	When("QueueType defaulting and explicit values", func() {
		It("defaults QueueType to Quorum when unspecified", func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Spec.QueueType).ToNot(BeNil())
				g.Expect(*instance.Spec.QueueType).To(Equal("Quorum"))
			}, timeout, interval).Should(Succeed())
		})

		It("preserves explicitly set QueueType", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Spec.QueueType).ToNot(BeNil())
				g.Expect(*instance.Spec.QueueType).To(Equal("Mirrored"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a default RabbitMQ gets created", func() {
		BeforeEach(func() {
			rabbitmq := CreateRabbitMQ(rabbitmqName, GetDefaultRabbitMQSpec())
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQCluster", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(cluster.Spec.TLS.SecretName).To(BeEmpty())
				g.Expect(cluster.Spec.TLS.CaSecretName).To(BeEmpty())
				g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeFalse())

				container := cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0]
				var rabbitmqServerAdditionalErlArgs string
				for _, env := range container.Env {
					if env.Name == "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS" {
						rabbitmqServerAdditionalErlArgs = env.Value
						break
					}
				}
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-proto_dist inet_tcp"))
				g.Expect(cluster.Spec.Rabbitmq.AdditionalConfig).To(ContainSubstring("prometheus.tcp.ip = ::"))
			}, timeout, interval).Should(Succeed())

		})
	})

	When("RabbitMQ gets created with TLS enabled", func() {
		var certSecret *corev1.Secret
		BeforeEach(func() {
			certSecret = CreateCertSecret(rabbitmqName)
			DeferCleanup(th.DeleteSecret, types.NamespacedName{Name: certSecret.Name, Namespace: namespace})
		})

		When("with default version (4.2)", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["tls"] = map[string]any{
					"secretName": certSecret.Name,
				}
				rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
				DeferCleanup(th.DeleteInstance, rabbitmq)
			})

			It("should have created a RabbitMQCluster with TLS enabled", func() {
				SimulateRabbitMQClusterReady(rabbitmqName)
				Eventually(func(g Gomega) {
					cluster := GetRabbitMQCluster(rabbitmqName)
					g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
					g.Expect(cluster.Spec.TLS.SecretName).To(Equal(certSecret.Name))
					g.Expect(cluster.Spec.TLS.CaSecretName).To(Equal(certSecret.Name))
					g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeTrue())
					g.Expect(cluster.Spec.Rabbitmq.AdvancedConfig).To(ContainSubstring("ssl_options"))
					g.Expect(cluster.Spec.Rabbitmq.AdditionalConfig).To(ContainSubstring("prometheus.ssl.ip = ::"))

					container := cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0]
					var rabbitmqServerAdditionalErlArgs string
					for _, env := range container.Env {
						if env.Name == "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS" {
							rabbitmqServerAdditionalErlArgs = env.Value
							break
						}
					}
					g.Expect(rabbitmqServerAdditionalErlArgs).NotTo(ContainSubstring("-crypto fips_mode true"))
					g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-proto_dist inet_tls"))
					g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-ssl_dist_optfile /etc/rabbitmq/inter-node-tls.config"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("with version 3.9", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["tls"] = map[string]any{
					"secretName": certSecret.Name,
				}
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "3.9",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)
			})

			It("should configure TLS 1.2 only for non-FIPS mode", func() {
				SimulateRabbitMQClusterReady(rabbitmqName)
				Eventually(func(g Gomega) {
					// TLS settings for Erlang and RabbitMQ endpoints (AdvancedConfig)
					// are in an Erlang data structure, so just parse the expected strings
					// for application "rabbit", "rabbitmq_management", "client" and "ssl"
					cluster := GetRabbitMQCluster(rabbitmqName)
					advancedConfig := cluster.Spec.Rabbitmq.AdvancedConfig

					g.Expect(advancedConfig).To(ContainSubstring("{ssl, [{protocol_version, ['tlsv1.2']}"))
					g.Expect(advancedConfig).To(ContainSubstring("{rabbit, ["))
					g.Expect(advancedConfig).To(ContainSubstring("{rabbitmq_management, ["))
					g.Expect(advancedConfig).To(ContainSubstring("{client, ["))
					g.Expect(strings.Count(advancedConfig, "{versions, ['tlsv1.2']}")).To(Equal(3))
					// Ensure it doesn't use TLS1.3 (as FIPS still does for the time being)
					g.Expect(strings.Count(advancedConfig, "'tlsv1.3'")).To(Equal(0))

					// TLS settings for RabbitMQ cluster communication (inter-node-tls.config)
					// should have a config for server and client. Those are in an Erlang
					// data structure, so just parse the expected string
					configMapName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-config-data", rabbitmqName.Name),
						Namespace: rabbitmqName.Namespace,
					}
					cm := th.GetConfigMap(configMapName)
					g.Expect(cm.Data).To(HaveKey("inter_node_tls.config"))
					interNodeConfig := cm.Data["inter_node_tls.config"]

					// Verify server and client configurations use TLS 1.2 only
					g.Expect(interNodeConfig).To(ContainSubstring("{server, ["))
					g.Expect(interNodeConfig).To(ContainSubstring("{client, ["))
					g.Expect(strings.Count(interNodeConfig, "{versions, ['tlsv1.2']}")).To(Equal(2))
					// Ensure it doesn't use TLS1.3 (as FIPS still does for the time being)
					g.Expect(strings.Count(interNodeConfig, "'tlsv1.3'")).To(Equal(0))
				}, timeout, interval).Should(Succeed())
			})
		})
	})

	When("RabbitMQ gets created with FIPS enabled", func() {
		var certSecret *corev1.Secret
		BeforeEach(func() {
			clusterCm := types.NamespacedName{Name: "cluster-config-v1", Namespace: "kube-system"}
			cm := th.GetConfigMap(clusterCm)
			cm.Data["install-config"] = "fips: true"
			err := th.K8sClient.Update(ctx, cm)
			Expect(err).To(Succeed())
			certSecret = CreateCertSecret(rabbitmqName)
			DeferCleanup(th.DeleteSecret, types.NamespacedName{Name: certSecret.Name, Namespace: namespace})
			spec := GetDefaultRabbitMQSpec()
			spec["tls"] = map[string]any{
				"secretName": certSecret.Name,
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQCluster with FIPS enabled", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(cluster.Spec.TLS.SecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.CaSecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeTrue())
				g.Expect(cluster.Spec.Rabbitmq.AdvancedConfig).To(ContainSubstring("ssl_options"))
				g.Expect(cluster.Spec.Rabbitmq.AdditionalConfig).To(ContainSubstring("prometheus.ssl.ip = ::"))

				container := cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0]
				var rabbitmqServerAdditionalErlArgs string
				for _, env := range container.Env {
					if env.Name == "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS" {
						rabbitmqServerAdditionalErlArgs = env.Value
						break
					}
				}
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-crypto fips_mode true"))
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-proto_dist inet_tls"))
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-ssl_dist_optfile /etc/rabbitmq/inter-node-tls.config"))
			}, timeout, interval).Should(Succeed())
		})

		It("should configure TLS 1.2 and 1.3 for FIPS mode", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// TLS settings for Erlang and RabbitMQ endpoints (AdvancedConfig)
				// are in an Erlang data structure, so just parse the expected strings
				// for application "rabbit", "rabbitmq_management", "client" and "ssl"
				cluster := GetRabbitMQCluster(rabbitmqName)
				advancedConfig := cluster.Spec.Rabbitmq.AdvancedConfig

				g.Expect(advancedConfig).To(ContainSubstring("{ssl, [{protocol_version, ['tlsv1.2','tlsv1.3']}"))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbit, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbitmq_management, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{client, ["))
				g.Expect(strings.Count(advancedConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(3))

				// TLS settings for RabbitMQ cluster communication (inter-node-tls.config)
				// should have a config for server and client. Those are in an Erlang
				// data structure, so just parse the expected string
				configMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-config-data", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				cm := th.GetConfigMap(configMapName)
				g.Expect(cm.Data).To(HaveKey("inter_node_tls.config"))
				interNodeConfig := cm.Data["inter_node_tls.config"]

				// Verify server and client configurations use TLS 1.2 only
				g.Expect(interNodeConfig).To(ContainSubstring("{server, ["))
				g.Expect(interNodeConfig).To(ContainSubstring("{client, ["))
				g.Expect(strings.Count(interNodeConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(2))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ TLS input validation", func() {
		It("should set TLSInputReadyCondition to true when valid TLS secret is provided", func() {
			certSecret := CreateCertSecret(rabbitmqName)
			DeferCleanup(th.DeleteSecret, types.NamespacedName{Name: certSecret.Name, Namespace: namespace})

			spec := GetDefaultRabbitMQSpec()
			spec["tls"] = map[string]any{
				"secretName": certSecret.Name,
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.Conditions.Has(condition.TLSInputReadyCondition)).To(BeTrue())
				tlsCondition := instance.Status.Conditions.Get(condition.TLSInputReadyCondition)
				g.Expect(tlsCondition.Status).To(Equal(corev1.ConditionTrue))
				g.Expect(tlsCondition.Message).To(Equal(condition.InputReadyMessage))
			}, timeout, interval).Should(Succeed())
		})

		It("should set TLSInputReadyCondition to false when TLS secret is missing", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["tls"] = map[string]any{
				"secretName": "non-existent-secret",
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.Conditions.Has(condition.TLSInputReadyCondition)).To(BeTrue())
				tlsCondition := instance.Status.Conditions.Get(condition.TLSInputReadyCondition)
				g.Expect(tlsCondition.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(string(tlsCondition.Reason)).To(Equal(string(condition.RequestedReason)))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ gets created with a complete statefulset override", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["override"] = map[string]any{
				"statefulSet": map[string]any{
					"spec": map[string]any{
						"replicas": 3,
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []any{
									map[string]any{
										"name": "foobar",
									},
								},
							},
						},
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQCluster", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(*cluster.Spec.Override.StatefulSet.Spec.Replicas).To(Equal(int32(3)))
				g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Name).To(Equal("foobar"))
			}, timeout, interval).Should(Succeed())

		})
	})

	When("RabbitMQ gets created with a partial statefulset override", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["override"] = map[string]any{
				"statefulSet": map[string]any{
					"spec": map[string]any{
						"replicas": 3,
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQCluster", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(*cluster.Spec.Override.StatefulSet.Spec.Replicas).To(Equal(int32(3)))
				g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec.Containers[0].Name).To(Equal(rabbitmqDefaultName))
			}, timeout, interval).Should(Succeed())

		})
	})

	When("RabbitMQ gets updated with an invalid statefulset override", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["override"] = map[string]any{
				"statefulSet": map[string]any{
					"spec": map[string]any{
						"replicas": 3,
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("gets blocked by the webhook and fail", func() {
			instance := &rabbitmqv1.RabbitMq{}

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				data, _ := json.Marshal(map[string]any{
					"wrong": "type",
				})
				instance.Spec.Override.StatefulSet.Raw = data
				err := th.K8sClient.Update(ctx, instance)
				g.Expect(err.Error()).Should(ContainSubstring("invalid spec override"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ gets created with a service override", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["override"] = map[string]any{
				"service": map[string]any{
					"metadata": map[string]any{
						"annotations": map[string]any{
							"metallb.universe.tf/address-pool":    "internalapi",
							"metallb.universe.tf/loadBalancerIPs": "192.0.2.1",
						},
					},
					"spec": map[string]any{
						"type": "LoadBalancer",
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQCluster with the correct service annotations", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(cluster.Spec.Override.Service.Annotations["metallb.universe.tf/address-pool"]).To(Equal("internalapi"))
				g.Expect(cluster.Spec.Override.Service.Annotations["metallb.universe.tf/loadBalancerIPs"]).To(Equal("192.0.2.1"))
				g.Expect(cluster.Spec.Override.Service.Annotations["dnsmasq.network.openstack.org/hostname"]).To(Equal(fmt.Sprintf("%s.%s.svc", rabbitmqDefaultName, namespace)))
				g.Expect(cluster.Spec.Override.Service.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ per-pod services with 1 IP specified", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["override"] = map[string]any{
				"service": map[string]any{
					"metadata": map[string]any{
						"annotations": map[string]any{
							"metallb.universe.tf/address-pool":    "internalapi",
							"metallb.universe.tf/loadBalancerIPs": "192.0.2.10",
						},
					},
					"spec": map[string]any{
						"type": "LoadBalancer",
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should use IP for main service only, no per-pod services", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check main service has the specified IP
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(cluster.Spec.Override.Service.Annotations["metallb.universe.tf/loadBalancerIPs"]).To(Equal("192.0.2.10"))

				// Check per-pod services are NOT created
				for i := 0; i < 3; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					err := k8sClient.Get(ctx, svcName, svc)
					g.Expect(err).To(HaveOccurred())
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ per-pod services with podOverride", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["queueType"] = "None" // Avoid ha-all policy which requires actual pods
			spec["podOverride"] = map[string]any{
				"services": []map[string]any{
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.11",
							},
						},
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.12",
							},
						},
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.13",
							},
						},
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should create per-pod services with specified IPs", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check per-pod services have the specified IPs
				expectedIPs := []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"}
				for i := 0; i < 3; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					g.Expect(k8sClient.Get(ctx, svcName, svc)).Should(Succeed())
					g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
					g.Expect(svc.Annotations["metallb.universe.tf/loadBalancerIPs"]).To(Equal(expectedIPs[i]))
					g.Expect(svc.Annotations["metallb.universe.tf/address-pool"]).To(Equal("internalapi"))
					g.Expect(svc.Spec.Selector).To(HaveKeyWithValue("statefulset.kubernetes.io/pod-name", fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i)))
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ per-pod services with wrong number of service overrides", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["queueType"] = "None" // Avoid ha-all policy which requires actual pods
			spec["podOverride"] = map[string]any{
				"services": []map[string]any{
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.11",
							},
						},
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.12",
							},
						},
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should skip per-pod service creation due to mismatch", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check per-pod services are NOT created (only 2 services for 3 replicas)
				for i := 0; i < 3; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					err := k8sClient.Get(ctx, svcName, svc)
					g.Expect(err).To(HaveOccurred())
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ per-pod services are deleted when podOverride is removed", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["queueType"] = "None"
			spec["podOverride"] = map[string]any{
				"services": []map[string]any{
					{
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should delete per-pod services when podOverride is removed", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify services are created
			Eventually(func(g Gomega) {
				for i := 0; i < 3; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					g.Expect(k8sClient.Get(ctx, svcName, svc)).Should(Succeed())
				}
			}, timeout, interval).Should(Succeed())

			// Remove podOverride
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				instance.Spec.PodOverride = nil
				g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify services are deleted
			Eventually(func(g Gomega) {
				for i := 0; i < 3; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					err := k8sClient.Get(ctx, svcName, svc)
					g.Expect(err).To(HaveOccurred())
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ per-pod services with podOverride", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 2
			spec["queueType"] = "None"
			spec["podOverride"] = map[string]any{
				"services": []map[string]any{
					{
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
					{
						"spec": map[string]any{
							"type": string(corev1.ServiceTypeLoadBalancer),
						},
					},
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should create per-pod services with owner references for automatic cleanup", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify services are created with owner references
			Eventually(func(g Gomega) {
				for i := 0; i < 2; i++ {
					svcName := types.NamespacedName{
						Name:      fmt.Sprintf("%s-server-%d", rabbitmqDefaultName, i),
						Namespace: namespace,
					}
					svc := &corev1.Service{}
					g.Expect(k8sClient.Get(ctx, svcName, svc)).Should(Succeed())

					// Verify service has owner reference to RabbitMq CR
					g.Expect(svc.OwnerReferences).NotTo(BeEmpty())
					g.Expect(svc.OwnerReferences[0].Kind).To(Equal("RabbitMq"))
					g.Expect(svc.OwnerReferences[0].Name).To(Equal(rabbitmqDefaultName))
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ version upgrade", func() {
		When("RabbitMQ is created without version labels", func() {
			BeforeEach(func() {
				rabbitmq := CreateRabbitMQ(rabbitmqName, GetDefaultRabbitMQSpec())
				DeferCleanup(th.DeleteInstance, rabbitmq)
			})

			It("should default Status.CurrentVersion to 4.2", func() {
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("RabbitMQ major version upgrade (3.9 to 4.2)", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Quorum"
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "3.9",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("3.9"))
				}, timeout, interval).Should(Succeed())
			})

			It("should require storage wipe and update Status.CurrentVersion after upgrade", func() {
				// 3.9 -> 4.2 requires storage wipe (no direct path)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete (UpgradePhase = "WaitingForCluster")
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Wait for new cluster to be created after storage wipe
				var newCluster *rabbitmqclusterv2.RabbitmqCluster
				Eventually(func(g Gomega) {
					newCluster = &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, newCluster)
					g.Expect(err).ToNot(HaveOccurred())
					// Cluster should exist and have been created
					g.Expect(newCluster.Generation).To(BeNumerically(">", 0))
				}, timeout, interval).Should(Succeed())

				// Now simulate the new cluster as ready
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify Status.CurrentVersion is updated after cluster is ready
				Eventually(func(g Gomega) {
					updatedInstance := GetRabbitMQ(rabbitmqName)
					g.Expect(updatedInstance.Status.CurrentVersion).To(Equal("4.2"))
					g.Expect(updatedInstance.Annotations).To(HaveKeyWithValue("rabbitmq.openstack.org/target-version", "4.2"))
				}, timeout, interval).Should(Succeed())
			})

			It("should add and remove storage-wipe-needed annotation during upgrade", func() {
				// 3.9 -> 4.2 requires storage wipe
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Verify new cluster has temporary storage-wipe-needed annotation
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(cluster.Annotations).To(HaveKeyWithValue("rabbitmq.openstack.org/storage-wipe-needed", "true"))
				}, timeout, interval).Should(Succeed())

				// Simulate cluster ready
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify annotation is removed after cluster is ready
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(cluster.Annotations).ToNot(HaveKey("rabbitmq.openstack.org/storage-wipe-needed"))
				}, timeout, interval).Should(Succeed())
			})

			It("should add wipe-data init container when storage-wipe-needed annotation is set", func() {
				// 3.9 -> 4.2 requires storage wipe
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Verify cluster has wipe-data init container
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())

					// Check that cluster has temporary storage-wipe-needed annotation
					g.Expect(cluster.Annotations).To(HaveKeyWithValue("rabbitmq.openstack.org/storage-wipe-needed", "true"))

					// Check that wipe-data init container is present
					// The cluster spec should have Override.StatefulSet with init containers
					g.Expect(cluster.Spec.Override.StatefulSet).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec.InitContainers).ToNot(BeEmpty())

					// Find the wipe-data init container
					var foundWipeContainer bool
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.InitContainers {
						if container.Name == "wipe-data" {
							foundWipeContainer = true
							// Verify it has the correct command
							g.Expect(container.Command).To(Equal([]string{"/bin/sh"}))
							g.Expect(container.Args).To(HaveLen(2))
							g.Expect(container.Args[0]).To(Equal("-c"))
							// Verify script contains essential wipe commands
							g.Expect(container.Args[1]).To(ContainSubstring("WIPE_DIR=\"/var/lib/rabbitmq\""))
							g.Expect(container.Args[1]).To(ContainSubstring("rm -rf"))
							g.Expect(container.Args[1]).To(ContainSubstring(".operator-wipe-4.2"))
							// Verify it has the correct working directory
							g.Expect(container.WorkingDir).To(Equal("/var/lib/rabbitmq"))
							// Verify it has the persistence volume mount
							var foundPersistenceMount bool
							for _, mount := range container.VolumeMounts {
								if mount.Name == "persistence" && mount.MountPath == "/var/lib/rabbitmq" {
									foundPersistenceMount = true
									break
								}
							}
							g.Expect(foundPersistenceMount).To(BeTrue(), "persistence volume mount should be present")
							break
						}
					}
					g.Expect(foundWipeContainer).To(BeTrue(), "wipe-data init container should be present")
				}, timeout, interval).Should(Succeed())
			})

			It("should keep wipe-data init container even after annotation is removed to avoid pod restarts", func() {
				// 3.9 -> 4.2 requires storage wipe
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Simulate cluster ready
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Wait for annotation to be removed
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(cluster.Annotations).ToNot(HaveKey("rabbitmq.openstack.org/storage-wipe-needed"))
				}, timeout, interval).Should(Succeed())

				// Trigger a reconcile by updating RabbitMq CR
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["test-trigger"] = "reconcile"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Verify wipe-data init container is STILL present to avoid unnecessary pod restarts
				// The version-specific marker file prevents it from actually wiping data on restarts
				Consistently(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())

					// Annotation should have been removed to prevent user manipulation
					g.Expect(cluster.Annotations).ToNot(HaveKey("rabbitmq.openstack.org/storage-wipe-needed"))

					// Init container should STILL be present (to avoid spec changes and pod restarts)
					g.Expect(cluster.Spec.Override.StatefulSet).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec).ToNot(BeNil())

					var foundWipeContainer bool
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.InitContainers {
						if container.Name == "wipe-data" {
							foundWipeContainer = true
							// Verify it has version-specific marker logic (should contain ".operator-wipe-")
							g.Expect(container.Args).ToNot(BeEmpty())
							g.Expect(container.Args[len(container.Args)-1]).To(ContainSubstring(".operator-wipe-"))
							break
						}
					}
					g.Expect(foundWipeContainer).To(BeTrue(), "wipe-data init container should remain present to avoid pod restarts")
				}, "5s", interval).Should(Succeed())
			})

			It("should preserve default user credentials during storage wipe", func() {
				// Create initial default user secret before triggering storage wipe
				var cluster *rabbitmqclusterv2.RabbitmqCluster
				Eventually(func(g Gomega) {
					cluster = &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())
				}, timeout, interval).Should(Succeed())

				// Create the default user secret
				secretName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-default-user",
					Namespace: rabbitmqName.Namespace,
				}
				CreateOrUpdateRabbitMQClusterSecret(secretName, cluster)

				// Capture the original secret credentials
				var originalSecret corev1.Secret
				Eventually(func(g Gomega) {
					secret := &corev1.Secret{}
					err := k8sClient.Get(ctx, secretName, secret)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(secret.Data["username"]).ToNot(BeEmpty())
					g.Expect(secret.Data["password"]).ToNot(BeEmpty())
					originalSecret = *secret
				}, timeout, interval).Should(Succeed())

				// Trigger storage wipe by upgrading from 3.9 to 4.2
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to reach WaitingForCluster phase
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"),
						"should reach WaitingForCluster after cluster deletion")
				}, timeout*2, interval).Should(Succeed())

				// Verify backup secret was created with original credentials
				Eventually(func(g Gomega) {
					backupSecret := &corev1.Secret{}
					backupSecretName := types.NamespacedName{
						Name:      rabbitmqName.Name + "-default-user-backup",
						Namespace: rabbitmqName.Namespace,
					}
					err := k8sClient.Get(ctx, backupSecretName, backupSecret)
					g.Expect(err).ToNot(HaveOccurred(), "backup secret should exist during storage wipe")
					g.Expect(backupSecret.Data["username"]).To(Equal(originalSecret.Data["username"]))
					g.Expect(backupSecret.Data["password"]).To(Equal(originalSecret.Data["password"]))
				}, timeout, interval).Should(Succeed())

				// Simulate the new cluster as ready
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Wait for upgrade to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"),
						"CurrentVersion should be updated to 4.2 after successful upgrade")
					g.Expect(instance.Status.UpgradePhase).To(BeEmpty(),
						"UpgradePhase should be cleared after upgrade completes")
				}, timeout*2, interval).Should(Succeed())

				// Verify a preserved RabbitMQUser was created with original credentials
				Eventually(func(g Gomega) {
					preservedUser := &rabbitmqv1.RabbitMQUser{}
					preservedUserName := types.NamespacedName{
						Name:      rabbitmqName.Name + "-default-user-preserved",
						Namespace: rabbitmqName.Namespace,
					}
					err := k8sClient.Get(ctx, preservedUserName, preservedUser)
					g.Expect(err).ToNot(HaveOccurred(), "preserved user should exist after storage wipe")

					// Verify it uses the original username and references the backup secret
					g.Expect(preservedUser.Spec.Username).To(Equal(string(originalSecret.Data["username"])),
						"preserved user should have original username")
					g.Expect(*preservedUser.Spec.Secret).To(Equal(rabbitmqName.Name+"-default-user-backup"),
						"preserved user should reference backup secret")
					g.Expect(preservedUser.Spec.Tags).To(ContainElement("administrator"),
						"preserved user should have administrator tag")
				}, timeout, interval).Should(Succeed())

				// Verify backup secret still exists (needed by RabbitMQUser)
				Eventually(func(g Gomega) {
					backupSecret := &corev1.Secret{}
					backupSecretName := types.NamespacedName{
						Name:      rabbitmqName.Name + "-default-user-backup",
						Namespace: rabbitmqName.Namespace,
					}
					err := k8sClient.Get(ctx, backupSecretName, backupSecret)
					g.Expect(err).ToNot(HaveOccurred(), "backup secret should still exist for RabbitMQUser to reference")
					g.Expect(backupSecret.Data["username"]).To(Equal(originalSecret.Data["username"]))
					g.Expect(backupSecret.Data["password"]).To(Equal(originalSecret.Data["password"]))
				}, timeout, interval).Should(Succeed())
			})

			It("should preserve default user secret credentials during upgrade", func() {
				// Wait for initial cluster to be created
				var cluster *rabbitmqclusterv2.RabbitmqCluster
				Eventually(func(g Gomega) {
					cluster = &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())
				}, timeout, interval).Should(Succeed())

				// Create the default user secret WITHOUT marking the cluster as ready yet
				// This simulates that the secret exists from the initial deployment
				secretName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-default-user",
					Namespace: rabbitmqName.Namespace,
				}
				CreateOrUpdateRabbitMQClusterSecret(secretName, cluster)

				// Capture the original secret credentials
				var originalSecret corev1.Secret
				Eventually(func(g Gomega) {
					secret := &corev1.Secret{}
					err := k8sClient.Get(ctx, secretName, secret)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(secret.Data["username"]).ToNot(BeEmpty())
					g.Expect(secret.Data["password"]).ToNot(BeEmpty())
					originalSecret = *secret
				}, timeout, interval).Should(Succeed())

				// NOW trigger upgrade from 3.9 to 4.2 (will delete pods, not cluster)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for upgrade to reach WaitingForCluster phase (pods deleted)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"), "should reach WaitingForCluster after pods are deleted")
				}, timeout*2, interval).Should(Succeed())

				// Simulate pods coming back up (cluster becomes ready again)
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Wait for the upgrade to complete (CurrentVersion updated, UpgradePhase cleared)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"), "CurrentVersion should be updated to 4.2")
					g.Expect(instance.Status.UpgradePhase).To(BeEmpty(), "UpgradePhase should be cleared after completion")
				}, timeout*2, interval).Should(Succeed())

				// Verify the RabbitMQCluster still exists (we only deleted pods, not the cluster)
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred(), "RabbitMQCluster should still exist - we only delete pods")
				}, timeout, interval).Should(Succeed())

				// Verify the default user secret still exists and credentials are unchanged
				Eventually(func(g Gomega) {
					secret := &corev1.Secret{}
					secretName := types.NamespacedName{
						Name:      rabbitmqName.Name + "-default-user",
						Namespace: rabbitmqName.Namespace,
					}
					err := k8sClient.Get(ctx, secretName, secret)
					g.Expect(err).ToNot(HaveOccurred(), "secret should exist - it was never deleted")

					// Credentials must be identical - secret was never deleted/recreated
					g.Expect(secret.Data["username"]).To(Equal(originalSecret.Data["username"]), "username must remain stable for external consumers")
					g.Expect(secret.Data["password"]).To(Equal(originalSecret.Data["password"]), "password must remain stable for external consumers")
				}, timeout, interval).Should(Succeed())
			})
		})

		When("RabbitMQ patch version changes (3.9.0 to 3.9.1)", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "3.9",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("3.9"))
				}, timeout, interval).Should(Succeed())
			})

			It("should allow patch version changes without storage wipe", func() {
				// Patch version changes don't require storage wipe
				instance := GetRabbitMQ(rabbitmqName)
				if instance.Annotations == nil {
					instance.Annotations = make(map[string]string)
				}
				instance.Annotations["rabbitmq.openstack.org/target-version"] = "3.9.1"
				Expect(k8sClient.Update(ctx, instance)).Should(Succeed())

				// Should NOT trigger storage wipe - Status.CurrentVersion should remain 3.9
				Consistently(func(g Gomega) {
					updatedInstance := GetRabbitMQ(rabbitmqName)
					g.Expect(updatedInstance.Status.CurrentVersion).To(Equal("3.9"))
				}, "5s", interval).Should(Succeed())
			})
		})

		When("RabbitMQ version downgrade (4.2 to 3.9)", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Quorum"
				rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// First upgrade to 4.2 to establish a 4.2 cluster
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for upgrade to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout*2, interval).Should(Succeed())
			})

			It("should require storage wipe for downgrade", func() {
				// 4.2 -> 3.9 is a downgrade and requires storage wipe
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "3.9"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete (UpgradePhase = "WaitingForCluster")
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Wait for new cluster to be created after storage wipe
				var newCluster *rabbitmqclusterv2.RabbitmqCluster
				Eventually(func(g Gomega) {
					newCluster = &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, newCluster)
					g.Expect(err).ToNot(HaveOccurred())
					// Cluster should exist and have been created
					g.Expect(newCluster.Generation).To(BeNumerically(">", 0))
				}, timeout, interval).Should(Succeed())

				// Simulate the new cluster as ready
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify Status.CurrentVersion is updated after cluster is ready
				Eventually(func(g Gomega) {
					updatedInstance := GetRabbitMQ(rabbitmqName)
					g.Expect(updatedInstance.Status.CurrentVersion).To(Equal("3.9"))
					g.Expect(updatedInstance.Annotations).To(HaveKeyWithValue("rabbitmq.openstack.org/target-version", "3.9"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("Existing RabbitMQCluster without CurrentVersion is reconciled with target-version annotation", func() {
			// This test covers the bug where an existing 3.9 cluster is reconciled by a new operator
			// that tracks CurrentVersion, and openstack-operator immediately sets target-version: "4.2"
			// The controller must detect the existing cluster and initialize CurrentVersion to "3.9"
			// to trigger proper storage wipe, not skip it by initializing to "4.2"
			It("should initialize CurrentVersion to 3.9 and trigger storage wipe for upgrade to 4.2", func() {
				// Step 1: Create a RabbitMQCluster directly (simulating old operator deployment)
				cluster := &rabbitmqclusterv2.RabbitmqCluster{}
				cluster.Name = rabbitmqName.Name
				cluster.Namespace = rabbitmqName.Namespace
				cluster.Spec.Image = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"
				cluster.Spec.Replicas = ptr.To(int32(1))
				Expect(k8sClient.Create(ctx, cluster)).Should(Succeed())
				DeferCleanup(func() {
					Eventually(func(g Gomega) {
						c := &rabbitmqclusterv2.RabbitmqCluster{}
						err := k8sClient.Get(ctx, rabbitmqName, c)
						if err == nil {
							g.Expect(k8sClient.Delete(ctx, c)).Should(Succeed())
						}
					}, timeout, interval).Should(Succeed())
				})

				// Step 2: Create RabbitMQ CR with target-version: "4.2" annotation
				// This simulates openstack-operator setting the target version immediately
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Quorum"
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "4.2",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// Step 3: Verify CurrentVersion is initialized to "3.9" (not "4.2"!)
				// This is the critical fix - we detect the existing cluster and assume 3.9
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("3.9"),
						"CurrentVersion should be initialized to 3.9 when existing cluster is detected")
				}, timeout, interval).Should(Succeed())

				// Step 4: Wait for storage wipe to reach WaitingForCluster phase
				// This ensures pods have been deleted and the cluster is waiting for readiness
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"),
						"Storage wipe should reach WaitingForCluster phase for 3.9 -> 4.2 upgrade")
				}, timeout*2, interval).Should(Succeed())

				// Step 5: Simulate the new cluster as ready (pods came back up)
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Step 6: Verify CurrentVersion is updated to "4.2" after upgrade completes
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"),
						"CurrentVersion should be updated to 4.2 after successful upgrade")
					g.Expect(instance.Status.UpgradePhase).To(BeEmpty(),
						"UpgradePhase should be cleared after upgrade completes")
				}, timeout*2, interval).Should(Succeed())
			})
		})
	})

	When("RabbitMQ mirrored queues and version compatibility", func() {
		When("RabbitMQ 3.9 with Mirrored queues", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Mirrored"
				spec["replicas"] = 2
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "3.9",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)
			})

			It("should apply mirrored queue policy on RabbitMQ 3.9", func() {
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify mirrored queue policy is applied
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("3.9"))
					g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeMirrored))
				}, timeout, interval).Should(Succeed())

				// Verify policy CR is created
				policyName := types.NamespacedName{
					Name:      rabbitmqDefaultName + "-ha-all",
					Namespace: namespace,
				}
				Eventually(func(g Gomega) {
					policy := &rabbitmqv1.RabbitMQPolicy{}
					err := k8sClient.Get(ctx, policyName, policy)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(policy.Spec.Name).To(Equal("ha-all"))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("RabbitMQ 4.2 with Mirrored queues", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Mirrored"
				spec["replicas"] = 2
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "4.2",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// Wait for controller to set default version first (4.2)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout, interval).Should(Succeed())

				SimulateRabbitMQClusterReady(rabbitmqName)
			})

			It("should skip mirrored queue policy on RabbitMQ 4.2+", func() {
				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify mirrored queue policy is NOT applied (status should be cleared)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
					// Status should be empty since policy is not applied
					g.Expect(instance.Status.QueueType).To(BeEmpty())
				}, timeout, interval).Should(Succeed())

				// Verify policy CR is NOT created
				policyName := types.NamespacedName{
					Name:      rabbitmqDefaultName + "-ha-all",
					Namespace: namespace,
				}
				Consistently(func(g Gomega) {
					policy := &rabbitmqv1.RabbitMQPolicy{}
					err := k8sClient.Get(ctx, policyName, policy)
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
				}, "5s", interval).Should(Succeed())
			})

			It("should override Mirrored to Quorum via webhook on updates", func() {
				// Trigger an update to the CR (webhook should override Mirrored to Quorum)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					// Make a trivial change to trigger webhook defaulting
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["test-update"] = "trigger-webhook"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Verify webhook overrode queueType to Quorum
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Spec.QueueType).ToNot(BeNil())
					g.Expect(*instance.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("RabbitMQ 3.9 with Mirrored queues upgrading to 4.2", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Mirrored"
				spec["replicas"] = 2
				annotations := map[string]string{
					"rabbitmq.openstack.org/target-version": "3.9",
				}
				rabbitmq := CreateRabbitMQWithAnnotations(rabbitmqName, spec, annotations)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// Wait for controller to initialize with version 3.9
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("3.9"))
				}, timeout, interval).Should(Succeed())

				SimulateRabbitMQClusterReady(rabbitmqName)

				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeMirrored))
				}, timeout, interval).Should(Succeed())
			})

			It("should automatically migrate to Quorum queues and wipe cluster", func() {
				// Trigger upgrade to 4.2
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Verify queueType is automatically changed to Quorum
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Spec.QueueType).ToNot(BeNil())
					g.Expect(*instance.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete (UpgradePhase = "WaitingForCluster")
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Wait for new cluster to be created after storage wipe
				var newCluster *rabbitmqclusterv2.RabbitmqCluster
				Eventually(func(g Gomega) {
					newCluster = &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, newCluster)
					g.Expect(err).ToNot(HaveOccurred())
					// Cluster should exist and have been created
					g.Expect(newCluster.Generation).To(BeNumerically(">", 0))
				}, timeout, interval).Should(Succeed())

				SimulateRabbitMQClusterReady(rabbitmqName)

				// Verify Status.CurrentVersion is updated
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout, interval).Should(Succeed())

				// Status.queueType should be updated to Quorum after migration
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				}, timeout, interval).Should(Succeed())
			})

			It("should add wipe-data init container during queue migration", func() {
				// Trigger upgrade to 4.2 (which also triggers Mirrored -> Quorum migration)
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
				}, timeout, interval).Should(Succeed())

				// Wait for storage wipe to complete
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.UpgradePhase).To(Equal("WaitingForCluster"))
				}, timeout*2, interval).Should(Succeed())

				// Verify cluster has storage-wipe-needed annotation and wipe-data init container
				Eventually(func(g Gomega) {
					cluster := &rabbitmqclusterv2.RabbitmqCluster{}
					err := k8sClient.Get(ctx, rabbitmqName, cluster)
					g.Expect(err).ToNot(HaveOccurred())

					// Annotation should be set to trigger wipe
					g.Expect(cluster.Annotations).To(HaveKeyWithValue("rabbitmq.openstack.org/storage-wipe-needed", "true"))

					// Init container should be present
					g.Expect(cluster.Spec.Override.StatefulSet).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template).ToNot(BeNil())
					g.Expect(cluster.Spec.Override.StatefulSet.Spec.Template.Spec).ToNot(BeNil())

					var foundWipeContainer bool
					for _, container := range cluster.Spec.Override.StatefulSet.Spec.Template.Spec.InitContainers {
						if container.Name == "wipe-data" {
							foundWipeContainer = true
							break
						}
					}
					g.Expect(foundWipeContainer).To(BeTrue(), "wipe-data init container should be present during queue migration")
				}, timeout, interval).Should(Succeed())
			})
		})

		When("RabbitMQ 4.2 with Quorum queues", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["queueType"] = "Quorum"
				rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// Wait for default version initialization
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout, interval).Should(Succeed())

				SimulateRabbitMQClusterReady(rabbitmqName)
			})

			It("should automatically override Mirrored to Quorum on RabbitMQ 4.2", func() {
				// Try to change queueType to Mirrored - webhook should override to Quorum
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					mirrored := rabbitmqv1.QueueTypeMirrored
					instance.Spec.QueueType = &mirrored
					// Add target-version annotation to trigger DefaultForUpdate
					if instance.Annotations == nil {
						instance.Annotations = make(map[string]string)
					}
					instance.Annotations["rabbitmq.openstack.org/target-version"] = "4.2"
					err := k8sClient.Update(ctx, instance)
					g.Expect(err).ToNot(HaveOccurred())
				}, timeout, interval).Should(Succeed())

				// Verify queueType was automatically overridden to Quorum by webhook
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Spec.QueueType).ToNot(BeNil())
					g.Expect(*instance.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				}, timeout, interval).Should(Succeed())
			})
		})
	})

	When("RabbitMQ TLS configuration", func() {
		When("RabbitMQ 4.2 with TLS enabled", func() {
			BeforeEach(func() {
				spec := GetDefaultRabbitMQSpec()
				spec["tls"] = map[string]any{
					"secretName": "test-tls-secret",
				}
				rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
				DeferCleanup(th.DeleteInstance, rabbitmq)

				// Create TLS secret
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-tls-secret",
						Namespace: namespace,
					},
					Data: map[string][]byte{
						"tls.crt": []byte("test-cert"),
						"tls.key": []byte("test-key"),
						"ca.crt":  []byte("test-ca"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())
				DeferCleanup(k8sClient.Delete, ctx, secret)

				// Wait for default version initialization
				Eventually(func(g Gomega) {
					instance := GetRabbitMQ(rabbitmqName)
					g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				}, timeout, interval).Should(Succeed())

				SimulateRabbitMQClusterReady(rabbitmqName)
			})

			It("should enable TLS 1.3 on RabbitMQ 4.2", func() {
				// Verify TLS 1.3 is enabled in RabbitMQ 4.2
				Eventually(func(g Gomega) {
					cluster := GetRabbitMQCluster(rabbitmqName)
					advancedConfig := cluster.Spec.Rabbitmq.AdvancedConfig

					// RabbitMQ 4.2 should have TLS 1.2 and 1.3 enabled
					g.Expect(advancedConfig).To(ContainSubstring("{ssl, [{protocol_version, ['tlsv1.2','tlsv1.3']}"))
					g.Expect(strings.Count(advancedConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(3))

					// Verify inter_node_tls config also has TLS 1.3
					configMapName := types.NamespacedName{
						Name:      rabbitmqName.Name + "-config-data",
						Namespace: namespace,
					}
					configMap := &corev1.ConfigMap{}
					err := k8sClient.Get(ctx, configMapName, configMap)
					g.Expect(err).ToNot(HaveOccurred())

					interNodeConfig := configMap.Data["inter_node_tls.config"]
					g.Expect(strings.Count(interNodeConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(2))
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
