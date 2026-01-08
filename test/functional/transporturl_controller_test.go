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

			// Wait for RabbitMQUser to be created
			userCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-nova-user-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate RabbitMQUser being ready with the custom vhost
			SimulateRabbitMQUserReady(userCRName, vhostName)

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

			// Verify RabbitMQUser permissions are set to ".*"
			user := GetRabbitMQUser(userCRName)
			Expect(user.Spec.Permissions.Configure).To(Equal(".*"), "Configure permission should be .*")
			Expect(user.Spec.Permissions.Read).To(Equal(".*"), "Read permission should be .*")
			Expect(user.Spec.Permissions.Write).To(Equal(".*"), "Write permission should be .*")

			// Get the user password from the secret
			Expect(user.Status.SecretName).ToNot(BeEmpty(), "User secret should be created")
			userSecret := &corev1.Secret{}
			secretName := types.NamespacedName{
				Name:      user.Status.SecretName,
				Namespace: namespace,
			}
			Expect(k8sClient.Get(ctx, secretName, userSecret)).Should(Succeed())
			Expect(userSecret.Data).To(HaveKey("password"))
			userPassword := string(userSecret.Data["password"])
			Expect(userPassword).ToNot(BeEmpty())

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

	When("TransportURL vhost and username are updated after creation", func() {
		var rabbitmqName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-update-test",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with initial vhost and username
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "initial-user",
				"vhost":               "initial-vhost",
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should update the secret when vhost is changed", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQVhost to be created
			initialVhostCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-initial-vhost-vhost", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, initialVhostCRName, vhost)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate initial vhost being ready
			SimulateRabbitMQVhostReady(initialVhostCRName)

			// Wait for initial RabbitMQUser to be created
			initialUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-initial-user-user", transportURLName.Name),
				Namespace: namespace,
			}
			var initialUserPassword string
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("initial-user"))
				g.Expect(user.Spec.VhostRef).To(Equal(initialVhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready with credentials
			SimulateRabbitMQUserReady(initialUserCRName, "initial-vhost")

			// Get the initial user password from the secret
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Status.SecretName).ToNot(BeEmpty())

				userSecret := &corev1.Secret{}
				secretName := types.NamespacedName{
					Name:      user.Status.SecretName,
					Namespace: namespace,
				}
				g.Expect(k8sClient.Get(ctx, secretName, userSecret)).Should(Succeed())
				g.Expect(userSecret.Data).To(HaveKey("password"))
				initialUserPassword = string(userSecret.Data["password"])
				g.Expect(initialUserPassword).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Verify initial transport URL secret contains initial vhost
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("initial-user"))
				g.Expect(transportURL).To(ContainSubstring("/initial-vhost?ssl=0"))
			}, timeout, interval).Should(Succeed())

			// Verify initial TransportURL status
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("initial-user"))
				g.Expect(tr.Status.RabbitmqVhost).To(Or(Equal("initial-vhost"), Equal("/initial-vhost")))
			}, timeout, interval).Should(Succeed())

			// Update the TransportURL spec with new vhost
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Vhost = "updated-vhost"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQVhost to be created
			updatedVhostCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-updated-vhost-vhost", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, updatedVhostCRName, vhost)).Should(Succeed())
				g.Expect(vhost.Spec.Name).To(Equal("updated-vhost"))
			}, timeout, interval).Should(Succeed())

			// Simulate updated vhost being ready
			SimulateRabbitMQVhostReady(updatedVhostCRName)

			// Wait for RabbitMQUser to be updated with new vhost
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.VhostRef).To(Equal(updatedVhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready with updated vhost
			SimulateRabbitMQUserReady(initialUserCRName, "updated-vhost")

			// Verify transport URL secret is updated with new vhost
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("initial-user"))
				g.Expect(transportURL).To(ContainSubstring("/updated-vhost?ssl=0"))
				g.Expect(transportURL).ToNot(ContainSubstring("/initial-vhost"))
			}, timeout, interval).Should(Succeed())

			// Verify updated TransportURL status
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("initial-user"))
				g.Expect(tr.Status.RabbitmqVhost).To(Or(Equal("updated-vhost"), Equal("/updated-vhost")))
			}, timeout, interval).Should(Succeed())
		})

		It("should update the secret when vhost is added after creation", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Create TransportURL WITHOUT vhost initially (will use default "/")
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "test-user",
			}
			transportURLNoVhost := types.NamespacedName{
				Name:      "foo-no-vhost",
				Namespace: namespace,
			}
			secretNameNoVhost := types.NamespacedName{
				Name:      "rabbitmq-transport-url-foo-no-vhost",
				Namespace: namespace,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLNoVhost, tuSpec))

			// Wait for RabbitMQUser to be created (no vhost, so no RabbitMQVhost CR)
			initialUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-test-user-user", transportURLNoVhost.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.VhostRef).To(BeEmpty(), "Should have no vhost ref initially")
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready with default vhost "/"
			SimulateRabbitMQUserReady(initialUserCRName, "/")

			// Verify initial transport URL uses default vhost "/"
			Eventually(func(g Gomega) {
				s := th.GetSecret(secretNameNoVhost)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("test-user"))
				// Default vhost "/" should appear in the URL
				g.Expect(transportURL).To(MatchRegexp(`/\?ssl=0$`), "Should use default vhost /")
			}, timeout, interval).Should(Succeed())

			// Verify initial TransportURL status shows default vhost
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLNoVhost)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("test-user"))
				g.Expect(tr.Status.RabbitmqVhost).To(Equal("/"))
			}, timeout, interval).Should(Succeed())

			// NOW ADD A VHOST - this is the user's scenario!
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLNoVhost)
				tr.Spec.Vhost = "new-vhost"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for RabbitMQVhost to be created
			newVhostCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-new-vhost-vhost", transportURLNoVhost.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, newVhostCRName, vhost)).Should(Succeed())
				g.Expect(vhost.Spec.Name).To(Equal("new-vhost"))
			}, timeout, interval).Should(Succeed())

			// Simulate new vhost being ready
			SimulateRabbitMQVhostReady(newVhostCRName)

			// Wait for RabbitMQUser to be updated with new vhost reference
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.VhostRef).To(Equal(newVhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready with NEW vhost
			SimulateRabbitMQUserReady(initialUserCRName, "new-vhost")

			// Verify transport URL secret is UPDATED with new vhost
			Eventually(func(g Gomega) {
				s := th.GetSecret(secretNameNoVhost)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("test-user"))
				g.Expect(transportURL).To(ContainSubstring("/new-vhost?ssl=0"))
				// Ensure it's NOT using the default vhost anymore
				g.Expect(transportURL).ToNot(MatchRegexp(`/\?ssl=0$`))
			}, timeout, interval).Should(Succeed())

			// Verify updated TransportURL status
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLNoVhost)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("test-user"))
				g.Expect(tr.Status.RabbitmqVhost).To(Or(Equal("new-vhost"), Equal("/new-vhost")))
			}, timeout, interval).Should(Succeed())
		})

		It("should update the secret when username is changed", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQVhost to be created
			vhostCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-initial-vhost-vhost", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, vhostCRName, vhost)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate vhost being ready
			SimulateRabbitMQVhostReady(vhostCRName)

			// Wait for initial RabbitMQUser to be created
			initialUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-initial-user-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate initial user being ready
			SimulateRabbitMQUserReady(initialUserCRName, "initial-vhost")

			// Verify initial transport URL
			var initialPassword string
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, initialUserCRName, user)).Should(Succeed())
				g.Expect(user.Status.SecretName).ToNot(BeEmpty())

				userSecret := &corev1.Secret{}
				secretName := types.NamespacedName{
					Name:      user.Status.SecretName,
					Namespace: namespace,
				}
				g.Expect(k8sClient.Get(ctx, secretName, userSecret)).Should(Succeed())
				initialPassword = string(userSecret.Data["password"])

				s := th.GetSecret(transportURLSecretName)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("initial-user:%s", initialPassword)))
			}, timeout, interval).Should(Succeed())

			// Update the TransportURL spec with new username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "updated-user"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			updatedUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-updated-user-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, updatedUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("updated-user"))
				g.Expect(user.Spec.VhostRef).To(Equal(vhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate updated user being ready
			SimulateRabbitMQUserReady(updatedUserCRName, "initial-vhost")

			// Verify transport URL secret is updated with new username
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, updatedUserCRName, user)).Should(Succeed())
				g.Expect(user.Status.SecretName).ToNot(BeEmpty())

				userSecret := &corev1.Secret{}
				secretName := types.NamespacedName{
					Name:      user.Status.SecretName,
					Namespace: namespace,
				}
				g.Expect(k8sClient.Get(ctx, secretName, userSecret)).Should(Succeed())
				updatedPassword := string(userSecret.Data["password"])

				s := th.GetSecret(transportURLSecretName)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("updated-user:%s", updatedPassword)))
				g.Expect(transportURL).ToNot(ContainSubstring(fmt.Sprintf("initial-user:%s", initialPassword)))
			}, timeout, interval).Should(Succeed())

			// Verify updated TransportURL status
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("updated-user"))
			}, timeout, interval).Should(Succeed())
		})
	})
})
