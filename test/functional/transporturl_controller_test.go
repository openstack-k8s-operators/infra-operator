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
	"time"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

	When("username is changed, old RabbitMQUser should be automatically deleted", func() {
		var rabbitmqName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-user-cleanup",
				Namespace: namespace,
			}

			// Set up mock RabbitMQ Management API so controller can make API calls
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with initial username
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "olduser",
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should delete the old user resource when username is changed", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("olduser"))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Verify old user exists and has both finalizers
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeTrue())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)).To(BeTrue(),
					"User should have cleanup-blocked finalizer to prevent automatic deletion")
			}, timeout, interval).Should(Succeed())

			// Update the TransportURL spec with new username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-newuser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("newuser"))
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Verify old user's TransportURL finalizer was removed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// User might still exist but should not have the TransportURL finalizer
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse())
				}
			}, timeout, interval).Should(Succeed())

			// Manually remove the cleanup-blocked finalizer to allow deletion to proceed
			// (simulating admin/operator manually approving the deletion)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				if err == nil && controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer) {
					controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)
					g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
				}
			}, timeout, interval).Should(Succeed())

			// Verify old user resource is deleted (or being deleted)
			// With 30s grace period - deletion should happen after grace period elapses
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// Either NotFound or has DeletionTimestamp set
				if err == nil {
					g.Expect(user.DeletionTimestamp.IsZero()).To(BeFalse(), "Old user should have DeletionTimestamp set")
				} else {
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "Old user should be NotFound")
				}
			}, time.Second*45, interval).Should(Succeed()) // Allow time for 30s grace period

			// Verify new user is still present and ready
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
				g.Expect(user.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should NOT delete the old user if it has a nodeset finalizer or cleanup-blocked finalizer", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Verify user has the cleanup-blocked finalizer (added automatically)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)).To(BeTrue(),
					"User should have cleanup-blocked finalizer")
			}, timeout, interval).Should(Succeed())

			// Add a nodeset finalizer to the old user (simulating external usage)
			nodesetFinalizer := "dataplane.openstack.org/nodeset-compute"
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				controllerutil.AddFinalizer(user, nodesetFinalizer)
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify the nodeset finalizer was added
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, nodesetFinalizer)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Update the TransportURL spec with new username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "newuser-protected"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-newuser-protected-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Verify old user's TransportURL finalizer was removed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse())
			}, timeout, interval).Should(Succeed())

			// Verify old user is STILL present (not deleted due to external finalizers)
			// No grace period - external finalizers immediately block deletion
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				// Should NOT have DeletionTimestamp
				g.Expect(user.DeletionTimestamp.IsZero()).To(BeTrue(), "Old user should NOT be marked for deletion")
				// Should still have both the nodeset finalizer and cleanup-blocked finalizer
				g.Expect(controllerutil.ContainsFinalizer(user, nodesetFinalizer)).To(BeTrue())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Cleanup: Remove both finalizers to allow cleanup
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				controllerutil.RemoveFinalizer(user, nodesetFinalizer)
				controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("username is changed with an owner service, deletion waits for owner to reconcile", func() {
		var rabbitmqName types.NamespacedName
		var ownerName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-owner-gen-test",
				Namespace: namespace,
			}
			ownerName = types.NamespacedName{
				Name:      "test-owner-deployment",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create a mock owner service using a StatefulSet (simulating Cinder, Nova, etc.)
			// StatefulSet has status.observedGeneration and conditions
			owner := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "StatefulSet",
					"metadata": map[string]any{
						"name":      ownerName.Name,
						"namespace": ownerName.Namespace,
					},
					"spec": map[string]any{
						"replicas": int64(1),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"containers": []any{
									map[string]any{
										"name":  "test",
										"image": "test:latest",
									},
								},
							},
						},
					},
				},
			}
			owner.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			})
			Expect(k8sClient.Create(ctx, owner)).Should(Succeed())
			DeferCleanup(k8sClient.Delete, owner)

			// Verify the owner was created with generation=1
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				generation, found, err := unstructured.NestedInt64(owner.Object, "metadata", "generation")
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(found).To(BeTrue(), "metadata.generation should be set")
				g.Expect(generation).To(Equal(int64(1)), "metadata.generation should be 1")
			}, timeout, interval).Should(Succeed())

			// Set the status to match generation (observedGeneration=1, Ready=True)
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				g.Expect(unstructured.SetNestedField(owner.Object, int64(1), "status", "observedGeneration")).Should(Succeed())
				g.Expect(unstructured.SetNestedSlice(owner.Object, []any{
					map[string]any{
						"type":   "Ready",
						"status": "True",
					},
				}, "status", "conditions")).Should(Succeed())
				g.Expect(k8sClient.Status().Update(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify the owner status was properly set
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				observedGen, found, err := unstructured.NestedInt64(owner.Object, "status", "observedGeneration")
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(found).To(BeTrue(), "status.observedGeneration should be set")
				g.Expect(observedGen).To(Equal(int64(1)), "status.observedGeneration should be 1")

				conditions, found, err := unstructured.NestedSlice(owner.Object, "status", "conditions")
				g.Expect(err).ShouldNot(HaveOccurred())
				g.Expect(found).To(BeTrue(), "status.conditions should be set")
				g.Expect(conditions).To(HaveLen(1))
			}, timeout, interval).Should(Succeed())

			// Create TransportURL with owner reference
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      transportURLName.Name,
					Namespace: transportURLName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
							Name:       ownerName.Name,
							UID:        owner.GetUID(),
							Controller: func() *bool { b := true; return &b }(),
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "gentest-olduser",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)
		})

		It("should wait for owner observedGeneration to increase before deleting old user", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-gentest-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("gentest-olduser"))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Update the TransportURL spec with new username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "gentest-newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-gentest-newuser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("gentest-newuser"))
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Verify old user's TransportURL finalizer was removed and timestamp annotation was set
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse())
				// Verify the finalizer-removed-at timestamp annotation was set
				g.Expect(user.Annotations).To(HaveKey("rabbitmq.openstack.org/finalizer-removed-at"))
			}, timeout, interval).Should(Succeed())

			// Wait a bit and verify old user is NOT deleted yet (grace period hasn't elapsed)
			Consistently(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				// User should still exist and not have DeletionTimestamp
				g.Expect(user.DeletionTimestamp).To(BeNil(), "Old user should NOT be deleted during grace period")
			}, time.Second*3, interval).Should(Succeed())

			// Manually remove the cleanup-blocked finalizer to allow deletion to proceed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				if err == nil && controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer) {
					controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)
					g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
				}
			}, timeout, interval).Should(Succeed())

			// Now verify old user gets deleted after grace period (30s + buffer)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// Either NotFound or has DeletionTimestamp set
				if err == nil {
					g.Expect(user.DeletionTimestamp.IsZero()).To(BeFalse(), "Old user should have DeletionTimestamp set")
				} else {
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "Old user should be NotFound")
				}
			}, time.Second*45, interval).Should(Succeed())
		})
	})

	When("username is changed but owner service is not ready", func() {
		var rabbitmqName types.NamespacedName
		var transportURLName types.NamespacedName
		var ownerName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-owner-notready",
				Namespace: namespace,
			}
			transportURLName = types.NamespacedName{
				Name:      "transporturl-owner-notready",
				Namespace: namespace,
			}
			ownerName = types.NamespacedName{
				Name:      "owner-notready-deployment",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create a fake owner service (StatefulSet)
			owner := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "StatefulSet",
					"metadata": map[string]any{
						"name":      ownerName.Name,
						"namespace": ownerName.Namespace,
					},
					"spec": map[string]any{
						"replicas": int64(1),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"containers": []any{
									map[string]any{
										"name":  "test",
										"image": "test:latest",
									},
								},
							},
						},
					},
				},
			}
			owner.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			})
			Expect(k8sClient.Create(ctx, owner)).Should(Succeed())
			DeferCleanup(k8sClient.Delete, owner)

			// Set owner status with Ready=True initially
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				g.Expect(unstructured.SetNestedField(owner.Object, int64(1), "metadata", "generation")).Should(Succeed())
				g.Expect(unstructured.SetNestedField(owner.Object, int64(1), "status", "observedGeneration")).Should(Succeed())

				conditions := []any{
					map[string]any{
						"type":   "Ready",
						"status": "True",
					},
				}
				g.Expect(unstructured.SetNestedSlice(owner.Object, conditions, "status", "conditions")).Should(Succeed())
				g.Expect(k8sClient.Status().Update(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Create TransportURL with owner reference
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      transportURLName.Name,
					Namespace: transportURLName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
							Name:       ownerName.Name,
							UID:        owner.GetUID(),
							Controller: func() *bool { b := true; return &b }(),
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "notready-olduser",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)
		})

		It("should wait for owner to be ready before removing finalizer", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-notready-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("notready-olduser"))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Verify old user has the TransportURL finalizer
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Set owner to NOT ready
			Eventually(func(g Gomega) {
				owner := &unstructured.Unstructured{}
				owner.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "StatefulSet",
				})
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())

				conditions := []any{
					map[string]any{
						"type":   "Ready",
						"status": "False",
						"reason": "Reconciling",
					},
				}
				g.Expect(unstructured.SetNestedSlice(owner.Object, conditions, "status", "conditions")).Should(Succeed())
				g.Expect(k8sClient.Status().Update(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Change username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "notready-newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-notready-newuser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Verify old user finalizer is NOT removed (owner not ready)
			Consistently(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeTrue(),
					"TransportURL finalizer should NOT be removed while owner is not ready")
			}, time.Second*5, interval).Should(Succeed())

			// Set owner to Ready
			Eventually(func(g Gomega) {
				owner := &unstructured.Unstructured{}
				owner.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "StatefulSet",
				})
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())

				conditions := []any{
					map[string]any{
						"type":   "Ready",
						"status": "True",
					},
				}
				g.Expect(unstructured.SetNestedSlice(owner.Object, conditions, "status", "conditions")).Should(Succeed())
				g.Expect(k8sClient.Status().Update(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Now verify finalizer is removed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse(),
					"TransportURL finalizer should be removed after owner becomes ready")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("username is changed and owner service is deleted", func() {
		var rabbitmqName types.NamespacedName
		var transportURLName types.NamespacedName
		var ownerName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-owner-deleted",
				Namespace: namespace,
			}
			transportURLName = types.NamespacedName{
				Name:      "transporturl-owner-deleted",
				Namespace: namespace,
			}
			ownerName = types.NamespacedName{
				Name:      "owner-deleted-deployment",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create a fake owner service (StatefulSet)
			owner := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "StatefulSet",
					"metadata": map[string]any{
						"name":      ownerName.Name,
						"namespace": ownerName.Namespace,
					},
					"spec": map[string]any{
						"replicas": int64(1),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"containers": []any{
									map[string]any{
										"name":  "test",
										"image": "test:latest",
									},
								},
							},
						},
					},
				},
			}
			owner.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			})
			Expect(k8sClient.Create(ctx, owner)).Should(Succeed())

			// Set owner status with Ready=True
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				g.Expect(unstructured.SetNestedField(owner.Object, int64(1), "metadata", "generation")).Should(Succeed())
				g.Expect(unstructured.SetNestedField(owner.Object, int64(1), "status", "observedGeneration")).Should(Succeed())

				conditions := []any{
					map[string]any{
						"type":   "Ready",
						"status": "True",
					},
				}
				g.Expect(unstructured.SetNestedSlice(owner.Object, conditions, "status", "conditions")).Should(Succeed())
				g.Expect(k8sClient.Status().Update(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Create TransportURL with owner reference
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      transportURLName.Name,
					Namespace: transportURLName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
							Name:       ownerName.Name,
							UID:        owner.GetUID(),
							Controller: func() *bool { b := true; return &b }(),
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "deleted-olduser",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)
		})

		It("should proceed with cleanup when owner is deleted", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-deleted-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Change username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "deleted-newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-deleted-newuser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Delete the owner service
			Eventually(func(g Gomega) {
				owner := &unstructured.Unstructured{}
				owner.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "StatefulSet",
				})
				g.Expect(k8sClient.Get(ctx, ownerName, owner)).Should(Succeed())
				g.Expect(k8sClient.Delete(ctx, owner)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify owner is deleted
			Eventually(func(g Gomega) {
				owner := &unstructured.Unstructured{}
				owner.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "StatefulSet",
				})
				err := k8sClient.Get(ctx, ownerName, owner)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "Owner should be deleted")
			}, timeout, interval).Should(Succeed())

			// Verify old user's TransportURL finalizer is removed (cleanup proceeds)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// User might still exist but should not have the TransportURL finalizer
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse(),
						"TransportURL finalizer should be removed even when owner is deleted")
				}
			}, timeout, interval).Should(Succeed())
		})
	})

	When("username is changed for standalone TransportURL without owner", func() {
		var rabbitmqName types.NamespacedName
		var transportURLName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-no-owner",
				Namespace: namespace,
			}
			transportURLName = types.NamespacedName{
				Name:      "transporturl-no-owner",
				Namespace: namespace,
			}

			// Create RabbitMQCluster first
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL WITHOUT owner reference (standalone)
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      transportURLName.Name,
					Namespace: transportURLName.Namespace,
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "noowner-olduser",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)
		})

		It("should proceed with cleanup immediately when there is no owner", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-noowner-olduser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Verify TransportURL has NO owner references
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.GetOwnerReferences()).To(BeEmpty(), "TransportURL should have no owner references")
			}, timeout, interval).Should(Succeed())

			// Change username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "noowner-newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-noowner-newuser-user", transportURLName.Name),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// Verify old user's TransportURL finalizer is removed promptly (no owner to wait for)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// User might still exist but should not have the TransportURL finalizer
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)).To(BeFalse(),
						"TransportURL finalizer should be removed immediately when there is no owner")
				}
			}, timeout, interval).Should(Succeed())

			// Manually remove the cleanup-blocked finalizer to allow deletion to proceed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				if err == nil && controllerutil.ContainsFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer) {
					controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)
					g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
				}
			}, timeout, interval).Should(Succeed())

			// Verify cleanup proceeds and user is eventually deleted
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				// Either NotFound or has DeletionTimestamp set
				if err == nil {
					g.Expect(user.DeletionTimestamp.IsZero()).To(BeFalse(), "Old user should have DeletionTimestamp set")
				} else {
					g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "Old user should be NotFound")
				}
			}, time.Second*45, interval).Should(Succeed())
		})
	})

	When("TransportURL is created before RabbitMQ Status.QueueType is set (race condition)", func() {
		BeforeEach(func() {
			// Create RabbitMQ CR with Spec.QueueType=Quorum but without Status.QueueType
			// This simulates the race condition during RabbitMQ 3.9->4.2 upgrade
			spec := GetDefaultRabbitMQSpec()
			spec["queueType"] = "Quorum"
			rabbitmq := CreateRabbitMQ(rabbitmqClusterName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create RabbitMQCluster
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			// Create TransportURL
			transportURLSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, transportURLSpec))
		})

		It("should read Spec.QueueType when Status.QueueType is not yet set", func() {
			// Simulate RabbitMQCluster as ready
			// Note: SimulateRabbitMQClusterReady does NOT set Status.QueueType
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			// Verify RabbitMQ CR has Spec.QueueType set to Quorum
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqClusterName)
				g.Expect(rabbitmq.Spec.QueueType).ToNot(BeNil())
				g.Expect(*rabbitmq.Spec.QueueType).To(Equal(rabbitmqv1.QueueTypeQuorum))
			}, timeout, interval).Should(Succeed())

			// Verify the TransportURL secret contains quorumqueues=true
			// even though Status.QueueType is not set (race condition scenario)
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
				g.Expect(s.Data).To(HaveKeyWithValue("quorumqueues", []byte("true")),
					"TransportURL should set quorumqueues=true based on Spec.QueueType even when Status.QueueType is not yet set")
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL is ready
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})
})
