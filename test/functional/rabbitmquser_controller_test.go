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
	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RabbitMQUser controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var vhostName types.NamespacedName
	var userName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		vhostName = types.NamespacedName{Name: "test-vhost", Namespace: namespace}
		userName = types.NamespacedName{Name: "test-user", Namespace: namespace}

		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		SimulateRabbitMQClusterReady(rabbitmqClusterName)
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

		vhost := CreateRabbitMQVhost(vhostName, map[string]any{
			"rabbitmqClusterName": rabbitmqClusterName.Name,
			"name":                "test",
		})
		DeferCleanup(th.DeleteInstance, vhost)
	})

	// Mark cluster for deletion before cleanup phase to trigger skip-cleanup logic
	AfterEach(func() {
		cluster := &rabbitmqclusterv2.RabbitmqCluster{}
		err := th.K8sClient.Get(th.Ctx, rabbitmqClusterName, cluster)
		if err == nil && cluster.DeletionTimestamp.IsZero() {
			// Cluster exists and not being deleted - mark for deletion
			_ = th.K8sClient.Delete(th.Ctx, cluster)
		}
	})

	When("a RabbitMQUser is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should have spec fields set", func() {
			user := GetRabbitMQUser(userName)
			Expect(user.Spec.RabbitmqClusterName).To(Equal(rabbitmqClusterName.Name))
			Expect(user.Spec.VhostRef).To(Equal(vhostName.Name))
		})

		It("should have initialized conditions", func() {
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.Conditions).NotTo(BeNil())
				g.Expect(user.Status.Conditions.Has(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser with custom username is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"username":            "custom-user",
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should have custom username in spec", func() {
			user := GetRabbitMQUser(userName)
			Expect(user.Spec.Username).To(Equal("custom-user"))
		})
	})

	When("a RabbitMQUser with custom permissions is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"permissions": map[string]any{
					"configure": "^queue.*",
					"write":     "^queue.*",
					"read":      ".*",
				},
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should have custom permissions in spec", func() {
			user := GetRabbitMQUser(userName)
			Expect(user.Spec.Permissions.Configure).To(Equal("^queue.*"))
			Expect(user.Spec.Permissions.Write).To(Equal("^queue.*"))
			Expect(user.Spec.Permissions.Read).To(Equal(".*"))
		})
	})

	When("a RabbitMQUser references non-existent vhost", func() {
		It("should reject creation with validation error", func() {
			userWithBadVhost := types.NamespacedName{Name: "bad-vhost-user", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            "non-existent",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQUser",
				"metadata": map[string]any{
					"name":      userWithBadVhost.Name,
					"namespace": userWithBadVhost.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("referenced vhost does not exist"))
		})
	})

	When("a RabbitMQUser owned by TransportURL is deleted", func() {
		var transportURLName types.NamespacedName
		var ownedUserName types.NamespacedName

		BeforeEach(func() {
			transportURLName = types.NamespacedName{Name: "test-transport", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"username":            "testuser",
			}
			transportURL := CreateTransportURL(transportURLName, spec)
			DeferCleanup(th.DeleteInstance, transportURL)

			// Wait for user to be created by TransportURL
			ownedUserName = types.NamespacedName{Name: transportURLName.Name + "-testuser-user", Namespace: namespace}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedUserName, user)).To(Succeed())
				g.Expect(user.Finalizers).To(ContainElement(rabbitmqv1.TransportURLFinalizer))
			}, timeout, interval).Should(Succeed())
		})

		It("should block deletion while TransportURL exists", func() {
			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, ownedUserName, user)).To(Succeed())

			// Try to delete user
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// User should still exist (deletion blocked by finalizer)
			Consistently(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedUserName, u)).To(Succeed())
				g.Expect(u.DeletionTimestamp).NotTo(BeNil())
				g.Expect(u.Finalizers).To(ContainElement(rabbitmqv1.TransportURLFinalizer))
			}, "2s", interval).Should(Succeed())
		})

		It("should allow deletion after TransportURL is deleted", func() {
			// Mark cluster for deletion to trigger skip-cleanup logic
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, ownedUserName, user)).To(Succeed())

			// Try to delete user
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Delete TransportURL
			transportURL := &rabbitmqv1.TransportURL{}
			Expect(th.K8sClient.Get(th.Ctx, transportURLName, transportURL)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, transportURL)).To(Succeed())

			// User should eventually be deleted
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, ownedUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser is created with VhostRef", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should add per-user finalizer to vhost", func() {
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name

			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())
		})

		It("should update status.VhostRef", func() {
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(vhostName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should remove finalizer when user is deleted", func() {
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name

			// Verify finalizer was added
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Delete user
			user := GetRabbitMQUser(userName)
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Verify finalizer was removed from vhost
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser VhostRef is changed", func() {
		var secondVhostName types.NamespacedName

		BeforeEach(func() {
			// Create second vhost
			secondVhostName = types.NamespacedName{Name: "test-vhost-2", Namespace: namespace}
			vhost2 := CreateRabbitMQVhost(secondVhostName, map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "test2",
			})
			DeferCleanup(th.DeleteInstance, vhost2)

			// Create user with first vhost
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for finalizer on first vhost AND status to be updated
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Wait for status.VhostRef to be set
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(vhostName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should move finalizer from old vhost to new vhost", func() {
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name

			// Update user to reference second vhost
			user := GetRabbitMQUser(userName)
			user.Spec.VhostRef = secondVhostName.Name
			Expect(th.K8sClient.Update(th.Ctx, user)).To(Succeed())

			// Verify finalizer removed from first vhost
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Verify finalizer added to second vhost
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(secondVhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Verify status updated
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(secondVhostName.Name))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("multiple RabbitMQUsers reference the same vhost", func() {
		var user2Name types.NamespacedName

		BeforeEach(func() {
			user2Name = types.NamespacedName{Name: "test-user-2", Namespace: namespace}

			// Create first user
			spec1 := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user1 := CreateRabbitMQUser(userName, spec1)
			DeferCleanup(th.DeleteInstance, user1)

			// Create second user
			spec2 := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user2 := CreateRabbitMQUser(user2Name, spec2)
			DeferCleanup(th.DeleteInstance, user2)
		})

		It("should add separate finalizers for each user", func() {
			finalizer1 := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name
			finalizer2 := rabbitmqv1.UserVhostFinalizerPrefix + user2Name.Name

			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(finalizer1))
				g.Expect(vhost.Finalizers).To(ContainElement(finalizer2))
			}, timeout, interval).Should(Succeed())
		})

		It("should keep vhost blocked until all users are deleted", func() {
			finalizer1 := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name
			finalizer2 := rabbitmqv1.UserVhostFinalizerPrefix + user2Name.Name

			// Wait for both finalizers
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(finalizer1))
				g.Expect(vhost.Finalizers).To(ContainElement(finalizer2))
			}, timeout, interval).Should(Succeed())

			// Delete first user
			user1 := GetRabbitMQUser(userName)
			Expect(th.K8sClient.Delete(th.Ctx, user1)).To(Succeed())

			// Verify first user's finalizer removed, second remains
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).NotTo(ContainElement(finalizer1))
				g.Expect(vhost.Finalizers).To(ContainElement(finalizer2))
			}, timeout, interval).Should(Succeed())

			// Delete second user
			user2 := GetRabbitMQUser(user2Name)
			Expect(th.K8sClient.Delete(th.Ctx, user2)).To(Succeed())

			// Verify second user's finalizer also removed
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).NotTo(ContainElement(finalizer2))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser is deleted while cluster is being deleted", func() {
		var userWithDeletingCluster types.NamespacedName
		var deletingClusterName types.NamespacedName
		var testVhostName types.NamespacedName

		BeforeEach(func() {
			deletingClusterName = types.NamespacedName{Name: "deleting-rabbitmq", Namespace: namespace}
			testVhostName = types.NamespacedName{Name: "test-vhost-deleting", Namespace: namespace}
			userWithDeletingCluster = types.NamespacedName{Name: "user-deleting-cluster", Namespace: namespace}

			// Create a separate cluster for this test
			CreateRabbitMQCluster(deletingClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(deletingClusterName)

			// Create vhost
			vhost := CreateRabbitMQVhost(testVhostName, map[string]any{
				"rabbitmqClusterName": deletingClusterName.Name,
				"name":                "test",
			})
			DeferCleanup(th.DeleteInstance, vhost)

			// Create user
			spec := map[string]any{
				"rabbitmqClusterName": deletingClusterName.Name,
				"vhostRef":            testVhostName.Name,
			}
			user := CreateRabbitMQUser(userWithDeletingCluster, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to have finalizer
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, userWithDeletingCluster, u)).To(Succeed())
				g.Expect(u.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should allow deletion without cleanup when cluster is being deleted", func() {
			// Delete user first
			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, userWithDeletingCluster, user)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Now mark cluster for deletion
			DeleteRabbitMQCluster(deletingClusterName)

			// User should be deleted without attempting cleanup
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, userWithDeletingCluster, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			// Clean up orphaned finalizer from vhost if it exists
			// When user is deleted while cluster is being deleted, timing can leave the user's
			// finalizer on the vhost. We need to manually remove it to allow test cleanup.
			orphanedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userWithDeletingCluster.Name
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				if err := th.K8sClient.Get(th.Ctx, testVhostName, vhost); err == nil {
					// Vhost exists - check if it has the orphaned finalizer
					if len(vhost.Finalizers) > 0 {
						// Remove the orphaned user finalizer if present
						newFinalizers := []string{}
						for _, f := range vhost.Finalizers {
							if f != orphanedFinalizer {
								newFinalizers = append(newFinalizers, f)
							}
						}
						vhost.Finalizers = newFinalizers
						g.Expect(th.K8sClient.Update(th.Ctx, vhost)).To(Succeed())
					}
				}
			}, timeout, interval).Should(Succeed())
		})
	})
})
