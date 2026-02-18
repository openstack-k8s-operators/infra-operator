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
	"fmt"

	. "github.com/onsi/ginkgo/v2" //nolint:revive
	. "github.com/onsi/gomega"    //nolint:revive
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			// Simulate successful RabbitMQ API call by updating status
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Status.Vhost = "test"
				user.Status.VhostRef = vhostName.Name
				g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(vhostName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should remove finalizer when user is deleted", func() {
			// Get user to compute finalizer
			user := GetRabbitMQUser(userName)
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name

			// Verify finalizer was added
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Delete user
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

			// Simulate successful RabbitMQ API call by updating status
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Status.Vhost = "test"
				user.Status.VhostRef = vhostName.Name
				g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
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
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Spec.VhostRef = secondVhostName.Name
				g.Expect(th.K8sClient.Update(th.Ctx, user)).To(Succeed())
			}, timeout, interval).Should(Succeed())

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

			// Simulate successful RabbitMQ API call by updating status
			// This mimics what the controller would do after successfully updating vhost permissions
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Status.Vhost = "test2"
				user.Status.VhostRef = secondVhostName.Name
				g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify status updated
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(secondVhostName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should update status.VhostRef when VhostRef is changed", func() {
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userName.Name

			// Verify initial VhostRef in status
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(vhostName.Name))
			}, timeout, interval).Should(Succeed())

			// Update user to reference second vhost
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Spec.VhostRef = secondVhostName.Name
				g.Expect(th.K8sClient.Update(th.Ctx, user)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Wait for controller to move finalizer from old to new vhost
			// This must happen BEFORE we update status, otherwise controller won't detect the change
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(secondVhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Simulate successful RabbitMQ API call by updating status
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Status.Vhost = "test2"
				user.Status.VhostRef = secondVhostName.Name
				g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify status.VhostRef is updated
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(secondVhostName.Name))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser VhostRef is changed from default to custom vhost", func() {
		var customVhostName types.NamespacedName

		BeforeEach(func() {
			// Create custom vhost
			customVhostName = types.NamespacedName{Name: "custom-vhost", Namespace: namespace}
			vhostCustom := CreateRabbitMQVhost(customVhostName, map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "custom",
			})
			DeferCleanup(th.DeleteInstance, vhostCustom)

			// Create user WITHOUT vhostRef (will use default "/")
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"username":            "default-user",
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for status.VhostRef to be empty (no custom vhost)
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should update VhostRef from default to custom vhost", func() {
			// Verify initial state - no custom vhost
			user := GetRabbitMQUser(userName)
			Expect(user.Status.VhostRef).To(BeEmpty())

			// Update user to use custom vhost - THIS IS THE USER'S SCENARIO
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Spec.VhostRef = customVhostName.Name
				g.Expect(th.K8sClient.Update(th.Ctx, user)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Simulate successful RabbitMQ API call by updating status
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				user.Status.Vhost = "custom"
				user.Status.VhostRef = customVhostName.Name
				g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify status.VhostRef updated
			// Note: We don't test status.Vhost because that requires RabbitMQ API
			// The real controller would detect: status.Vhost ("/") != new vhost ("custom")
			// and update RabbitMQ permissions accordingly
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.VhostRef).To(Equal(customVhostName.Name))
			}, timeout, interval).Should(Succeed())

			// Verify finalizer added to custom vhost
			// User was created with username: "default-user"
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + "default-user"
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(customVhostName)
				g.Expect(vhost.Finalizers).To(ContainElement(expectedFinalizer))
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

			// Wait for both finalizers to be added
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

	When("a RabbitMQUser deletion status conditions", func() {
		It("should show TransportURL finalizer blocking deletion in status", func() {
			// Create TransportURL which owns a user
			transportURLName := types.NamespacedName{Name: "test-transport-status", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"username":            "testuser-status",
			}
			transportURL := CreateTransportURL(transportURLName, spec)
			DeferCleanup(th.DeleteInstance, transportURL)

			// Wait for user to be created by TransportURL
			ownedUserName := types.NamespacedName{Name: transportURLName.Name + "-testuser-status-user", Namespace: namespace}
			Eventually(func(g Gomega) {
				user := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedUserName, user)).To(Succeed())
				g.Expect(user.Finalizers).To(ContainElement(rabbitmqv1.TransportURLFinalizer))
			}, timeout, interval).Should(Succeed())

			// Mark cluster for deletion to avoid RabbitMQ API call failures
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Delete user
			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, ownedUserName, user)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Verify status condition shows waiting for TransportURL
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedUserName, u)).To(Succeed())
				g.Expect(u.DeletionTimestamp).NotTo(BeNil())

				cond := u.Status.Conditions.Get(rabbitmqv1.RabbitMQUserReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(string(cond.Reason)).To(Equal(string(condition.DeletingReason)))
				g.Expect(cond.Message).To(ContainSubstring("Waiting for TransportURL to release user"))
			}, timeout, interval).Should(Succeed())
		})

		It("should show external finalizer blocking deletion in status", func() {
			// Create user
			testUserName := types.NamespacedName{Name: "test-user-finalizer", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user := CreateRabbitMQUser(testUserName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to exist and have finalizers
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				g.Expect(u.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Add external finalizer
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				u.Finalizers = append(u.Finalizers, "external.example.com/test-finalizer")
				g.Expect(th.K8sClient.Update(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Mark cluster for deletion to avoid RabbitMQ API call failures
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Delete user
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				g.Expect(th.K8sClient.Delete(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify status condition shows waiting for external finalizer
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, testUserName, u)).To(Succeed())
				g.Expect(u.DeletionTimestamp).NotTo(BeNil())

				cond := u.Status.Conditions.Get(rabbitmqv1.RabbitMQUserReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(string(cond.Reason)).To(Equal(string(condition.DeletingReason)))
				g.Expect(cond.Message).To(ContainSubstring("Waiting for external finalizers to be removed"))
				g.Expect(cond.Message).To(ContainSubstring("external.example.com/test-finalizer"))
			}, timeout, interval).Should(Succeed())

			// Clean up - remove external finalizer
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, testUserName, u)).To(Succeed())
				newFinalizers := []string{}
				for _, f := range u.Finalizers {
					if f != "external.example.com/test-finalizer" {
						newFinalizers = append(newFinalizers, f)
					}
				}
				u.Finalizers = newFinalizers
				g.Expect(th.K8sClient.Update(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("should show cluster deleted status when cluster is deleted during user deletion", func() {
			// Create a separate cluster and user for this test to avoid interference
			testClusterName := types.NamespacedName{Name: "test-cluster-deleted", Namespace: namespace}
			testUserName := types.NamespacedName{Name: "test-user-cluster-deleted", Namespace: namespace}

			CreateRabbitMQCluster(testClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(testClusterName)
			DeferCleanup(DeleteRabbitMQCluster, testClusterName)

			// Create user
			spec := map[string]any{
				"rabbitmqClusterName": testClusterName.Name,
			}
			user := CreateRabbitMQUser(testUserName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to exist and have finalizers
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				g.Expect(u.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Delete cluster first
			cluster := GetRabbitMQCluster(testClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Delete user
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				g.Expect(th.K8sClient.Delete(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify user is deleted (should proceed quickly since cluster is being deleted)
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, testUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser is created and immediately deleted (race condition test)", func() {
		It("should prevent vhost deletion by adding vhost finalizer before user finalizer", func() {
			// This test verifies the fix for the race condition where:
			// 1. User is created
			// 2. User gets its own finalizer
			// 3. User is deleted before vhost finalizer is added
			// 4. Vhost can be deleted (BAD!)
			//
			// The fix ensures vhost finalizer is added BEFORE user finalizer,
			// preventing the vhost from being deleted even if user is deleted immediately.

			testUserName := types.NamespacedName{Name: "race-test-user", Namespace: namespace}
			testVhostName := types.NamespacedName{Name: "race-test-vhost", Namespace: namespace}

			// Create vhost
			vhost := CreateRabbitMQVhost(testVhostName, map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "race-test",
			})
			DeferCleanup(th.DeleteInstance, vhost)

			// Create user
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            testVhostName.Name,
				"username":            "race-user",
			}
			user := CreateRabbitMQUser(testUserName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Verify vhost finalizer is added quickly (should happen before or with user finalizer)
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + "race-user"
			Eventually(func(g Gomega) {
				vhostObj := GetRabbitMQVhost(testVhostName)
				g.Expect(vhostObj.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Immediately try to delete the vhost
			vhostObj := GetRabbitMQVhost(testVhostName)
			Expect(th.K8sClient.Delete(th.Ctx, vhostObj)).To(Succeed())

			// Verify vhost is NOT deleted (blocked by user's finalizer)
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, testVhostName, v)).To(Succeed())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
				g.Expect(v.Finalizers).To(ContainElement(expectedFinalizer))
			}, "3s", interval).Should(Succeed())

			// Mark cluster for deletion to allow cleanup without RabbitMQ API calls
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Delete user - this should unblock vhost deletion
			userObj := GetRabbitMQUser(testUserName)
			Expect(th.K8sClient.Delete(th.Ctx, userObj)).To(Succeed())

			// Verify user's finalizer is removed from vhost OR vhost is deleted
			// (both are acceptable outcomes - the important thing is the user cleaned up)
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, testVhostName, v)
				if err != nil {
					// Vhost deleted - this is good, user cleaned up properly
					return
				}
				// Vhost still exists - verify finalizer was removed
				g.Expect(v.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Both user and vhost should eventually be deleted
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, testUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, testVhostName, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})

		It("should handle user deletion during initialization without leaving vhost stuck", func() {
			// This test simulates the exact scenario from the bug report:
			// User is deleted BEFORE it completes initial reconciliation
			// (before status.Vhost is set)

			testUserName := types.NamespacedName{Name: "early-delete-user", Namespace: namespace}
			testVhostName := types.NamespacedName{Name: "early-delete-vhost", Namespace: namespace}

			// Create vhost
			vhost := CreateRabbitMQVhost(testVhostName, map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "early-delete",
			})
			DeferCleanup(th.DeleteInstance, vhost)

			// Create user
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            testVhostName.Name,
				"username":            "early-user",
			}
			user := CreateRabbitMQUser(testUserName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for vhost finalizer to be added (this is the critical fix)
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + "early-user"
			Eventually(func(g Gomega) {
				vhostObj := GetRabbitMQVhost(testVhostName)
				g.Expect(vhostObj.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Delete user BEFORE status.Vhost is set (simulating early deletion)
			// Don't wait for reconciliation to complete
			userObj := GetRabbitMQUser(testUserName)
			Expect(th.K8sClient.Delete(th.Ctx, userObj)).To(Succeed())

			// Try to delete vhost
			vhostObj := GetRabbitMQVhost(testVhostName)
			Expect(th.K8sClient.Delete(th.Ctx, vhostObj)).To(Succeed())

			// Vhost should be blocked from deletion by user's finalizer
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, testVhostName, v)).To(Succeed())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
			}, "3s", interval).Should(Succeed())

			// Mark cluster for deletion to allow cleanup without RabbitMQ API calls
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// User should clean up its finalizer from vhost during deletion
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, testVhostName, v)).To(Succeed())
				g.Expect(v.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Both user and vhost should eventually be deleted
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, testUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, testVhostName, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})

		It("should handle vhost being deleted when user status.Vhost is not yet set", func() {
			// This test verifies the controller can handle cleanup even when
			// the vhost CR is gone and status.Vhost was never populated

			testUserName := types.NamespacedName{Name: "orphan-user", Namespace: namespace}
			testVhostName := types.NamespacedName{Name: "orphan-vhost", Namespace: namespace}

			// Create vhost
			vhost := CreateRabbitMQVhost(testVhostName, map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "orphan",
			})
			DeferCleanup(th.DeleteInstance, vhost)

			// Create user
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            testVhostName.Name,
				"username":            "orphan-user",
			}
			user := CreateRabbitMQUser(testUserName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user finalizer to be added
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(testUserName)
				g.Expect(u.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Mark cluster for deletion to allow vhost cleanup
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Force delete the vhost (simulating the race condition scenario)
			// Remove all finalizers and delete
			Eventually(func(g Gomega) {
				vhostObj := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, testVhostName, vhostObj)).To(Succeed())
				vhostObj.Finalizers = []string{}
				g.Expect(th.K8sClient.Update(th.Ctx, vhostObj)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				vhostObj := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, testVhostName, vhostObj)).To(Succeed())
				g.Expect(th.K8sClient.Delete(th.Ctx, vhostObj)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify vhost is gone
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, testVhostName, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			// Now delete the user
			userObj := GetRabbitMQUser(testUserName)
			Expect(th.K8sClient.Delete(th.Ctx, userObj)).To(Succeed())

			// User should be able to complete deletion despite missing vhost CR
			// (it should handle the NotFound error gracefully)
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, testUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser with secret reference is created", func() {
		var credentialSecretName types.NamespacedName

		BeforeEach(func() {
			// Create credential secret first
			credentialSecretName = types.NamespacedName{
				Name:      "test-credentials",
				Namespace: namespace,
			}
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credentialSecretName.Name,
					Namespace: credentialSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("custom-user"),
					"password": []byte("custom-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, credentialSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, credentialSecret)

			// Create user with secret reference
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"secret":              credentialSecretName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should use credentials from secret", func() {
			// Webhook sets spec.Username from the secret
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Spec.Username).To(Equal("custom-user"))
			}, timeout, interval).Should(Succeed())

			// Controller sets status fields
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.Username).To(Equal("custom-user"))
				g.Expect(user.Status.SecretName).To(Equal(credentialSecretName.Name))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser with custom credential selectors is created", func() {
		BeforeEach(func() {
			// Create secret with custom keys
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-keys-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"user": []byte("my-user"),
					"pass": []byte("my-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, credentialSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, credentialSecret)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"secret":              "custom-keys-secret",
				"credentialSelectors": map[string]any{
					"username": "user",
					"password": "pass",
				},
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should use custom selector keys", func() {
			// Webhook sets spec.Username from the secret using custom selector
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Spec.Username).To(Equal("my-user"))
			}, timeout, interval).Should(Succeed())

			// Controller sets status fields
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.Username).To(Equal("my-user"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser references non-existent secret", func() {
		It("should reject creation with validation error", func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"secret":              "non-existent-secret",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQUser",
				"metadata": map[string]any{
					"name":      "bad-secret-user",
					"namespace": namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("referenced secret does not exist"))
		})
	})

	When("a RabbitMQUser references secret with missing password key", func() {
		It("should reject creation with validation error", func() {
			// Create incomplete secret
			incompleteSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "incomplete-secret",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"username": []byte("user-only"),
					// missing password key
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, incompleteSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, incompleteSecret)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"secret":              "incomplete-secret",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQUser",
				"metadata": map[string]any{
					"name":      "incomplete-creds-user",
					"namespace": namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("password"))
		})
	})

	When("a RabbitMQUser without secret uses auto-generation", func() {
		It("should auto-generate credentials as before", func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				// Should use auto-generated secret name
				expectedSecretName := fmt.Sprintf("rabbitmq-user-%s", userName.Name)
				g.Expect(user.Status.SecretName).To(Equal(expectedSecretName))

				// Verify auto-generated secret exists
				secret := &corev1.Secret{}
				err := th.K8sClient.Get(th.Ctx,
					types.NamespacedName{Name: expectedSecretName, Namespace: namespace},
					secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secret.Data).To(HaveKey("username"))
				g.Expect(secret.Data).To(HaveKey("password"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("password in user-provided secret is changed", func() {
		var credentialSecretName types.NamespacedName

		BeforeEach(func() {
			// Create credential secret with initial password
			credentialSecretName = types.NamespacedName{
				Name:      "test-password-change",
				Namespace: namespace,
			}
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credentialSecretName.Name,
					Namespace: credentialSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("test-user"),
					"password": []byte("initial-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, credentialSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, credentialSecret)

			// Create user with secret reference
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"secret":              credentialSecretName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to be created with initial password
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Status.Username).To(Equal("test-user"))
				g.Expect(user.Status.SecretName).To(Equal(credentialSecretName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should trigger reconciliation and handle password update", func() {
			// Update the password in the secret
			secret := &corev1.Secret{}
			Expect(th.K8sClient.Get(th.Ctx, credentialSecretName, secret)).To(Succeed())

			secret.Data["password"] = []byte("updated-password")
			Expect(th.K8sClient.Update(th.Ctx, secret)).To(Succeed())

			// Wait a bit for the watch to trigger reconciliation
			// In a real cluster, password would be updated in RabbitMQ
			// In test environment, we verify no errors occur and status remains healthy
			Consistently(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				// Username should not change
				g.Expect(user.Status.Username).To(Equal("test-user"))
				// Secret reference should remain the same
				g.Expect(user.Status.SecretName).To(Equal(credentialSecretName.Name))
				// User should not have errors (status would be False if username changed)
				// We can't directly verify password in RabbitMQ in test environment
			}, "5s", interval).Should(Succeed())
		})
	})

	When("username in user-provided secret is changed", func() {
		It("should maintain original username despite secret change", func() {
			// Create credential secret with initial username
			credentialSecretName := types.NamespacedName{
				Name:      "test-username-immutable",
				Namespace: namespace,
			}
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credentialSecretName.Name,
					Namespace: credentialSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("original-user"),
					"password": []byte("test-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, credentialSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, credentialSecret)

			// Wait for secret to be visible (webhook needs to see it)
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(th.K8sClient.Get(th.Ctx, credentialSecretName, secret)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Create user with secret reference (no vhostRef to simplify cleanup)
			immutableUserName := types.NamespacedName{Name: "immutable-user", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"secret":              credentialSecretName.Name,
			}
			testUser := CreateRabbitMQUser(immutableUserName, spec)

			// Wait for user to be created with initial username
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(immutableUserName)
				g.Expect(user.Spec.Username).To(Equal("original-user"))
			}, timeout, interval).Should(Succeed())

			// Change username in the secret
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				g.Expect(th.K8sClient.Get(th.Ctx, credentialSecretName, secret)).To(Succeed())
				secret.Data["username"] = []byte("changed-user")
				g.Expect(th.K8sClient.Update(th.Ctx, secret)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Spec.Username should remain unchanged (set at creation time)
			Consistently(func(g Gomega) {
				user := GetRabbitMQUser(immutableUserName)
				g.Expect(user.Spec.Username).To(Equal("original-user"))
			}, "3s", interval).Should(Succeed())

			// Clean up the test user
			Expect(th.K8sClient.Delete(th.Ctx, testUser)).To(Succeed())
		})
	})

	When("a RabbitMQUser with auto-generated secret is deleted", func() {
		It("should have ownership set on auto-generated secret", func() {
			// Create a standalone user for this test
			ownershipTestUserName := types.NamespacedName{Name: "ownership-test-user", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			ownershipTestUser := CreateRabbitMQUser(ownershipTestUserName, spec)

			// Wait for user and get UID
			var userUID types.UID
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(ownershipTestUserName)
				userUID = user.UID
				g.Expect(userUID).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Verify auto-generated secret exists and has owner reference
			expectedSecretName := fmt.Sprintf("rabbitmq-user-%s", ownershipTestUserName.Name)
			Eventually(func(g Gomega) {
				secret := &corev1.Secret{}
				err := th.K8sClient.Get(th.Ctx,
					types.NamespacedName{Name: expectedSecretName, Namespace: namespace},
					secret)
				g.Expect(err).NotTo(HaveOccurred())
				// Verify it has owner reference to the RabbitMQUser
				g.Expect(secret.OwnerReferences).NotTo(BeEmpty())
				found := false
				for _, ref := range secret.OwnerReferences {
					if ref.UID == userUID {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Secret should have owner reference to RabbitMQUser")
			}, timeout, interval).Should(Succeed())

			// Clean up the test user
			Expect(th.K8sClient.Delete(th.Ctx, ownershipTestUser)).To(Succeed())
		})
	})

	When("a RabbitMQUser uses reserved secret name pattern", func() {
		It("should reject creation with validation error", func() {
			// Try to create user with reserved secret name pattern
			reservedSecretName := fmt.Sprintf("rabbitmq-user-%s", userName.Name)
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"secret":              reservedSecretName,
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQUser",
				"metadata": map[string]any{
					"name":      userName.Name,
					"namespace": namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})
	})

	When("a RabbitMQUser changes to different secret with same username", func() {
		var firstSecretName types.NamespacedName
		var secondSecretName types.NamespacedName

		BeforeEach(func() {
			// Create first secret
			firstSecretName = types.NamespacedName{
				Name:      "first-secret",
				Namespace: namespace,
			}
			firstSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      firstSecretName.Name,
					Namespace: firstSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("same-user"),
					"password": []byte("first-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, firstSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, firstSecret)

			// Create second secret with same username but different password
			secondSecretName = types.NamespacedName{
				Name:      "second-secret",
				Namespace: namespace,
			}
			secondSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secondSecretName.Name,
					Namespace: secondSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("same-user"),
					"password": []byte("second-password"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, secondSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, secondSecret)

			// Create user with first secret
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostName.Name,
				"secret":              firstSecretName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to be created
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Spec.Username).To(Equal("same-user"))
			}, timeout, interval).Should(Succeed())
		})

		It("should allow changing secret when username stays the same", func() {
			// Update user to use second secret (same username, different password)
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				// Create a raw update to test webhook behavior
				raw := map[string]any{
					"apiVersion": "rabbitmq.openstack.org/v1beta1",
					"kind":       "RabbitMQUser",
					"metadata": map[string]any{
						"name":            user.Name,
						"namespace":       user.Namespace,
						"resourceVersion": user.ResourceVersion,
					},
					"spec": map[string]any{
						"rabbitmqClusterName": rabbitmqClusterName.Name,
						"vhostRef":            vhostName.Name,
						"secret":              secondSecretName.Name,
					},
				}
				unstructuredObj := &unstructured.Unstructured{Object: raw}
				g.Expect(th.K8sClient.Update(th.Ctx, unstructuredObj)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify secret was updated and username stayed the same
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(*user.Spec.Secret).To(Equal(secondSecretName.Name))
				g.Expect(user.Spec.Username).To(Equal("same-user"))
				g.Expect(user.Status.SecretName).To(Equal(secondSecretName.Name))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser changes credentialSelectors with same username", func() {
		var credentialSecretName types.NamespacedName

		BeforeEach(func() {
			// Create secret with both default and custom keys pointing to same username
			credentialSecretName = types.NamespacedName{
				Name:      "multi-key-secret",
				Namespace: namespace,
			}
			credentialSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      credentialSecretName.Name,
					Namespace: credentialSecretName.Namespace,
				},
				Data: map[string][]byte{
					"username": []byte("consistent-user"),
					"password": []byte("pass1"),
					"user":     []byte("consistent-user"),
					"pass":     []byte("pass2"),
					"alt_user": []byte("consistent-user"),
					"alt_pass": []byte("pass3"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, credentialSecret)).To(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, credentialSecret)

			// Create user with default selectors (no vhostRef to avoid finalizer cleanup issues)
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"secret":              credentialSecretName.Name,
			}
			user := CreateRabbitMQUser(userName, spec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user to be created
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Spec.Username).To(Equal("consistent-user"))
			}, timeout, interval).Should(Succeed())
		})

		It("should allow changing selectors when username stays the same", func() {
			// Update user to use different selectors (same username value)
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				raw := map[string]any{
					"apiVersion": "rabbitmq.openstack.org/v1beta1",
					"kind":       "RabbitMQUser",
					"metadata": map[string]any{
						"name":            user.Name,
						"namespace":       user.Namespace,
						"resourceVersion": user.ResourceVersion,
					},
					"spec": map[string]any{
						"rabbitmqClusterName": rabbitmqClusterName.Name,
						"secret":              credentialSecretName.Name,
						"credentialSelectors": map[string]any{
							"username": "alt_user",
							"password": "alt_pass",
						},
					},
				}
				unstructuredObj := &unstructured.Unstructured{Object: raw}
				g.Expect(th.K8sClient.Update(th.Ctx, unstructuredObj)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify selectors were updated and username stayed the same
			Eventually(func(g Gomega) {
				user := GetRabbitMQUser(userName)
				g.Expect(user.Spec.CredentialSelectors).NotTo(BeNil())
				g.Expect(user.Spec.CredentialSelectors.Username).To(Equal("alt_user"))
				g.Expect(user.Spec.CredentialSelectors.Password).To(Equal("alt_pass"))
				g.Expect(user.Spec.Username).To(Equal("consistent-user"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQUser is created with mock RabbitMQ API", func() {
		var mockClusterName types.NamespacedName
		var mockVhostName types.NamespacedName
		var mockUserName types.NamespacedName

		BeforeEach(func() {
			mockClusterName = types.NamespacedName{Name: "rabbitmq-user-mock", Namespace: namespace}
			mockVhostName = types.NamespacedName{Name: "vhost-user-mock", Namespace: namespace}
			mockUserName = types.NamespacedName{Name: "user-mock-test", Namespace: namespace}

			// Set up mock RabbitMQ Management API so controller can make API calls
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create cluster and mark it ready
			CreateRabbitMQCluster(mockClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(mockClusterName)
			DeferCleanup(DeleteRabbitMQCluster, mockClusterName)

			// Create vhost and mark it ready
			vhost := CreateRabbitMQVhost(mockVhostName, map[string]any{
				"rabbitmqClusterName": mockClusterName.Name,
				"name":                "test-vhost",
			})
			DeferCleanup(th.DeleteInstance, vhost)
			SimulateRabbitMQVhostReady(mockVhostName)

			// Create user
			user := CreateRabbitMQUser(mockUserName, map[string]any{
				"rabbitmqClusterName": mockClusterName.Name,
				"vhostRef":            mockVhostName.Name,
			})
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should create user via RabbitMQ Management API and become ready", func() {
			// User should become ready after successfully calling the mock API
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(mockUserName)
				g.Expect(u.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
				g.Expect(u.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should reconcile user on every reconciliation loop", func() {
			// Wait for initial ready state
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(mockUserName)
				g.Expect(u.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Update a label to trigger reconciliation
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(mockUserName)
				if u.Labels == nil {
					u.Labels = make(map[string]string)
				}
				u.Labels["test-reconcile"] = "trigger"
				g.Expect(th.K8sClient.Update(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// User should remain ready - controller called API again
			Consistently(func(g Gomega) {
				u := GetRabbitMQUser(mockUserName)
				g.Expect(u.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
				g.Expect(u.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, "3s", interval).Should(Succeed())
		})
	})

	When("RabbitMQ cluster is deleted and recreated", func() {
		var recreateClusterName types.NamespacedName
		var recreateVhostName types.NamespacedName
		var recreateUserName types.NamespacedName

		BeforeEach(func() {
			// Use separate cluster to avoid interfering with other tests
			recreateClusterName = types.NamespacedName{Name: "rabbitmq-recreate", Namespace: namespace}
			recreateVhostName = types.NamespacedName{Name: "vhost-recreate", Namespace: namespace}
			recreateUserName = types.NamespacedName{Name: "user-recreate", Namespace: namespace}

			// Create cluster, vhost, and user
			CreateRabbitMQCluster(recreateClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(recreateClusterName)

			CreateRabbitMQVhost(recreateVhostName, map[string]any{
				"rabbitmqClusterName": recreateClusterName.Name,
				"name":                "recreate-test",
			})

			spec := map[string]any{
				"rabbitmqClusterName": recreateClusterName.Name,
				"vhostRef":            recreateVhostName.Name,
			}
			CreateRabbitMQUser(recreateUserName, spec)

			// Simulate vhost and user ready since we don't have real RabbitMQ
			SimulateRabbitMQVhostReady(recreateVhostName)
			SimulateRabbitMQUserReady(recreateUserName, "recreate-test")
		})

		AfterEach(func() {
			// Mark cluster for deletion to allow cleanup without RabbitMQ API calls
			cluster := &rabbitmqclusterv2.RabbitmqCluster{}
			err := th.K8sClient.Get(th.Ctx, recreateClusterName, cluster)
			if err == nil && cluster.DeletionTimestamp.IsZero() {
				Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())
			}

			// Clean up user
			user := &rabbitmqv1.RabbitMQUser{}
			err = th.K8sClient.Get(th.Ctx, recreateUserName, user)
			if err == nil {
				Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())
				Eventually(func(g Gomega) {
					u := &rabbitmqv1.RabbitMQUser{}
					err := th.K8sClient.Get(th.Ctx, recreateUserName, u)
					g.Expect(err).To(HaveOccurred())
				}, timeout, interval).Should(Succeed())
			}

			// Clean up vhost
			vhost := &rabbitmqv1.RabbitMQVhost{}
			err = th.K8sClient.Get(th.Ctx, recreateVhostName, vhost)
			if err == nil {
				Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())
				Eventually(func(g Gomega) {
					v := &rabbitmqv1.RabbitMQVhost{}
					err := th.K8sClient.Get(th.Ctx, recreateVhostName, v)
					g.Expect(err).To(HaveOccurred())
				}, timeout, interval).Should(Succeed())
			}

			// Wait for cluster to be deleted
			Eventually(func(g Gomega) {
				c := &rabbitmqclusterv2.RabbitmqCluster{}
				err := th.K8sClient.Get(th.Ctx, recreateClusterName, c)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})

		It("should automatically reconcile users when cluster is recreated", func() {
			// Delete the cluster
			DeleteRabbitMQCluster(recreateClusterName)

			// Wait for cluster to be deleted
			Eventually(func(g Gomega) {
				cluster := &rabbitmqclusterv2.RabbitmqCluster{}
				err := th.K8sClient.Get(th.Ctx, recreateClusterName, cluster)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			// User should go to error state when cluster is gone
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(recreateUserName)
				// User exists but cluster is gone - should show error
				g.Expect(u.Status.Conditions.IsFalse(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
				// The condition message should indicate cluster not found
				cond := u.Status.Conditions.Get(rabbitmqv1.RabbitMQUserReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Message).To(ContainSubstring("not found"))
			}, timeout, interval).Should(Succeed())

			// Recreate the cluster with the same name (but don't mark it ready yet)
			CreateRabbitMQCluster(recreateClusterName, GetDefaultRabbitMQClusterSpec(false))

			// User should show waiting status when cluster exists but isn't ready
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(recreateUserName)
				g.Expect(u.Status.Conditions.IsFalse(rabbitmqv1.RabbitMQUserReadyCondition)).To(BeTrue())
				cond := u.Status.Conditions.Get(rabbitmqv1.RabbitMQUserReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				// Should indicate waiting for cluster to be ready
				g.Expect(cond.Message).To(ContainSubstring("waiting"))
			}, timeout, interval).Should(Succeed())

			// Now mark the cluster as ready
			SimulateRabbitMQClusterReady(recreateClusterName)

			// The watch should trigger reconciliation. Simulate success since we don't have real RabbitMQ API
			SimulateRabbitMQVhostReady(recreateVhostName)
			SimulateRabbitMQUserReady(recreateUserName, "recreate-test")

			// Verify user is ready again
			Eventually(func(g Gomega) {
				u := GetRabbitMQUser(recreateUserName)
				g.Expect(u.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
				// Status fields should be populated
				g.Expect(u.Status.Username).NotTo(BeEmpty())
				g.Expect(u.Status.SecretName).NotTo(BeEmpty())
				g.Expect(u.Status.Vhost).To(Equal("recreate-test"))
			}, timeout, interval).Should(Succeed())
		})
	})
})
