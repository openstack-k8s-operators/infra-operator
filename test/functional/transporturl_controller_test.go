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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
				g.Expect(s.Data).To(HaveLen(2))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@rabbitmq.%s.svc:5672/?ssl=0", namespace)))
				g.Expect(s.Data).To(HaveKeyWithValue("quorumqueues", []byte("true")))

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
			certSecretName := types.NamespacedName{Name: "cert-rabbitmq-svc", Namespace: namespace}
			CreateCertSecret(certSecretName)
			DeferCleanup(th.DeleteSecret, certSecretName)

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
				g.Expect(s.Data).To(HaveLen(2))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@rabbitmq.%s.svc:5671/?ssl=1", namespace)))

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
				g.Expect(s.Data).To(HaveLen(2))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@rabbitmq.%s.svc:5672/?ssl=0", namespace)))
				g.Expect(s.Data).To(HaveKeyWithValue("quorumqueues", []byte("true")))

			}, timeout, interval).Should(Succeed())

			// update rabbitmq to be tls
			certSecretName := types.NamespacedName{Name: "cert-rabbitmq-svc", Namespace: namespace}
			CreateCertSecret(certSecretName)
			DeferCleanup(th.DeleteSecret, certSecretName)
			UpdateRabbitMQClusterToTLS(rabbitmqClusterName)
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(2))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@rabbitmq.%s.svc:5671/?ssl=1", namespace)))

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

			// Create RabbitMq CR with podOverride
			spec := GetDefaultRabbitMQSpec()
			spec["containerImage"] = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"
			spec["replicas"] = 3
			spec["queueType"] = "Quorum"
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

			// Wait for controller to populate ServiceHostnames from per-pod services
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqName)
				g.Expect(rabbitmq.Status.ServiceHostnames).To(HaveLen(3))
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

			// Create RabbitMq CR with podOverride
			spec := GetDefaultRabbitMQSpec()
			spec["containerImage"] = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"
			spec["replicas"] = 3
			spec["queueType"] = "Quorum"
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
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, vhostName),
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, vhostName, "nova-user"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate RabbitMQUser being ready with the custom vhost
			SimulateRabbitMQUserReady(userCRName, vhostName)

			// Wait for controller to populate ServiceHostnames from per-pod services
			Eventually(func(g Gomega) {
				rabbitmq := GetRabbitMQ(rabbitmqName)
				g.Expect(rabbitmq.Status.ServiceHostnames).To(HaveLen(3))
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
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, "initial-vhost"),
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "initial-vhost", "initial-user"),
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
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, "updated-vhost"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, updatedVhostCRName, vhost)).Should(Succeed())
				g.Expect(vhost.Spec.Name).To(Equal("updated-vhost"))
			}, timeout, interval).Should(Succeed())

			// Simulate updated vhost being ready
			SimulateRabbitMQVhostReady(updatedVhostCRName)

			// In the shared singleton model, changing vhost creates a new canonical user CR
			// (since the canonical name includes the vhost)
			updatedUserCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "updated-vhost", "initial-user"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, updatedUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.VhostRef).To(Equal(updatedVhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready with updated vhost
			SimulateRabbitMQUserReady(updatedUserCRName, "updated-vhost")

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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "test-user"),
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
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, "new-vhost"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, newVhostCRName, vhost)).Should(Succeed())
				g.Expect(vhost.Spec.Name).To(Equal("new-vhost"))
			}, timeout, interval).Should(Succeed())

			// Simulate new vhost being ready
			SimulateRabbitMQVhostReady(newVhostCRName)

			// In the shared singleton model, adding a vhost creates a new canonical user CR
			// (since the canonical name includes the vhost)
			updatedUserCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "new-vhost", "test-user"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, updatedUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.VhostRef).To(Equal(newVhostCRName.Name))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready with NEW vhost
			SimulateRabbitMQUserReady(updatedUserCRName, "new-vhost")

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
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, "initial-vhost"),
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "initial-vhost", "initial-user"),
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "initial-vhost", "updated-user"),
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

	When("two TransportURLs share the same user CR", func() {
		var rabbitmqName types.NamespacedName
		var transportURL1Name types.NamespacedName
		var transportURL2Name types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-shared",
				Namespace: namespace,
			}

			transportURL1Name = types.NamespacedName{
				Name:      "turl-shared-1",
				Namespace: namespace,
			}

			transportURL2Name = types.NamespacedName{
				Name:      "turl-shared-2",
				Namespace: namespace,
			}

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "shared-user",
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURL1Name, tuSpec))
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURL2Name, tuSpec))
		})

		It("should keep the shared user alive when one TransportURL is deleted", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			userCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "shared-user"),
				Namespace: namespace,
			}

			// Wait for user CR to be created with both per-consumer finalizers
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURL1Name.Name))).To(BeTrue())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURL2Name.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userCRName, "/")

			// Delete TransportURL 1
			Eventually(func(g Gomega) {
				tr := &rabbitmqv1.TransportURL{}
				g.Expect(k8sClient.Get(ctx, transportURL1Name, tr)).Should(Succeed())
				g.Expect(k8sClient.Delete(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify: user CR still exists, TransportURL 1's finalizer removed, TransportURL 2's stays
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
				g.Expect(user.DeletionTimestamp.IsZero()).To(BeTrue(), "User should NOT be deleted")
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURL1Name.Name))).To(BeFalse(),
					"Deleted TransportURL's finalizer should be removed")
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURL2Name.Name))).To(BeTrue(),
					"Active TransportURL's finalizer should remain")
				g.Expect(user.Labels[rabbitmqv1.RabbitMQUserOrphanedLabel]).ToNot(Equal("true"),
					"User should NOT be marked orphaned while another consumer exists")
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "olduser"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("olduser"))
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Verify old user exists and has per-consumer finalizer
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Update the TransportURL spec with new username
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = "newuser"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new RabbitMQUser to be created
			newUserCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "newuser"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("newuser"))
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// With no NodeSets in the namespace, the user controller detects that the
			// orphaned user's credentials are not deployed anywhere and deletes the CR.
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user CR should be auto-deleted when no NodeSets exist")
			}, timeout, interval).Should(Succeed())

			// Verify new user is still present and ready
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
				g.Expect(user.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should NOT delete the old user if it has an external finalizer", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial RabbitMQUser to be created
			oldUserCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "olduser"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(oldUserCRName, "/")

			// Add an external finalizer to the old user (simulating external usage)
			nodesetFinalizer := "dataplane.openstack.org/nodeset-compute"
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				controllerutil.AddFinalizer(user, nodesetFinalizer)
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify the external finalizer was added
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "newuser-protected"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// With no NodeSets, the user controller verifies credentials are safe to delete
			// and calls Delete(), but the external finalizer prevents actual removal —
			// CR stays in Terminating state.
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				g.Expect(user.DeletionTimestamp.IsZero()).To(BeFalse(), "Old user should be marked for deletion")
				// External finalizer should still block actual deletion
				g.Expect(controllerutil.ContainsFinalizer(user, nodesetFinalizer)).To(BeTrue(),
					"External finalizer should still be present")
			}, timeout, interval).Should(Succeed())

			// Cleanup: Remove external finalizer to allow full deletion
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, oldUserCRName, user)).Should(Succeed())
				controllerutil.RemoveFinalizer(user, nodesetFinalizer)
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("username is rotated twice in sequence (double rotation)", func() {
		var rabbitmqName types.NamespacedName
		var doubleRotTURLName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-double-rot",
				Namespace: namespace,
			}
			doubleRotTURLName = types.NamespacedName{
				Name:      "turl-double-rot",
				Namespace: namespace,
			}

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "user-a",
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(doubleRotTURLName, tuSpec))
		})

		It("should handle double rotation (rotate twice in sequence)", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// --- Phase 1: initial user "user-a" ---
			userACRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "user-a"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("user-a"))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userACRName, "/")

			// Verify user-a has the per-consumer finalizer
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(doubleRotTURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL is ready with user-a
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(doubleRotTURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("user-a"))
			}, timeout, interval).Should(Succeed())

			// --- Phase 2: rotate to "user-b" ---
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(doubleRotTURLName)
				tr.Spec.Username = "user-b"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			userBCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "user-b"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("user-b"))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userBCRName, "/")

			// With no NodeSets, user-a should be auto-deleted
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"user-a should be auto-deleted after rotation to user-b (no NodeSets)")
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL now uses user-b
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(doubleRotTURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("user-b"))
			}, timeout, interval).Should(Succeed())

			// --- Phase 3: rotate again to "user-c" ---
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(doubleRotTURLName)
				tr.Spec.Username = "user-c"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			userCCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "user-c"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("user-c"))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userCCRName, "/")

			// With no NodeSets, user-b should be auto-deleted
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userBCRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"user-b should be auto-deleted after rotation to user-c (no NodeSets)")
			}, timeout, interval).Should(Succeed())

			// Verify user-a is still gone
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"user-a should still be deleted")
			}, timeout, interval).Should(Succeed())

			// Verify only user-c remains active
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCCRName, user)).Should(Succeed())
				g.Expect(user.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(doubleRotTURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL is ready with user-c
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(doubleRotTURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("user-c"))
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				doubleRotTURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("credential rotation with NodeSet present (two-phase release)", func() {
		var rabbitmqName types.NamespacedName
		var turlName types.NamespacedName
		var nodesetName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-nodeset-phase",
				Namespace: namespace,
			}
			turlName = types.NamespacedName{
				Name:      "turl-nodeset-phase",
				Namespace: namespace,
			}
			nodesetName = types.NamespacedName{
				Name:      "compute-nodeset",
				Namespace: namespace,
			}

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with a Nova ownerReference
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      turlName.Name,
					Namespace: turlName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "nova.openstack.org/v1beta1",
							Kind:       "Nova",
							Name:       "nova",
							UID:        "fake-nova-uid",
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "phase-user-a",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)

			CreateNodeSet(nodesetName)
			DeferCleanup(DeleteNodeSet, nodesetName)
		})

		It("should wait for NodeSet deployment before releasing old user", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for initial user to be created and simulate ready
			userACRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "phase-user-a"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("phase-user-a"))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userACRName, "/")

			// Wait for TransportURL to be ready and its secret to exist
			transportURLSecretName := types.NamespacedName{
				Name:      "rabbitmq-transport-url-" + turlName.Name,
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("phase-user-a"))
				s := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, transportURLSecretName, s)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Add a consumer finalizer to the transport secret, simulating
			// the service operator adopting the secret.
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, transportURLSecretName, secret)).Should(Succeed())
				controllerutil.AddFinalizer(secret, "openstack.org/nova-transport-consumer")
				g.Expect(k8sClient.Update(ctx, secret)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Set NodeSet secretHashes with the current transport URL secret.
			initialHash := GetSecretHash(transportURLSecretName)
			SetNodeSetSecretHashes(nodesetName, map[string]string{
				transportURLSecretName.Name: initialHash,
			})

			// --- Trigger credential rotation ---
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				tr.Spec.Username = "phase-user-b"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for new user to be created
			userBCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "phase-user-b"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("phase-user-b"))
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userBCRName, "/")

			// The consumer finalizer on the old secret gates the release.
			// Wait for rotation state to be set up.
			var newSecretName string
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).ToNot(BeEmpty(),
					"PreviousRabbitmqUserRef should be set during rotation")
				g.Expect(tr.Status.PreviousSecretName).To(Equal(transportURLSecretName.Name),
					"PreviousSecretName should track the old mutable secret")
				g.Expect(tr.Status.SecretName).ToNot(Equal(transportURLSecretName.Name),
					"SecretName should point to the new immutable secret")
				newSecretName = tr.Status.SecretName
			}, timeout, interval).Should(Succeed())

			// Set NodeSet with stale hash for new secret to simulate a
			// config secret regenerated by the service operator but not
			// yet deployed to compute nodes.
			SetNodeSetSecretHashes(nodesetName, map[string]string{
				newSecretName: "stale-before-deploy",
			})

			// Remove consumer finalizer — simulates service operator
			// completing its rollout with the new credentials.
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, transportURLSecretName, secret)).Should(Succeed())
				controllerutil.RemoveFinalizer(secret, "openstack.org/nova-transport-consumer")
				g.Expect(k8sClient.Update(ctx, secret)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Old user should still exist — NodeSet hashes are stale so
			// the controller waits for a dataplane deployment.
			Consistently(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(turlName.Name))).To(BeTrue(),
					"Old user should still have per-consumer finalizer while waiting for deployment")
			}, 5*time.Second, interval).Should(Succeed())

			// --- Simulate NodeSet deployment completing ---
			newHash := GetSecretHash(types.NamespacedName{
				Name:      newSecretName,
				Namespace: namespace,
			})
			SetNodeSetSecretHashes(nodesetName, map[string]string{
				newSecretName: newHash,
			})

			// Hashes now in sync → old user released
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user should be deleted after NodeSet deployment completes")
			}, timeout, interval).Should(Succeed())

			// Verify cleanup
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).To(BeEmpty(),
					"PreviousRabbitmqUserRef should be cleared after release")
				g.Expect(tr.Status.NodeSetSynced).To(BeEmpty(),
					"NodeSetSynced should be cleared after release")
			}, timeout, interval).Should(Succeed())

			// Verify user-b is active and TransportURL is ready
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
				g.Expect(user.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				turlName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("credential rotation for controlplane-only owner (non-EDPM)", func() {
		var rabbitmqName types.NamespacedName
		var turlName types.NamespacedName
		var nodesetName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-cp-only",
				Namespace: namespace,
			}
			turlName = types.NamespacedName{
				Name:      "turl-cp-only",
				Namespace: namespace,
			}
			nodesetName = types.NamespacedName{
				Name:      "compute-cp-only",
				Namespace: namespace,
			}

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with a Cinder ownerReference
			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      turlName.Name,
					Namespace: turlName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "cinder.openstack.org/v1beta1",
							Kind:       "Cinder",
							Name:       "cinder",
							UID:        "fake-cinder-uid",
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "cp-only-user-a",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)

			CreateNodeSet(nodesetName)
			DeferCleanup(DeleteNodeSet, nodesetName)
		})

		It("should release old user immediately without waiting for NodeSet sync", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			userACRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "cp-only-user-a"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userACRName, "/")

			transportURLSecretName := types.NamespacedName{
				Name:      "rabbitmq-transport-url-" + turlName.Name,
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("cp-only-user-a"))
				s := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, transportURLSecretName, s)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			initialHash := GetSecretHash(transportURLSecretName)
			SetNodeSetSecretHashes(nodesetName, map[string]string{
				transportURLSecretName.Name: initialHash,
			})

			// --- Trigger credential rotation ---
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				tr.Spec.Username = "cp-only-user-b"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			userBCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "cp-only-user-b"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userBCRName, "/")

			// Old user should be released immediately — NodeSet hashes
			// are in sync (the tracked secret hasn't changed)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user should be deleted immediately for non-EDPM owner")
			}, timeout, interval).Should(Succeed())

			// Verify cleanup
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).To(BeEmpty())
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

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "noowner-olduser"),
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
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "noowner-newuser"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, newUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate new user being ready
			SimulateRabbitMQUserReady(newUserCRName, "/")

			// With no NodeSets, the user controller verifies credentials are safe
			// to delete and removes the orphaned user CR
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, oldUserCRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user CR should be auto-deleted when no NodeSets exist")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("credential rotation creates immutable secret and respects consumer finalizers", func() {
		var rabbitmqName types.NamespacedName
		var turlName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-immut",
				Namespace: namespace,
			}
			turlName = types.NamespacedName{
				Name:      "turl-immut",
				Namespace: namespace,
			}

			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			tu := &rabbitmqv1.TransportURL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      turlName.Name,
					Namespace: turlName.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "heat.openstack.org/v1beta1",
							Kind:       "Heat",
							Name:       "heat",
							UID:        "fake-heat-uid",
						},
					},
				},
				Spec: rabbitmqv1.TransportURLSpec{
					RabbitmqClusterName: rabbitmqName.Name,
					Username:            "immut-user-a",
				},
			}
			Expect(k8sClient.Create(ctx, tu)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, tu)
		})

		It("should create immutable secret on rotation and wait for consumer finalizer", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			userACRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "immut-user-a"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userACRName, "/")

			// Wait for TransportURL to be ready with mutable secret
			mutableSecretName := types.NamespacedName{
				Name:      "rabbitmq-transport-url-" + turlName.Name,
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("immut-user-a"))
				g.Expect(tr.Status.SecretName).To(Equal(mutableSecretName.Name))
				g.Expect(tr.Status.SecretHash).ToNot(BeEmpty())
				s := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, mutableSecretName, s)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate consumer adding a finalizer to the mutable secret
			consumerFinalizer := "openstack.org/heat-transport-consumer"
			Eventually(func(g Gomega) {
				s := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, mutableSecretName, s)).Should(Succeed())
				controllerutil.AddFinalizer(s, consumerFinalizer)
				g.Expect(k8sClient.Update(ctx, s)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// --- Trigger credential rotation ---
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				tr.Spec.Username = "immut-user-b"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			userBCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "immut-user-b"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userBCRName, "/")

			// Verify rotation created an immutable secret
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.SecretName).ToNot(Equal(mutableSecretName.Name),
					"SecretName should change to the new immutable secret")
				g.Expect(tr.Status.PreviousSecretName).To(Equal(mutableSecretName.Name),
					"PreviousSecretName should track the old mutable secret")
				g.Expect(tr.Status.SecretHash).ToNot(BeEmpty())

				// New secret should exist, be immutable, and have protection finalizer
				newSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tr.Status.SecretName,
					Namespace: namespace,
				}, newSecret)).Should(Succeed())
				g.Expect(newSecret.Immutable).ToNot(BeNil())
				g.Expect(*newSecret.Immutable).To(BeTrue())
				g.Expect(controllerutil.ContainsFinalizer(newSecret,
					rabbitmqv1.TransportSecretProtectionFinalizer)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Old user should NOT be released while consumer finalizer is present
			Consistently(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).ToNot(BeEmpty(),
					"Old user should not be released while consumer holds finalizer")
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
			}, "3s", interval).Should(Succeed())

			// Simulate consumer removing finalizer from old secret (rollout complete)
			Eventually(func(g Gomega) {
				s := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, mutableSecretName, s)).Should(Succeed())
				controllerutil.RemoveFinalizer(s, consumerFinalizer)
				g.Expect(k8sClient.Update(ctx, s)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Old user should now be released
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user should be deleted after consumer releases finalizer")
			}, timeout, interval).Should(Succeed())

			// PreviousSecretName and PreviousRabbitmqUserRef should be cleared
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).To(BeEmpty())
				g.Expect(tr.Status.PreviousSecretName).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should release immediately when no consumer finalizer exists (backward compat)", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			userACRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "immut-user-a"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userACRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userACRName, "/")

			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("immut-user-a"))
			}, timeout, interval).Should(Succeed())

			// Do NOT add any consumer finalizer (simulating old operator)

			// Trigger credential rotation
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				tr.Spec.Username = "immut-user-b"
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			userBCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "immut-user-b"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userBCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(userBCRName, "/")

			// No consumer finalizer → old user released immediately
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userACRName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Old user should be released immediately without consumer finalizer")
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(turlName)
				g.Expect(tr.Status.PreviousRabbitmqUserRef).To(BeEmpty())
				g.Expect(tr.Status.PreviousSecretName).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
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

	When("TransportURL is created without username or userRef (default credentials)", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should use admin credentials from the cluster secret", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			// Verify transport URL uses admin credentials (user/12345678 from cluster secret)
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("user:12345678@"))
				g.Expect(transportURL).To(MatchRegexp(`/\?ssl=0$`), "Should use default vhost /")
			}, timeout, interval).Should(Succeed())

			// Verify no RabbitMQUser or RabbitMQVhost CRs were created
			userList := &rabbitmqv1.RabbitMQUserList{}
			Expect(k8sClient.List(ctx, userList, client.InNamespace(namespace))).Should(Succeed())
			for _, user := range userList.Items {
				// No user should be owned by this TransportURL
				tr := th.GetTransportURL(transportURLName)
				for _, ref := range user.GetOwnerReferences() {
					Expect(ref.UID).ToNot(Equal(tr.UID), "No RabbitMQUser should be owned by TransportURL using default credentials")
				}
			}

			// Verify status reflects admin credentials
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("user"))
				g.Expect(tr.Status.RabbitmqVhost).To(Equal("/"))
				g.Expect(tr.Status.RabbitmqUserRef).To(BeEmpty())
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("TransportURL is created with userRef pointing to an existing RabbitMQUser", func() {
		var rabbitmqName types.NamespacedName
		var externalUserName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-userref",
				Namespace: namespace,
			}

			// Set up mock RabbitMQ Management API
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create RabbitMQCluster
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create an external RabbitMQUser (not owned by TransportURL)
			externalUserName = types.NamespacedName{
				Name:      "external-nova-user",
				Namespace: namespace,
			}
			userSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "nova",
			}
			DeferCleanup(th.DeleteInstance, CreateRabbitMQUser(externalUserName, userSpec))

			// Create TransportURL with userRef
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"userRef":             externalUserName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should wait for the referenced user to be ready and use its credentials", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// TransportURL should be waiting for user to be ready
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionFalse,
			)

			// Simulate the external user being ready
			SimulateRabbitMQUserReady(externalUserName, "/")

			// Verify transport URL uses the external user's credentials
			Eventually(func(g Gomega) {
				// Get the actual password from the user's secret
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, externalUserName, user)).Should(Succeed())
				g.Expect(user.Status.SecretName).ToNot(BeEmpty())
				userSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: user.Status.SecretName, Namespace: namespace}, userSecret)).Should(Succeed())
				userPassword := string(userSecret.Data["password"])

				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring(fmt.Sprintf("nova:%s@", userPassword)))
			}, timeout, interval).Should(Succeed())

			// Verify status reflects the userRef
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("nova"))
				g.Expect(tr.Status.RabbitmqUserRef).To(Equal(externalUserName.Name))
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("TransportURL is created before RabbitMQ cluster is ready", func() {
		BeforeEach(func() {
			// Create RabbitMQ cluster but do NOT simulate it as ready
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should set InProgress condition while cluster is not ready", func() {
			// Cluster is created but not ready (ReadyCount=0)
			// TransportURL should be in InProgress state
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionFalse,
			)

			// Now make the cluster ready
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			// TransportURL should become ready
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveKey("transport_url"))
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("TransportURL with username is deleted", func() {
		var rabbitmqName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-deletion",
				Namespace: namespace,
			}

			// Set up mock RabbitMQ Management API
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create RabbitMQCluster
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with username and vhost
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "delete-test-user",
				"vhost":               "delete-test-vhost",
			}
			CreateTransportURL(transportURLName, tuSpec)
		})

		It("should remove finalizers from owned users and vhosts on deletion", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for RabbitMQVhost and RabbitMQUser to be created
			vhostCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, "delete-test-vhost"),
				Namespace: namespace,
			}
			userCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "delete-test-vhost", "delete-test-user"),
				Namespace: namespace,
			}

			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, vhostCRName, vhost)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(vhost, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Simulate resources ready
			SimulateRabbitMQVhostReady(vhostCRName)
			SimulateRabbitMQUserReady(userCRName, "delete-test-vhost")

			// Verify TransportURL is ready
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			// Delete the TransportURL
			tr := th.GetTransportURL(transportURLName)
			Expect(k8sClient.Delete(ctx, tr)).Should(Succeed())

			// Verify TransportURL finalizer is removed from user
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, userCRName, user)
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeFalse(),
						"TransportURL finalizer should be removed from user")
				}
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				err := k8sClient.Get(ctx, vhostCRName, vhost)
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(vhost, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeFalse(),
						"TransportURL finalizer should be removed from vhost")
				}
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL is eventually deleted
			Eventually(func(g Gomega) {
				tr := &rabbitmqv1.TransportURL{}
				err := k8sClient.Get(ctx, transportURLName, tr)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(), "TransportURL should be deleted")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("TransportURL switches from custom username to default credentials", func() {
		var rabbitmqName types.NamespacedName

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-switch-default",
				Namespace: namespace,
			}

			// Set up mock RabbitMQ Management API
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create RabbitMQCluster
			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			// Create RabbitMq CR
			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			// Create TransportURL with custom username
			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            "custom-user",
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should switch to admin credentials when username is removed from spec", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			// Wait for RabbitMQUser to be created
			customUserCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, "/", "custom-user"),
				Namespace: namespace,
			}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, customUserCRName, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate user being ready
			SimulateRabbitMQUserReady(customUserCRName, "/")

			// Verify TransportURL uses custom user
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("custom-user:"))
			}, timeout, interval).Should(Succeed())

			// Verify status shows custom user
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("custom-user"))
				g.Expect(tr.Status.RabbitmqUserRef).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Remove username from spec to switch to default credentials
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				tr.Spec.Username = ""
				g.Expect(k8sClient.Update(ctx, tr)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify TransportURL switches to admin credentials
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				transportURL := string(s.Data["transport_url"])
				g.Expect(transportURL).To(ContainSubstring("user:12345678@"),
					"Should use admin credentials after removing username")
				g.Expect(transportURL).ToNot(ContainSubstring("custom-user:"),
					"Should no longer use custom user credentials")
			}, timeout, interval).Should(Succeed())

			// Verify status reflects admin credentials
			Eventually(func(g Gomega) {
				tr := th.GetTransportURL(transportURLName)
				g.Expect(tr.Status.RabbitmqUsername).To(Equal("user"))
				g.Expect(tr.Status.RabbitmqUserRef).To(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Verify old user's TransportURL finalizer was removed
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, customUserCRName, user)
				if err == nil {
					g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(transportURLName.Name))).To(BeFalse(),
						"TransportURL finalizer should be removed from old user")
				}
			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("TransportURL is upgraded from legacy per-TransportURL user CRs to shared singletons", func() {
		var migrationClusterName types.NamespacedName
		var migrationTransportURLName types.NamespacedName

		BeforeEach(func() {
			migrationClusterName = types.NamespacedName{Name: "rabbitmq-migration", Namespace: namespace}
			migrationTransportURLName = types.NamespacedName{Name: "migration-transport", Namespace: namespace}

			CreateRabbitMQCluster(migrationClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(migrationClusterName)
			DeferCleanup(DeleteRabbitMQCluster, migrationClusterName)
		})

		It("should clean up the legacy user CR and create the canonical one", func() {
			// Step 1: Create the legacy user CR first (simulating pre-upgrade state).
			// At this point no TransportURL exists yet, so no webhook conflict.
			legacyUserName := types.NamespacedName{
				Name:      migrationTransportURLName.Name + "-migrate-user-user",
				Namespace: namespace,
			}
			legacyUser := CreateRabbitMQUser(legacyUserName, map[string]any{
				"rabbitmqClusterName": migrationClusterName.Name,
				"username":            "migrate-user",
			})
			DeferCleanup(func(_ types.NamespacedName) {
				_ = k8sClient.Delete(ctx, legacyUser)
			}, legacyUserName)

			// Add legacy finalizer
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, legacyUserName, user)).Should(Succeed())
				controllerutil.AddFinalizer(user, rabbitmqv1.TransportURLFinalizer)
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			SimulateRabbitMQUserReady(legacyUserName, "/")

			// Step 2: Create the TransportURL (simulating the new code running after upgrade)
			spec := map[string]any{
				"rabbitmqClusterName": migrationClusterName.Name,
				"username":            "migrate-user",
			}
			turl := CreateTransportURL(migrationTransportURLName, spec)
			DeferCleanup(th.DeleteInstance, turl)

			// Add the owner reference from TransportURL to legacy user CR
			// (simulating what the old controller would have set up)
			turlObj := th.GetTransportURL(migrationTransportURLName)
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, legacyUserName, user)).Should(Succeed())

				isTrue := true
				user.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion:         "rabbitmq.openstack.org/v1beta1",
						Kind:               "TransportURL",
						Name:               migrationTransportURLName.Name,
						UID:                turlObj.UID,
						Controller:         &isTrue,
						BlockOwnerDeletion: &isTrue,
					},
				})
				g.Expect(k8sClient.Update(ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Step 3: The controller should:
			// 1. Find the legacy CR (owner ref match), remove its legacy finalizer
			// 2. Create the canonical user CR (webhook skips legacy CRs with TransportURL owner refs)
			// 3. Legacy CR stays alive until TransportURL is deleted (GC via owner ref)

			canonicalUserName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(migrationClusterName.Name, "/", "migrate-user"),
				Namespace: namespace,
			}

			// Wait for the canonical user CR to be created
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, canonicalUserName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal("migrate-user"))
				g.Expect(user.Spec.RabbitmqClusterName).To(Equal(migrationClusterName.Name))
				g.Expect(controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizerFor(migrationTransportURLName.Name))).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Simulate the canonical user becoming ready
			SimulateRabbitMQUserReady(canonicalUserName, "/")

			// Wait for the legacy user CR to be deleted
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				err := k8sClient.Get(ctx, legacyUserName, user)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue(),
					"Legacy user CR should be deleted after canonical CR exists")
			}, timeout, interval).Should(Succeed())

			// TransportURL should eventually become ready
			th.ExpectCondition(
				migrationTransportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("TransportURL is created with long cluster, user, and vhost names", func() {
		var rabbitmqName types.NamespacedName
		longVhost := "watcher-notifications"
		longUsername := "watcher-notifications"

		BeforeEach(func() {
			rabbitmqName = types.NamespacedName{
				Name:      "rabbitmq-notifications",
				Namespace: namespace,
			}

			CreateRabbitMQCluster(rabbitmqName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqName)

			spec := GetDefaultRabbitMQSpec()
			rabbitmq := CreateRabbitMQ(rabbitmqName, spec)
			DeferCleanup(th.DeleteInstance, rabbitmq)

			tuSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqName.Name,
				"username":            longUsername,
				"vhost":               longVhost,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, tuSpec))
		})

		It("should create user and vhost CRs with names under the 63-char label limit", func() {
			SimulateRabbitMQClusterReady(rabbitmqName)

			vhostCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalVhostName(rabbitmqName.Name, longVhost),
				Namespace: namespace,
			}
			Expect(len(vhostCRName.Name)).To(BeNumerically("<=", 63),
				"Vhost CR name %q is %d chars, must be <= 63", vhostCRName.Name, len(vhostCRName.Name))

			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(k8sClient.Get(ctx, vhostCRName, vhost)).Should(Succeed())
				g.Expect(vhost.Spec.Name).To(Equal(longVhost))
				g.Expect(vhost.Spec.RabbitmqClusterName).To(Equal(rabbitmqName.Name))
			}, timeout, interval).Should(Succeed())
			SimulateRabbitMQVhostReady(vhostCRName)

			userCRName := types.NamespacedName{
				Name:      rabbitmqv1.CanonicalUserName(rabbitmqName.Name, longVhost, longUsername),
				Namespace: namespace,
			}
			Expect(len(userCRName.Name)).To(BeNumerically("<=", 63),
				"User CR name %q is %d chars, must be <= 63", userCRName.Name, len(userCRName.Name))

			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(k8sClient.Get(ctx, userCRName, user)).Should(Succeed())
				g.Expect(user.Spec.Username).To(Equal(longUsername))
				g.Expect(user.Spec.RabbitmqClusterName).To(Equal(rabbitmqName.Name))
			}, timeout, interval).Should(Succeed())
			SimulateRabbitMQUserReady(userCRName, longVhost)

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})
})
