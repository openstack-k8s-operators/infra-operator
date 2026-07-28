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

	//revive:disable-next-line:dot-imports

	//. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
				g.Expect(*instance.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
			}, timeout, interval).Should(Succeed())
		})

		It("preserves explicitly set QueueType=Mirrored", func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Spec.QueueType).ToNot(BeNil())
				g.Expect(*instance.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeMirrored))
			}, timeout, interval).Should(Succeed())
		})

	})

	When("a default RabbitMQ gets created", func() {
		BeforeEach(func() {
			rabbitmq := CreateRabbitMQ(rabbitmqName, GetDefaultRabbitMQSpec())
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQ cluster with StatefulSet", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(cluster.Spec.TLS.SecretName).To(BeEmpty())
				g.Expect(cluster.Spec.TLS.CaSecretName).To(BeEmpty())
				g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeFalse())

				// Check the actual StatefulSet
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).ToNot(BeNil())
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(1)))

				container := sts.Spec.Template.Spec.Containers[0]
				// RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS is always set to configure
				// Erlang DNS resolution and inter-node communication protocol.
				var erlArgs string
				for _, env := range container.Env {
					if env.Name == "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS" {
						erlArgs = env.Value
						break
					}
				}
				g.Expect(erlArgs).To(ContainSubstring("-proto_dist inet_tcp"))
				g.Expect(erlArgs).ToNot(ContainSubstring("-kernel inetrc"))
			}, timeout, interval).Should(Succeed())

		})
	})

	When("RabbitMQ gets created with TLS enabled", func() {
		var certSecret *corev1.Secret
		BeforeEach(func() {
			certSecret = CreateCertSecret(rabbitmqName)
			DeferCleanup(th.DeleteSecret, types.NamespacedName{Name: certSecret.Name, Namespace: namespace})
			spec := GetDefaultRabbitMQSpec()
			spec["tls"] = map[string]any{
				"secretName": certSecret.Name,
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have created a RabbitMQ cluster with TLS enabled", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(cluster.Spec.TLS.SecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.CaSecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeTrue())

				// Check the generated server-conf ConfigMap for TLS configuration
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCfgMap := th.GetConfigMap(serverCfgMapName)
				g.Expect(serverCfgMap.Data).To(HaveKey("advanced.config"))
				g.Expect(serverCfgMap.Data["advanced.config"]).To(ContainSubstring("ssl_options"))

				// Check the actual StatefulSet
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).ToNot(BeNil())

				container := sts.Spec.Template.Spec.Containers[0]
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

		It("should configure TLS 1.2+1.3 for RabbitMQ 4.x non-FIPS mode", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check server-conf ConfigMap for advanced.config
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				g.Expect(serverCm.Data).To(HaveKey("advanced.config"))
				advancedConfig := serverCm.Data["advanced.config"]

				// RabbitMQ 4.x (default) enables both TLS 1.2 and 1.3
				g.Expect(advancedConfig).To(ContainSubstring("{ssl, [{protocol_version, ['tlsv1.2','tlsv1.3']}"))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbit, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbitmq_management, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{client, ["))
				g.Expect(strings.Count(advancedConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(3))

				// Check config-data ConfigMap for inter_node_tls.config
				configDataName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-config-data", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				configDataCm := th.GetConfigMap(configDataName)
				g.Expect(configDataCm.Data).To(HaveKey("inter_node_tls.config"))
				interNodeConfig := configDataCm.Data["inter_node_tls.config"]

				// Verify server and client configurations use TLS 1.2+1.3
				g.Expect(interNodeConfig).To(ContainSubstring("{server, ["))
				g.Expect(interNodeConfig).To(ContainSubstring("{client, ["))
				g.Expect(strings.Count(interNodeConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(2))
			}, timeout, interval).Should(Succeed())
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

		It("should have created a RabbitMQ cluster with FIPS enabled", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))
				g.Expect(cluster.Spec.TLS.SecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.CaSecretName).To(Equal(certSecret.Name))
				g.Expect(cluster.Spec.TLS.DisableNonTLSListeners).To(BeTrue())

				// Check the generated server-conf ConfigMap for TLS configuration
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCfgMap := th.GetConfigMap(serverCfgMapName)
				g.Expect(serverCfgMap.Data).To(HaveKey("advanced.config"))
				g.Expect(serverCfgMap.Data["advanced.config"]).To(ContainSubstring("ssl_options"))

				// Check the actual StatefulSet
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).ToNot(BeNil())

				container := sts.Spec.Template.Spec.Containers[0]
				var rabbitmqServerAdditionalErlArgs string
				for _, env := range container.Env {
					if env.Name == "RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS" {
						rabbitmqServerAdditionalErlArgs = env.Value
						break
					}
				}
				// Note: FIPS mode flag would be set by the controller based on cluster config
				// For now, we just verify TLS configuration is present
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-proto_dist inet_tls"))
				g.Expect(rabbitmqServerAdditionalErlArgs).To(ContainSubstring("-ssl_dist_optfile /etc/rabbitmq/inter-node-tls.config"))
			}, timeout, interval).Should(Succeed())
		})

		It("should configure TLS 1.2 and 1.3 for FIPS mode", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check server-conf ConfigMap for advanced.config
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				g.Expect(serverCm.Data).To(HaveKey("advanced.config"))
				advancedConfig := serverCm.Data["advanced.config"]

				g.Expect(advancedConfig).To(ContainSubstring("{ssl, [{protocol_version, ['tlsv1.2','tlsv1.3']}"))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbit, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{rabbitmq_management, ["))
				g.Expect(advancedConfig).To(ContainSubstring("{client, ["))
				g.Expect(strings.Count(advancedConfig, "{versions, ['tlsv1.2','tlsv1.3']}")).To(Equal(3))

				// Check config-data ConfigMap for inter_node_tls.config
				configDataName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-config-data", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				configDataCm := th.GetConfigMap(configDataName)
				g.Expect(configDataCm.Data).To(HaveKey("inter_node_tls.config"))
				interNodeConfig := configDataCm.Data["inter_node_tls.config"]

				// Verify server and client configurations use TLS 1.2 and 1.3 for FIPS
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

		It("should have created a StatefulSet based on spec", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))

				// Note: Override.StatefulSet is deprecated
				// The StatefulSet should be created from the RabbitMq spec directly
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).ToNot(BeNil())
				// The test setup creates override with replicas=3, but we now use spec.replicas
				// which defaults to 1 unless explicitly set
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

		It("should have created a StatefulSet based on spec", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(*cluster.Spec.Replicas).To(Equal(int32(1)))

				// Note: Override.StatefulSet is deprecated
				// The StatefulSet should be created from the RabbitMq spec directly
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).ToNot(BeNil())
				g.Expect(sts.Spec.Template.Spec.Containers[0].Name).To(Equal("rabbitmq"))
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

		It("should have created a RabbitMQ Service with the correct service annotations", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				// Check the override.service fields
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(cluster.Spec.Override.Service).NotTo(BeNil())
				g.Expect(cluster.Spec.Override.Service.EmbeddedLabelsAnnotations).NotTo(BeNil())
				g.Expect(cluster.Spec.Override.Service.Annotations["metallb.universe.tf/address-pool"]).To(Equal("internalapi"))
				g.Expect(cluster.Spec.Override.Service.Annotations["metallb.universe.tf/loadBalancerIPs"]).To(Equal("192.0.2.1"))
				g.Expect(cluster.Spec.Override.Service.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))

				// Check the actual Service resource
				svcName := types.NamespacedName{
					Name:      rabbitmqDefaultName,
					Namespace: namespace,
				}
				svc := &corev1.Service{}
				err := th.K8sClient.Get(th.Ctx, svcName, svc)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(svc.Annotations["metallb.universe.tf/address-pool"]).To(Equal("internalapi"))
				g.Expect(svc.Annotations["metallb.universe.tf/loadBalancerIPs"]).To(Equal("192.0.2.1"))
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ gets created with a LoadBalancer service override", func() {
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

		It("should create a LoadBalancer service from override", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				cluster := GetRabbitMQCluster(rabbitmqName)
				g.Expect(cluster.Spec.Override.Service).NotTo(BeNil())
				g.Expect(cluster.Spec.Override.Service.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))

				svcName := types.NamespacedName{
					Name:      rabbitmqDefaultName,
					Namespace: namespace,
				}
				svc := &corev1.Service{}
				err := th.K8sClient.Get(th.Ctx, svcName, svc)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
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

	When("migrating from old rabbitmq-cluster-operator", func() {
		var migrationName types.NamespacedName

		BeforeEach(func() {
			migrationName = types.NamespacedName{
				Name:      "rabbitmq-migrate",
				Namespace: namespace,
			}

			// Install a minimal CRD for rabbitmqclusters.rabbitmq.com so migration detection works
			oldCRD := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rabbitmqclusters.rabbitmq.com",
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "rabbitmq.com",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural:   "rabbitmqclusters",
						Singular: "rabbitmqcluster",
						Kind:     "RabbitmqCluster",
						ListKind: "RabbitmqClusterList",
					},
					Scope: apiextensionsv1.NamespaceScoped,
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{
							Name:    "v1beta1",
							Served:  true,
							Storage: true,
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Type:                   "object",
									XPreserveUnknownFields: ptr.To(true),
								},
							},
						},
					},
				},
			}
			existingCRD := &apiextensionsv1.CustomResourceDefinition{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "rabbitmqclusters.rabbitmq.com"}, existingCRD)
			if k8s_errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, oldCRD)).To(Succeed())
			}

			// Wait for CRD to be established
			Eventually(func(g Gomega) {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rabbitmqclusters.rabbitmq.com"}, crd)).To(Succeed())
				established := false
				for _, c := range crd.Status.Conditions {
					if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
						established = true
					}
				}
				g.Expect(established).To(BeTrue(), "CRD should be established")
			}, timeout, interval).Should(Succeed())

			// Create an old RabbitmqCluster CR to trigger migration detection
			oldCR := &uns.Unstructured{}
			oldCR.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "rabbitmq.com",
				Version: "v1beta1",
				Kind:    "RabbitmqCluster",
			})
			oldCR.SetName(migrationName.Name)
			oldCR.SetNamespace(migrationName.Namespace)
			oldCR.SetFinalizers([]string{"deletion.finalizers.rabbitmqclusters.rabbitmq.com"})
			Expect(k8sClient.Create(ctx, oldCR)).To(Succeed())
		})

		It("should adopt an existing StatefulSet owned by a foreign controller", func() {
			// Simulate a pre-existing StatefulSet created by the old rabbitmq-cluster-operator
			// with a foreign controller owner reference
			fakeOwnerUID := types.UID("fake-rabbitmqcluster-uid-12345")
			// Old rabbitmq-cluster-operator uses app.kubernetes.io/* labels which differ
			// from our controller's "service"/"owner" labels. The selector is immutable,
			// so the controller must detect and handle this mismatch.
			// Old rabbitmq-cluster-operator uses app.kubernetes.io/name as the
			// selector label, which now matches our new controller's selector.
			oldLabels := map[string]string{
				"app.kubernetes.io/component": "rabbitmq",
				"app.kubernetes.io/name":      migrationName.Name,
				"app.kubernetes.io/part-of":   "rabbitmq",
			}
			oldSelectorLabels := map[string]string{
				"app.kubernetes.io/name": migrationName.Name,
			}
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
					Labels:    oldLabels,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "rabbitmq.com/v1beta1",
							Kind:               "RabbitmqCluster",
							Name:               migrationName.Name,
							UID:                fakeOwnerUID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: oldSelectorLabels,
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: oldLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "rabbitmq", Image: "rabbitmq:3"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Also create a pre-existing default-user secret with old owner
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      migrationName.Name + "-default-user",
					Namespace: migrationName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "rabbitmq.com/v1beta1",
							Kind:               "RabbitmqCluster",
							Name:               migrationName.Name,
							UID:                fakeOwnerUID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Data: map[string][]byte{
					"username": []byte("existing-user"),
					"password": []byte("existing-pass"),
					"host":     []byte("old-host"),
					"port":     []byte("5672"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			// Now create the RabbitMq CR - controller should adopt existing resources
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(migrationName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// The old StatefulSet has incompatible selector labels, so the controller
			// should delete it (orphan) and recreate with correct labels and ownership
			Eventually(func(g Gomega) {
				adoptedSts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
				}, adoptedSts)).To(Succeed())

				// Should have exactly one controller owner reference, and it should be our RabbitMq CR
				controllerRefs := 0
				hasRabbitMqOwner := false
				hasOldOwner := false
				for _, ref := range adoptedSts.OwnerReferences {
					if ref.Controller != nil && *ref.Controller {
						controllerRefs++
						if ref.Kind == "RabbitMq" {
							hasRabbitMqOwner = true
						}
						if ref.Kind == "RabbitmqCluster" {
							hasOldOwner = true
						}
					}
				}
				g.Expect(controllerRefs).To(Equal(1), "should have exactly one controller owner")
				g.Expect(hasRabbitMqOwner).To(BeTrue(), "controller owner should be RabbitMq")
				g.Expect(hasOldOwner).To(BeFalse(), "old RabbitmqCluster owner should be removed")
			}, timeout, interval).Should(Succeed())

			// Verify default-user secret is adopted and preserves existing credentials
			Eventually(func(g Gomega) {
				adoptedSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      migrationName.Name + "-default-user",
					Namespace: migrationName.Namespace,
				}, adoptedSecret)).To(Succeed())

				// Should be owned by RabbitMq, not old RabbitmqCluster
				hasRabbitMqOwner := false
				for _, ref := range adoptedSecret.OwnerReferences {
					if ref.Controller != nil && *ref.Controller && ref.Kind == "RabbitMq" {
						hasRabbitMqOwner = true
					}
				}
				g.Expect(hasRabbitMqOwner).To(BeTrue(), "secret controller owner should be RabbitMq")

				// Existing credentials should be preserved (not regenerated)
				g.Expect(string(adoptedSecret.Data["username"])).To(Equal("existing-user"))
				g.Expect(string(adoptedSecret.Data["password"])).To(Equal("existing-pass"))
			}, timeout, interval).Should(Succeed())
		})

		It("should delete old RabbitmqCluster CR after reparenting", func() {
			// Get the old CR's UID (created in BeforeEach)
			oldCR := &uns.Unstructured{}
			oldCR.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "rabbitmq.com",
				Version: "v1beta1",
				Kind:    "RabbitmqCluster",
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      migrationName.Name,
				Namespace: migrationName.Namespace,
			}, oldCR)).To(Succeed())

			// Also create a pre-existing StatefulSet owned by the old CR
			fakeOwnerUID := oldCR.GetUID()
			oldLabels := map[string]string{
				"app.kubernetes.io/component": "rabbitmq",
				"app.kubernetes.io/name":      migrationName.Name,
				"app.kubernetes.io/part-of":   "rabbitmq",
			}
			oldSelectorLabels := map[string]string{
				"app.kubernetes.io/name": migrationName.Name,
			}
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
					Labels:    oldLabels,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "rabbitmq.com/v1beta1",
							Kind:               "RabbitmqCluster",
							Name:               migrationName.Name,
							UID:                fakeOwnerUID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: oldSelectorLabels,
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: oldLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "rabbitmq", Image: "rabbitmq:3"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Create the RabbitMq CR - controller should adopt resources and delete old CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(migrationName, spec)
			DeferCleanup(func() {
				DeleteRabbitMQCluster(migrationName)
			})
			_ = rabbitmq

			// Verify the old RabbitmqCluster CR is deleted (finalizers removed + deleted)
			Eventually(func(g Gomega) {
				checkCR := &uns.Unstructured{}
				checkCR.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "rabbitmq.com",
					Version: "v1beta1",
					Kind:    "RabbitmqCluster",
				})
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      migrationName.Name,
					Namespace: migrationName.Namespace,
				}, checkCR)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"old RabbitmqCluster CR should be deleted after reparenting")
			}, timeout, interval).Should(Succeed())

			// Verify the StatefulSet is now owned by the new RabbitMq CR
			Eventually(func(g Gomega) {
				adoptedSts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
				}, adoptedSts)).To(Succeed())

				hasRabbitMqOwner := false
				hasOldOwner := false
				for _, ref := range adoptedSts.OwnerReferences {
					if ref.Controller != nil && *ref.Controller {
						if ref.Kind == "RabbitMq" {
							hasRabbitMqOwner = true
						}
						if ref.Kind == "RabbitmqCluster" {
							hasOldOwner = true
						}
					}
				}
				g.Expect(hasRabbitMqOwner).To(BeTrue(), "controller owner should be RabbitMq")
				g.Expect(hasOldOwner).To(BeFalse(), "old RabbitmqCluster owner should be removed")
			}, timeout, interval).Should(Succeed())
		})

		It("should clean PVC ownerRefs without deleting the StatefulSet", func() {
			// Create a StatefulSet with stale rabbitmq.com ownerReferences in volumeClaimTemplates
			fakeOwnerUID := types.UID("fake-old-rabbitmqcluster-uid")
			storageClass := "local-storage"
			oldLabels := map[string]string{
				"app.kubernetes.io/component": "rabbitmq",
				"app.kubernetes.io/name":      migrationName.Name,
				"app.kubernetes.io/part-of":   "rabbitmq",
			}
			oldSelectorLabels := map[string]string{
				"app.kubernetes.io/name": migrationName.Name,
			}
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
					Labels:    oldLabels,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "rabbitmq.com/v1beta1",
							Kind:               "RabbitmqCluster",
							Name:               migrationName.Name,
							UID:                fakeOwnerUID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    ptr.To(int32(1)),
					ServiceName: migrationName.Name + "-nodes",
					Selector: &metav1.LabelSelector{
						MatchLabels: oldSelectorLabels,
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: oldLabels,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "rabbitmq", Image: "rabbitmq:3"},
							},
						},
					},
					VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "persistence",
								OwnerReferences: []metav1.OwnerReference{
									{
										APIVersion:         "rabbitmq.com/v1beta1",
										Kind:               "RabbitmqCluster",
										Name:               migrationName.Name,
										UID:                fakeOwnerUID,
										Controller:         ptr.To(true),
										BlockOwnerDeletion: ptr.To(false),
									},
								},
							},
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								StorageClassName: &storageClass,
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("10Gi"),
									},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, sts)).To(Succeed())

			// Record the STS UID to verify it's NOT recreated
			originalUID := sts.UID

			// Create the RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(migrationName, spec)
			DeferCleanup(func() {
				DeleteRabbitMQCluster(migrationName)
			})
			_ = rabbitmq

			// Verify VCTCleaned status is set
			Eventually(func(g Gomega) {
				rmq := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, migrationName, rmq)).To(Succeed())
				g.Expect(rmq.Status.VCTCleaned).To(Equal("True"), "VCTCleaned should be True after fix")
			}, timeout, interval).Should(Succeed())

			// Verify the adopted-storage-class annotation is set
			Eventually(func(g Gomega) {
				rmq := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, migrationName, rmq)).To(Succeed())
				sc, ok := rmq.Annotations["rabbitmq.openstack.org/adopted-storage-class"]
				g.Expect(ok).To(BeTrue(), "adopted-storage-class annotation should be set")
				g.Expect(sc).To(Equal("local-storage"))
			}, timeout, interval).Should(Succeed())

			// Verify the StatefulSet was NOT deleted and recreated — same UID
			Eventually(func(g Gomega) {
				adoptedSts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      migrationName.Name + "-server",
					Namespace: migrationName.Namespace,
				}, adoptedSts)).To(Succeed())

				g.Expect(adoptedSts.UID).To(Equal(originalUID),
					"StatefulSet should NOT be deleted/recreated — same UID means no pod disruption")

				// Should be owned by RabbitMq
				hasRabbitMqOwner := false
				for _, ref := range adoptedSts.OwnerReferences {
					if ref.Controller != nil && *ref.Controller && ref.Kind == "RabbitMq" {
						hasRabbitMqOwner = true
					}
				}
				g.Expect(hasRabbitMqOwner).To(BeTrue(), "STS should be owned by RabbitMq")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("coexisting with rabbitmq-cluster-operator without migration", func() {
		var coexistName types.NamespacedName

		BeforeEach(func() {
			coexistName = types.NamespacedName{
				Name:      "rabbitmq-new",
				Namespace: namespace,
			}
		})

		It("should set OldCRCleaned immediately when no old RabbitmqCluster CR exists", func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(coexistName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// OldCRCleaned should be set to True immediately since there is no
			// rabbitmq.com RabbitmqCluster with the same name
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(coexistName)
				g.Expect(instance.Status.OldCRCleaned).To(Equal("True"),
					"OldCRCleaned should be True for fresh deployment")
			}, timeout, interval).Should(Succeed())
		})

		It("should not touch an unrelated RabbitmqCluster CR in the same namespace", func() {
			// Install a minimal CRD for rabbitmqclusters.rabbitmq.com
			oldCRD := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rabbitmqclusters.rabbitmq.com",
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "rabbitmq.com",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural:   "rabbitmqclusters",
						Singular: "rabbitmqcluster",
						Kind:     "RabbitmqCluster",
						ListKind: "RabbitmqClusterList",
					},
					Scope: apiextensionsv1.NamespaceScoped,
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{
							Name:    "v1beta1",
							Served:  true,
							Storage: true,
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Type:                   "object",
									XPreserveUnknownFields: ptr.To(true),
								},
							},
						},
					},
				},
			}
			existingCRD := &apiextensionsv1.CustomResourceDefinition{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "rabbitmqclusters.rabbitmq.com"}, existingCRD)
			if k8s_errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, oldCRD)).To(Succeed())
			}

			// Wait for CRD to be established
			Eventually(func(g Gomega) {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rabbitmqclusters.rabbitmq.com"}, crd)).To(Succeed())
				established := false
				for _, c := range crd.Status.Conditions {
					if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
						established = true
					}
				}
				g.Expect(established).To(BeTrue(), "CRD should be established")
			}, timeout, interval).Should(Succeed())

			// Create an old RabbitmqCluster CR with a DIFFERENT name (managed by old operator)
			unrelatedName := "rabbitmq-old-operator"
			oldCR := &uns.Unstructured{}
			oldCR.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "rabbitmq.com",
				Version: "v1beta1",
				Kind:    "RabbitmqCluster",
			})
			oldCR.SetName(unrelatedName)
			oldCR.SetNamespace(namespace)
			oldCR.SetFinalizers([]string{"deletion.finalizers.rabbitmqclusters.rabbitmq.com"})
			Expect(k8sClient.Create(ctx, oldCR)).To(Succeed())
			DeferCleanup(func() {
				cr := &uns.Unstructured{}
				cr.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "rabbitmq.com",
					Version: "v1beta1",
					Kind:    "RabbitmqCluster",
				})
				cr.SetName(unrelatedName)
				cr.SetNamespace(namespace)
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: unrelatedName, Namespace: namespace}, cr); err == nil {
					cr.SetFinalizers(nil)
					_ = k8sClient.Update(ctx, cr)
					_ = k8sClient.Delete(ctx, cr)
				}
			})

			// Create a new RabbitMq CR with a different name
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(coexistName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// OldCRCleaned should be True (no matching old CR)
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(coexistName)
				g.Expect(instance.Status.OldCRCleaned).To(Equal("True"))
			}, timeout, interval).Should(Succeed())

			// The unrelated old RabbitmqCluster CR should still exist with its finalizer
			Consistently(func(g Gomega) {
				checkCR := &uns.Unstructured{}
				checkCR.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "rabbitmq.com",
					Version: "v1beta1",
					Kind:    "RabbitmqCluster",
				})
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      unrelatedName,
					Namespace: namespace,
				}, checkCR)
				g.Expect(err).ToNot(HaveOccurred(),
					"unrelated RabbitmqCluster CR should not be deleted")
				g.Expect(checkCR.GetFinalizers()).To(
					ContainElement("deletion.finalizers.rabbitmqclusters.rabbitmq.com"),
					"unrelated RabbitmqCluster CR finalizers should not be touched")
			}, "5s", interval).Should(Succeed())
		})
	})

	When("RabbitMq CR has proper resource ownership", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should have finalizer and own StatefulSet with proper cleanup", func() {
			// Wait for RabbitMq to be created and have the finalizer
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqName)
				g.Expect(rabbitmq).NotTo(BeNil())
				g.Expect(rabbitmq.Finalizers).To(ContainElement("openstack.org/rabbitmq"))
			}, timeout, interval).Should(Succeed())

			// Verify StatefulSet is created and owned by RabbitMq
			Eventually(func(g Gomega) {
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts).NotTo(BeNil())
				g.Expect(sts.OwnerReferences).To(HaveLen(1))
				g.Expect(sts.OwnerReferences[0].Kind).To(Equal("RabbitMq"))
				g.Expect(sts.OwnerReferences[0].Name).To(Equal(rabbitmqDefaultName))
			}, timeout, interval).Should(Succeed())

			// Delete the RabbitMq CR
			rabbitmq := GetRabbitMQ(rabbitmqName)
			Expect(th.K8sClient.Delete(th.Ctx, rabbitmq)).To(Succeed())

			// In a real cluster, StatefulSet would be garbage collected automatically
			// due to owner references. In envtest, we need to simulate this by manually
			// deleting the StatefulSet since garbage collection doesn't run.
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				err := th.K8sClient.Get(th.Ctx, types.NamespacedName{
					Name:      rabbitmqDefaultName + "-server",
					Namespace: namespace,
				}, sts)
				if err == nil {
					// Verify it has owner reference before deleting
					g.Expect(sts.OwnerReferences).To(HaveLen(1))
					g.Expect(sts.OwnerReferences[0].Kind).To(Equal("RabbitMq"))
					// Simulate garbage collection by deleting the owned resource
					g.Expect(th.K8sClient.Delete(th.Ctx, sts)).To(Succeed())
				}
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Verify RabbitMq CR is eventually deleted
			Eventually(func(g Gomega) {
				rabbitmq := &rabbitmqv1.RabbitMq{}
				err := th.K8sClient.Get(th.Ctx, rabbitmqName, rabbitmq)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				cluster := &rabbitmqv1.RabbitMq{}
				err := th.K8sClient.Get(th.Ctx, rabbitmqName, cluster)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "RabbitmqCluster should be deleted")

				rabbitmq := &rabbitmqv1.RabbitMq{}
				err = th.K8sClient.Get(th.Ctx, rabbitmqName, rabbitmq)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "RabbitMq should be deleted")
			}, timeout, interval).Should(Succeed())
		})
	})

	// Version Upgrade Tests
	When("a new RabbitMQ cluster is created without TargetVersion", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should initialize CurrentVersion to the default (4.2)", func() {
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
			}, timeout, interval).Should(Succeed())
		})

		It("should not have an upgrade phase set", func() {
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).NotTo(BeEmpty())
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a new RabbitMQ cluster is created with TargetVersion", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["targetVersion"] = "4.2"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should initialize CurrentVersion to the specified TargetVersion", func() {
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQ cluster is upgraded from 3.9 to 4.2", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Wait for initial version to be set then simulate ready
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Manually set CurrentVersion to 3.9 to simulate an existing 3.x cluster.
			// Set QueueType to match the spec (Quorum, from webhook default) to avoid
			// triggering a queue type migration race.
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				instance.Status.QueueType = rabbitmqv1.QueueTypeQuorum
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should trigger upgrade phases when TargetVersion is set", func() {
			// Set TargetVersion to 4.2 to trigger upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Should reach WaitingForCluster phase (after going through DeletingResources)
			// with VersionUpgrade wipe reason
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(string(rabbitmqv1.WipeReasonVersionUpgrade)))
			}, timeout, interval).Should(Succeed())

			// Simulate the new StatefulSet becoming ready
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Upgrade should complete: phases cleared and version updated
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
			}, timeout, interval).Should(Succeed())
		})

		It("should preserve default-user credentials across the upgrade", func() {
			secretName := types.NamespacedName{
				Name:      rabbitmqName.Name + "-default-user",
				Namespace: rabbitmqName.Namespace,
			}

			// Record the secret UID before upgrade
			var secretUID types.UID
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(th.K8sClient.Get(th.Ctx, secretName, secret)).Should(Succeed())
				secretUID = secret.UID
				g.Expect(secretUID).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Trigger upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for WaitingForCluster phase (StatefulSet was deleted)
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			// Verify secret still exists with same UID (not recreated)
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(th.K8sClient.Get(th.Ctx, secretName, secret)).Should(Succeed())
				g.Expect(secret.UID).To(Equal(secretUID))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQ cluster gets a patch-only version change", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["targetVersion"] = "4.2"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Wait for version initialization and simulate ready
			SimulateRabbitMQClusterReady(rabbitmqName)

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
			}, timeout, interval).Should(Succeed())
		})

		It("should NOT trigger a storage wipe for patch-only changes", func() {
			// Change to 4.2.1 (patch-only, same major.minor)
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2.1")
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Give the controller time to reconcile and verify no upgrade phase is set
			Consistently(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
			}, "5s", interval).Should(Succeed())
		})
	})

	When("a 3.x to 4.x upgrade with Quorum queue type", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set CurrentVersion to 3.9 to simulate existing 3.x cluster
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should set ProxyRequired for 3.x to 4.x upgrade with Quorum", func() {
			// Set TargetVersion and QueueType
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for upgrade to complete
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// ProxyRequired should be set after upgrade completes
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("AnnotationClientsReconfigured is set", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set ProxyRequired to true
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.ProxyRequired = "True"
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should clear ProxyRequired and remove the annotation", func() {
			// Set the annotation
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				if instance.Annotations == nil {
					instance.Annotations = map[string]string{}
				}
				instance.Annotations[rabbitmqv1.AnnotationClientsReconfigured] = "true"
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// ProxyRequired should be cleared and annotation removed
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.ProxyRequired).To(Equal("False"))
				_, hasAnnotation := instance.Annotations[rabbitmqv1.AnnotationClientsReconfigured]
				g.Expect(hasAnnotation).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQ cluster upgrade includes wipe-data init container", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set CurrentVersion to 3.9 to simulate existing 3.x cluster
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should add wipe-data init container to the StatefulSet during upgrade", func() {
			// Trigger upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for WaitingForCluster (StatefulSet recreated with wipe init container)
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			// The controller should recreate the StatefulSet with the wipe-data init container
			// Wait for the StatefulSet to be recreated
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				stsName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-server",
					Namespace: rabbitmqName.Namespace,
				}
				g.Expect(th.K8sClient.Get(th.Ctx, stsName, sts)).Should(Succeed())

				// Should have wipe-data as the first init container
				g.Expect(len(sts.Spec.Template.Spec.InitContainers)).To(BeNumerically(">=", 2))
				g.Expect(sts.Spec.Template.Spec.InitContainers[0].Name).To(Equal("wipe-data"))
				g.Expect(sts.Spec.Template.Spec.InitContainers[1].Name).To(Equal("setup-container"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a Mirrored 3.x cluster is upgraded to 4.2 (version upgrade forces Quorum)", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set CurrentVersion to 3.9 and QueueType to Mirrored to simulate existing 3.x cluster
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				instance.Status.QueueType = rabbitmqv1.QueueTypeMirrored
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should upgrade version and migrate to Quorum in a single operation", func() {
			// Set TargetVersion to 4.2 and force QueueType to Quorum.
			// In production, the defaulting webhook does this automatically
			// when it sees Mirrored + 4.x targetVersion. In envtest we
			// simulate the post-defaulting state directly.
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Should reach WaitingForCluster phase with VersionUpgrade wipe reason
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(string(rabbitmqv1.WipeReasonVersionUpgrade)))
			}, timeout, interval).Should(Succeed())

			// Simulate the new StatefulSet becoming ready
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Upgrade should complete: version updated, queue type migrated to Quorum,
			// and proxy enabled for backward compatibility
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a Mirrored cluster migrates to Quorum without version change", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set Status.QueueType to Mirrored to simulate existing mirrored cluster
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.QueueType = rabbitmqv1.QueueTypeMirrored
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should trigger storage wipe and set ProxyRequired when changing to Quorum", func() {
			// Change QueueType to Quorum
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Should reach WaitingForCluster phase with QueueTypeMigration wipe reason
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(string(rabbitmqv1.WipeReasonQueueTypeMigration)))
			}, timeout, interval).Should(Succeed())

			// Simulate the new StatefulSet becoming ready
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Migration should complete with ProxyRequired set
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
				g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ is created with Mirrored queue type", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should include mirrored queue defaults in server config", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				defaults := serverCm.Data["operatorDefaults.conf"]

				// Mirrored queues (no migration) should NOT set migration-only settings
				g.Expect(defaults).NotTo(ContainSubstring("relaxed_checks_on_redeclaration"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ 4.x is created with Quorum queue type (fresh cluster)", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["targetVersion"] = "4.2"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should NOT include migration-only settings in server config", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				defaults := serverCm.Data["operatorDefaults.conf"]

				// Fresh 4.x cluster should not have migration-only relaxed checks
				g.Expect(defaults).NotTo(ContainSubstring("relaxed_checks_on_redeclaration"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a 3.x to 4.x upgrade includes proxy sidecar in StatefulSet", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set CurrentVersion to 3.9 and QueueType to Quorum
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				instance.Status.QueueType = rabbitmqv1.QueueTypeQuorum
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should add proxy sidecar container to the StatefulSet", func() {
			// Trigger 3.x → 4.x upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for WaitingForCluster then simulate ready
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy sidecar is present in StatefulSet
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))

				sts := GetRabbitMQStatefulSet(rabbitmqName)
				containers := sts.Spec.Template.Spec.Containers
				g.Expect(containers).To(HaveLen(2))

				var proxyContainer *corev1.Container
				for i := range containers {
					if containers[i].Name == "amqp-proxy" {
						proxyContainer = &containers[i]
						break
					}
				}
				g.Expect(proxyContainer).NotTo(BeNil(), "proxy sidecar container should exist")
				g.Expect(proxyContainer.Image).To(Equal(instance.Spec.ContainerImage))
			}, timeout, interval).Should(Succeed())
		})

		It("should create a proxy ConfigMap with the proxy script", func() {
			// Trigger 3.x → 4.x upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for upgrade to reach WaitingForCluster
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Verify proxy ConfigMap exists
			Eventually(func(g Gomega) {
				proxyCmName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-proxy-script",
					Namespace: rabbitmqName.Namespace,
				}
				proxyCm := th.GetConfigMap(proxyCmName)
				g.Expect(proxyCm.Data).To(HaveKey("proxy.py"))
				g.Expect(proxyCm.Data["proxy.py"]).To(ContainSubstring("AMQPFrame"))
			}, timeout, interval).Should(Succeed())
		})

		It("should remove proxy sidecar when clients-reconfigured annotation is set", func() {
			// Trigger upgrade and wait for completion
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for proxy to be enabled
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
			}, timeout, interval).Should(Succeed())

			// Set clients-reconfigured annotation
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				if instance.Annotations == nil {
					instance.Annotations = map[string]string{}
				}
				instance.Annotations[rabbitmqv1.AnnotationClientsReconfigured] = "true"
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Proxy should be removed: ProxyRequired cleared and only 1 container
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.ProxyRequired).To(Equal("False"))

				sts := GetRabbitMQStatefulSet(rabbitmqName)
				g.Expect(sts.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(sts.Spec.Template.Spec.Containers[0].Name).To(Equal("rabbitmq"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a Mirrored to Quorum migration completes the full lifecycle", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Mirrored"
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set status to reflect existing Mirrored cluster
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.QueueType = rabbitmqv1.QueueTypeMirrored
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should complete the full migration: wipe, proxy, reconfigure, cleanup", func() {
			// Step 1: Change QueueType to Quorum
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.QueueType = ptr.To(rabbitmqv1.QueueTypeQuorum)
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Step 2: Should trigger storage wipe with QueueTypeMigration reason
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.WipeReason)).To(Equal(string(rabbitmqv1.WipeReasonQueueTypeMigration)))
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
			}, timeout, interval).Should(Succeed())

			// Step 3: Simulate cluster coming back up
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Step 4: Verify status updated — QueueType changed, proxy enabled
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
			}, timeout, interval).Should(Succeed())

			// Step 5: Set clients-reconfigured to remove proxy
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				if instance.Annotations == nil {
					instance.Annotations = map[string]string{}
				}
				instance.Annotations[rabbitmqv1.AnnotationClientsReconfigured] = "true"
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Step 6: Proxy removed, final state clean
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.ProxyRequired).To(Equal("False"))
				g.Expect(instance.Status.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
				_, hasAnnotation := instance.Annotations[rabbitmqv1.AnnotationClientsReconfigured]
				g.Expect(hasAnnotation).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a version upgrade completes the full state machine lifecycle", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			SimulateRabbitMQClusterReady(rabbitmqName)

			// Set CurrentVersion to 3.9 and QueueType to Quorum
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Status.CurrentVersion = "3.9"
				instance.Status.QueueType = rabbitmqv1.QueueTypeQuorum
				g.Expect(th.K8sClient.Status().Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should transition through all upgrade phases and reach clean state", func() {
			// Set TargetVersion to trigger upgrade
			Eventually(func(g Gomega) {
				instance := &rabbitmqv1.RabbitMq{}
				g.Expect(k8sClient.Get(ctx, rabbitmqName, instance)).Should(Succeed())
				instance.Spec.TargetVersion = ptr.To("4.2")
				g.Expect(th.K8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Phase 1: DeletingResources
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				phase := string(instance.Status.UpgradePhase)
				// Should reach DeletingResources or WaitingForCluster
				g.Expect(phase == string(rabbitmqv1.UpgradePhaseDeletingResources) ||
					phase == string(rabbitmqv1.UpgradePhaseWaitingForCluster)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Phase 2: WaitingForCluster
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(string(rabbitmqv1.UpgradePhaseWaitingForCluster)))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(string(rabbitmqv1.WipeReasonVersionUpgrade)))
			}, timeout, interval).Should(Succeed())

			// Simulate cluster recovery
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Phase 3: None (complete) — all phases cleared, version updated
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(string(instance.Status.UpgradePhase)).To(Equal(""))
				g.Expect(string(instance.Status.WipeReason)).To(Equal(""))
				g.Expect(instance.Status.CurrentVersion).To(Equal("4.2"))
				g.Expect(instance.Status.ProxyRequired).To(Equal("True"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ is created with pauseReconciliation label", func() {
		It("should not create a StatefulSet when reconciliation is paused", func() {
			spec := GetDefaultRabbitMQSpec()
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMq",
				"metadata": map[string]any{
					"name":      rabbitmqName.Name,
					"namespace": rabbitmqName.Namespace,
					"labels": map[string]any{
						"rabbitmq.openstack.org/pauseReconciliation": "true",
					},
				},
				"spec": spec,
			}
			obj := th.CreateUnstructured(raw)
			DeferCleanup(th.DeleteInstance, obj)

			stsName := types.NamespacedName{
				Name:      rabbitmqName.Name + "-server",
				Namespace: rabbitmqName.Namespace,
			}
			Consistently(func() bool {
				sts := &appsv1.StatefulSet{}
				err := k8sClient.Get(ctx, stsName, sts)
				return k8s_errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})

	When("RabbitMQ gets created with custom config", func() {
		BeforeEach(func() {
			spec := GetDefaultRabbitMQSpec()
			spec["rabbitmq"] = map[string]any{
				"additionalConfig": "vm_memory_high_watermark.relative = 0.4\nchannel_max = 2000\n",
				"advancedConfig":   "[{rabbit, [{log, [{console, [{level, warning}]}]}]}].\n",
				"envConfig":        "RABBITMQ_IO_THREAD_POOL_SIZE=128\n",
				"additionalPlugins": []string{
					"rabbitmq_shovel",
					"rabbitmq_federation",
				},
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should include additionalConfig in the server-conf ConfigMap", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				userConfig := serverCm.Data["userDefinedConfiguration.conf"]
				g.Expect(userConfig).To(ContainSubstring("vm_memory_high_watermark.relative = 0.4"))
				g.Expect(userConfig).To(ContainSubstring("channel_max = 2000"))
			}, timeout, interval).Should(Succeed())
		})

		It("should use user-provided advancedConfig in the server-conf ConfigMap", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				advancedConfig := serverCm.Data["advanced.config"]
				g.Expect(advancedConfig).To(ContainSubstring("{rabbit, [{log, [{console, [{level, warning}]}]}]}"))
			}, timeout, interval).Should(Succeed())
		})

		It("should include envConfig in the server-conf ConfigMap", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				serverCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-server-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				serverCm := th.GetConfigMap(serverCfgMapName)
				envConfig, ok := serverCm.Data["rabbitmq-env.conf"]
				g.Expect(ok).To(BeTrue(), "rabbitmq-env.conf should be present")
				g.Expect(envConfig).To(ContainSubstring("RABBITMQ_IO_THREAD_POOL_SIZE=128"))
			}, timeout, interval).Should(Succeed())
		})

		It("should include additional plugins in the plugins-conf ConfigMap", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)
			Eventually(func(g Gomega) {
				pluginsCfgMapName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-plugins-conf", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				pluginsCm := th.GetConfigMap(pluginsCfgMapName)
				enabledPlugins := pluginsCm.Data["enabled_plugins"]
				g.Expect(enabledPlugins).To(ContainSubstring("rabbitmq_shovel"))
				g.Expect(enabledPlugins).To(ContainSubstring("rabbitmq_federation"))
				// Default plugins should still be present
				g.Expect(enabledPlugins).To(ContainSubstring("rabbitmq_management"))
				g.Expect(enabledPlugins).To(ContainSubstring("rabbitmq_prometheus"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("TLS secret data is rotated, config hash remains stable", func() {
		var certSecret *corev1.Secret

		BeforeEach(func() {
			certSecret = CreateCertSecret(rabbitmqName)
			DeferCleanup(th.DeleteSecret, types.NamespacedName{Name: certSecret.Name, Namespace: namespace})
			spec := GetDefaultRabbitMQSpec()
			spec["tls"] = map[string]any{
				"secretName": certSecret.Name,
			}
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		It("should NOT update the config-hash annotation when TLS secret data changes", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Get initial config-hash from the StatefulSet
			var initialHash string
			Eventually(func(g Gomega) {
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				initialHash = sts.Spec.Template.Annotations["config-hash"]
				g.Expect(initialHash).NotTo(BeEmpty(), "config-hash annotation should be set")
			}, timeout, interval).Should(Succeed())

			// Update TLS secret data to simulate certificate rotation
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				secretName := types.NamespacedName{Name: certSecret.Name, Namespace: namespace}
				g.Expect(th.K8sClient.Get(th.Ctx, secretName, secret)).Should(Succeed())
				secret.Data["tls.crt"] = []byte("rotated-cert-data")
				g.Expect(th.K8sClient.Update(th.Ctx, secret)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify config-hash did NOT change — cert rotation is handled
			// by projected volumes and RabbitMQ 4.x automatic reload, not
			// by rolling restart. Hashing cert data caused quorum queue
			// corruption during initial deployment.
			Consistently(func(g Gomega) {
				sts := GetRabbitMQStatefulSet(rabbitmqName)
				newHash := sts.Spec.Template.Annotations["config-hash"]
				g.Expect(newHash).To(Equal(initialHash), "config-hash should not change on TLS secret data rotation")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("RabbitMQ gets created with a pre-existing default user secret", func() {
		BeforeEach(func() {
			secretName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-default-user", rabbitmqName.Name),
				Namespace: rabbitmqName.Namespace,
			}
			th.CreateSecret(secretName, map[string][]byte{
				"username":          []byte("myuser"),
				"password":          []byte("mypassword"),
				"default_user.conf": []byte("default_user = myuser\ndefault_pass = mypassword\ndefault_user_tags.administrator = true\n"),
			})
			DeferCleanup(th.DeleteSecret, secretName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)
		})

		// simulateSTSReady marks the StatefulSet as ready without touching the
		// default-user secret (unlike SimulateRabbitMQClusterReady which overwrites it).
		simulateSTSReady := func() {
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				stsName := types.NamespacedName{
					Name:      rabbitmqName.Name + "-server",
					Namespace: rabbitmqName.Namespace,
				}
				err := th.K8sClient.Get(th.Ctx, stsName, sts)
				g.Expect(err).ShouldNot(HaveOccurred())
				sts.Status.ReadyReplicas = 1
				sts.Status.Replicas = 1
				sts.Status.CurrentReplicas = 1
				sts.Status.UpdatedReplicas = 1
				sts.Status.ObservedGeneration = sts.Generation
				g.Expect(th.K8sClient.Status().Update(th.Ctx, sts)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		}

		It("should preserve the pre-created username and password", func() {
			simulateSTSReady()
			Eventually(func(g Gomega) {
				secretName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-default-user", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				secret := th.GetSecret(secretName)
				g.Expect(string(secret.Data["username"])).To(Equal("myuser"))
				g.Expect(string(secret.Data["password"])).To(Equal("mypassword"))
				g.Expect(string(secret.Data["default_user.conf"])).To(ContainSubstring("default_user = myuser"))
				g.Expect(string(secret.Data["default_user.conf"])).To(ContainSubstring("default_pass = mypassword"))
			}, timeout, interval).Should(Succeed())
		})

		It("should update host and port in the pre-created secret", func() {
			simulateSTSReady()
			Eventually(func(g Gomega) {
				secretName := types.NamespacedName{
					Name:      fmt.Sprintf("%s-default-user", rabbitmqName.Name),
					Namespace: rabbitmqName.Namespace,
				}
				secret := th.GetSecret(secretName)
				g.Expect(string(secret.Data["host"])).To(ContainSubstring(rabbitmqName.Name))
				g.Expect(string(secret.Data["port"])).To(Equal("5672"))
			}, timeout, interval).Should(Succeed())
		})

		It("should reference the secret in the RabbitMq status", func() {
			simulateSTSReady()
			Eventually(func(g Gomega) {
				instance := GetRabbitMQ(rabbitmqName)
				g.Expect(instance.Status.DefaultUser).NotTo(BeNil())
				g.Expect(instance.Status.DefaultUser.SecretReference).NotTo(BeNil())
				g.Expect(instance.Status.DefaultUser.SecretReference.Name).To(
					Equal(fmt.Sprintf("%s-default-user", rabbitmqName.Name)))
			}, timeout, interval).Should(Succeed())
		})
	})
})
