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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
)

var _ = Describe("TransportURL controller", func() {
	var transportURLName types.NamespacedName
	var rabbitmqClusterName types.NamespacedName
	var transportURLSecretName types.NamespacedName

	BeforeEach(func() {
		transportURLName = types.NamespacedName{
			Name:      "foo",
			Namespace: namespace,
		}
		rabbitmqClusterName = types.NamespacedName{
			Name:      "rabbitmq",
			Namespace: namespace,
		}
		transportURLSecretName = types.NamespacedName{
			Name:      "rabbitmq-transport-url-" + transportURLName.Name,
			Namespace: namespace,
		}
	})

	When("a non TLS TransportURL gets created", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should have the Spec fields set", func() {
			tr := th.GetTransportURL(transportURLName)
			Expect(tr.Spec.RabbitmqClusterName).Should(Equal("rabbitmq"))
		})

		It("should have not ready Conditions initialized", func() {
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("should create a secret holding the _NON_ TLS transport url when rabbitmq cluster is ready", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5672/?ssl=0", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			tr := th.GetTransportURL(transportURLName)
			Expect(tr).ToNot(BeNil())
			Expect(tr.Status.SecretName).To(Equal(transportURLSecretName.Name))
		})
	})

	When("a TLS TransportURL gets created", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(true))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should create a secret holding the TLS transport url when rabbitmq cluster is ready", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5671/?ssl=1", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			tr := th.GetTransportURL(transportURLName)
			Expect(tr).ToNot(BeNil())
			Expect(tr.Status.SecretName).To(Equal(transportURLSecretName.Name))
		})
	})

	When("TLS gets enable for RabbitMQ, the TransportURL gets updated", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should update the secret holding the TLS transport url", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			// validate non tls transport_url
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5672/?ssl=0", namespace)))

			}, timeout, interval).Should(Succeed())

			// update rabbitmq to be tls
			UpdateRabbitMQClusterToTLS(rabbitmqClusterName)
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5671/?ssl=1", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("a TransportURL gets created with RabbitMQ using per-pod services", func() {
		var rabbitmqName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-pods",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR with podOverride
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["queueType"] = "None"
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

			// Create TransportURL
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should create a secret with multi-host transport URL", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Simulate RabbitMq CR being ready with ServiceHostnames
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqName)
				g.Expect(rabbitmq).ToNot(BeNil())

				// Populate ServiceHostnames in status
				rabbitmq.Status.ServiceHostnames = []string{
					fmt.Sprintf("%s-server-0.%s.svc", rabbitmqName.Name, namespace),
					fmt.Sprintf("%s-server-1.%s.svc", rabbitmqName.Name, namespace),
					fmt.Sprintf("%s-server-2.%s.svc", rabbitmqName.Name, namespace),
				}
				g.Expect(k8sClient.Status().Update(ctx, rabbitmq)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify transport URL contains all three hosts
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))

				transportURL := string(s.Data["transport_url"])
				// Should contain all three hosts in the format:
				// rabbit://user:12345678@host1:5672,user:12345678@host2:5672,user:12345678@host3:5672/?ssl=0
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("user:12345678@%s-server-0.%s.svc:5672", rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("user:12345678@%s-server-1.%s.svc:5672", rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("user:12345678@%s-server-2.%s.svc:5672", rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring("/?ssl=0"))
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("a TransportURL gets created with RabbitMQ using per-pod services and custom vhost", func() {
		var rabbitmqName types.NamespacedName
		var vhostName string

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-pods-vhost",
				Namespace: namespace,
			}
			vhostName = "nova"

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR with podOverride
			spec := GetDefaultRabbitMQSpec()
			spec["replicas"] = 3
			spec["queueType"] = "None"
			spec["podOverride"] = map[string]any{
				"services": []map[string]any{
					{
						"metadata": map[string]any{
							"annotations": map[string]any{
								"metallb.universe.tf/address-pool":    "internalapi",
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.21",
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
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.22",
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
								"metallb.universe.tf/loadBalancerIPs": "192.0.2.23",
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

			// Create TransportURL with custom vhost
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "nova-user",
				"vhost":               vhostName,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should create a secret with multi-host transport URL including custom vhost", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for and simulate RabbitMQVhost being ready
			vhostCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s-vhost", transportURLName.Name, vhostName),
				Namespace: namespace,
			}
			// First wait for the vhost CR to be created by the TransportURL controller
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, vhostCRName, vhost)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
			// Now simulate it as ready
			SimulateRabbitMQVhostReady(vhostCRName)

			// Simulate RabbitMq CR being ready with ServiceHostnames
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqName)
				g.Expect(rabbitmq).ToNot(BeNil())

				// Populate ServiceHostnames in status
				rabbitmq.Status.ServiceHostnames = []string{
					fmt.Sprintf("%s-server-0.%s.svc", rabbitmqName.Name, namespace),
					fmt.Sprintf("%s-server-1.%s.svc", rabbitmqName.Name, namespace),
					fmt.Sprintf("%s-server-2.%s.svc", rabbitmqName.Name, namespace),
				}
				g.Expect(k8sClient.Status().Update(ctx, rabbitmq)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for RabbitMQUser to be created and ready
			var userPassword string
			Eventually(func(g Gomega) {
				userList := &rabbitmqv1.RabbitMQUserList{}
				g.Expect(k8sClient.List(ctx, userList, client.InNamespace(namespace))).Should(Succeed())
				g.Expect(userList.Items).ToNot(BeEmpty())

				// Find the user created by this TransportURL
				var user *rabbitmqv1.RabbitMQUser
				for i := range userList.Items {
					if userList.Items[i].Spec.Username == "nova-user" {
						user = &userList.Items[i]
						break
					}
				}
				g.Expect(user).ToNot(BeNil(), "nova-user should be created")

				// Wait for the user secret to be created by the controller
				g.Expect(user.Status.SecretName).ToNot(BeEmpty(), "User secret should be created")

				// Get the user secret to extract the actual password
				userSecret := &corev1.Secret{}
				secretName := types.NamespacedName{
					Name:      user.Status.SecretName,
					Namespace: namespace,
				}
				g.Expect(k8sClient.Get(ctx, secretName, userSecret)).Should(Succeed())
				g.Expect(userSecret.Data).To(HaveKey("password"))
				userPassword = string(userSecret.Data["password"])
				g.Expect(userPassword).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Verify transport URL contains all three hosts with custom vhost and actual credentials
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))

				transportURL := string(s.Data["transport_url"])
				// Should contain all three hosts with custom vhost in the format:
				// rabbit://nova-user:<password>@host1:5672,nova-user:<password>@host2:5672,nova-user:<password>@host3:5672/nova?ssl=0
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("nova-user:%s@%s-server-0.%s.svc:5672", userPassword, rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("nova-user:%s@%s-server-1.%s.svc:5672", userPassword, rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("nova-user:%s@%s-server-2.%s.svc:5672", userPassword, rabbitmqName.Name, namespace)))
				g.Expect(transportURL).To(ContainSubstring("/nova?ssl=0"))
				g.Expect(transportURL).To(HavePrefix("rabbit://"))
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			// Verify TransportURL status
			tr := th.GetTransportURL(transportURLName)
			Expect(tr.Status.RabbitmqUsername).To(Equal("nova-user"))
			// Vhost in status is stored without leading slash but used with slash in the URL
			Expect(tr.Status.RabbitmqVhost).To(Or(Equal("nova"), Equal("/nova")))
		})
	})
})
