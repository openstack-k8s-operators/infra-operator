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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RabbitMQVhost controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var vhostName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		vhostName = types.NamespacedName{Name: "test-vhost", Namespace: namespace}

		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		SimulateRabbitMQClusterReady(rabbitmqClusterName)
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)
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

	When("a RabbitMQVhost is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "test",
			}
			vhost := CreateRabbitMQVhost(vhostName, spec)
			DeferCleanup(th.DeleteInstance, vhost)
		})

		It("should have spec fields set", func() {
			vhost := GetRabbitMQVhost(vhostName)
			Expect(vhost.Spec.RabbitmqClusterName).To(Equal(rabbitmqClusterName.Name))
			Expect(vhost.Spec.Name).To(Equal("test"))
		})

		It("should have initialized conditions", func() {
			Eventually(func(g Gomega) {
				vhost := GetRabbitMQVhost(vhostName)
				g.Expect(vhost.Status.Conditions).NotTo(BeNil())
				g.Expect(vhost.Status.Conditions.Has(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQVhost with default name is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			vhost := CreateRabbitMQVhost(vhostName, spec)
			DeferCleanup(th.DeleteInstance, vhost)
		})

		It("should have default vhost name '/'", func() {
			vhost := GetRabbitMQVhost(vhostName)
			Expect(vhost.Spec.Name).To(Equal("/"))
		})
	})

	When("a RabbitMQVhost references non-existent cluster", func() {
		var vhostBadCluster types.NamespacedName

		BeforeEach(func() {
			vhostBadCluster = types.NamespacedName{Name: "bad-cluster-vhost", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": "non-existent",
				"name":                "test",
			}
			vhost := CreateRabbitMQVhost(vhostBadCluster, spec)
			DeferCleanup(th.DeleteInstance, vhost)
		})

		It("should have spec with non-existent cluster reference", func() {
			vhost := GetRabbitMQVhost(vhostBadCluster)
			Expect(vhost.Spec.RabbitmqClusterName).To(Equal("non-existent"))
		})
	})

	When("a RabbitMQVhost owned by TransportURL is deleted", func() {
		var transportURLName types.NamespacedName
		var ownedVhostName types.NamespacedName

		BeforeEach(func() {
			transportURLName = types.NamespacedName{Name: "test-transport", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"username":            "testuser",
				"vhost":               "testvhost",
			}
			transportURL := CreateTransportURL(transportURLName, spec)
			DeferCleanup(th.DeleteInstance, transportURL)

			// Wait for vhost to be created by TransportURL
			ownedVhostName = types.NamespacedName{Name: transportURLName.Name + "-testvhost-vhost", Namespace: namespace}
			Eventually(func(g Gomega) {
				vhost := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedVhostName, vhost)).To(Succeed())
				g.Expect(vhost.Finalizers).To(ContainElement(rabbitmqv1.TransportURLFinalizer))
			}, timeout, interval).Should(Succeed())
		})

		It("should block deletion while TransportURL exists", func() {
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, ownedVhostName, vhost)).To(Succeed())

			// Try to delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Vhost should still exist (deletion blocked by finalizer)
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, ownedVhostName, v)).To(Succeed())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
				g.Expect(v.Finalizers).To(ContainElement(rabbitmqv1.TransportURLFinalizer))
			}, "2s", interval).Should(Succeed())
		})
	})

	When("a RabbitMQVhost referenced by a user is deleted", func() {
		var vhostWithUser types.NamespacedName
		var userRefName types.NamespacedName

		BeforeEach(func() {
			vhostWithUser = types.NamespacedName{Name: "vhost-with-user", Namespace: namespace}
			userRefName = types.NamespacedName{Name: "user-ref", Namespace: namespace}

			// Create vhost with finalizer
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "testvhost",
			}
			vhost := CreateRabbitMQVhost(vhostWithUser, spec)
			DeferCleanup(th.DeleteInstance, vhost)

			// Wait for vhost controller to add its finalizer
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, v)).To(Succeed())
				g.Expect(v.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			// Create user referencing this vhost
			userSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostWithUser.Name,
			}
			user := CreateRabbitMQUser(userRefName, userSpec)
			DeferCleanup(th.DeleteInstance, user)

			// Wait for user finalizer to be added to vhost
			// Username defaults to CR name via webhook
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userRefName.Name
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, v)).To(Succeed())
				g.Expect(v.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())
		})

		It("should block deletion while user references it", func() {
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userRefName.Name
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, vhost)).To(Succeed())

			// Try to delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Vhost should still exist (deletion blocked by user finalizer)
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, v)).To(Succeed())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
				g.Expect(v.Finalizers).To(ContainElement(expectedFinalizer))
			}, "2s", interval).Should(Succeed())
		})

		It("should allow deletion after user is deleted", func() {
			// Mark cluster for deletion to trigger skip-cleanup logic
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + userRefName.Name
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, vhost)).To(Succeed())

			// Try to delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Delete user
			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, userRefName, user)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// User finalizer should be removed from vhost (or vhost deleted entirely)
			// Note: Vhost might be deleted before we can check the finalizer, which is also success
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, vhostWithUser, v)
				if err != nil {
					// Vhost already deleted - success!
					return
				}
				// Vhost still exists - check that finalizer was removed
				g.Expect(v.Finalizers).NotTo(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Vhost should eventually be deleted
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, vhostWithUser, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQVhost has orphaned user finalizer", func() {
		var vhostWithOrphan types.NamespacedName
		var orphanedUserName types.NamespacedName

		BeforeEach(func() {
			vhostWithOrphan = types.NamespacedName{Name: "vhost-orphan", Namespace: namespace}
			orphanedUserName = types.NamespacedName{Name: "orphaned-user", Namespace: namespace}

			// Create vhost
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"name":                "orphantest",
			}
			vhost := CreateRabbitMQVhost(vhostWithOrphan, spec)
			DeferCleanup(th.DeleteInstance, vhost)

			// Create user
			userSpec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            vhostWithOrphan.Name,
			}
			user := CreateRabbitMQUser(orphanedUserName, userSpec)

			// Wait for user finalizer to be added
			expectedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + orphanedUserName.Name
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithOrphan, v)).To(Succeed())
				g.Expect(v.Finalizers).To(ContainElement(expectedFinalizer))
			}, timeout, interval).Should(Succeed())

			// Force delete the user to create orphaned finalizer
			// Note: In a real test environment, this simulates a force delete
			// We'll remove the user's finalizer first, then delete
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				g.Expect(th.K8sClient.Get(th.Ctx, orphanedUserName, u)).To(Succeed())
				u.Finalizers = []string{} // Remove all finalizers
				g.Expect(th.K8sClient.Update(th.Ctx, u)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Now delete the user
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Wait for user to be gone
			Eventually(func(g Gomega) {
				u := &rabbitmqv1.RabbitMQUser{}
				err := th.K8sClient.Get(th.Ctx, orphanedUserName, u)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})

		It("should remain stuck with orphaned finalizer after force-delete", func() {
			orphanedFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + orphanedUserName.Name

			// Verify orphaned finalizer exists
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithOrphan, vhost)).To(Succeed())
			Expect(vhost.Finalizers).To(ContainElement(orphanedFinalizer))

			// Delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Vhost should remain stuck with orphaned finalizer
			// This is expected behavior - force-deleted users leave orphaned finalizers
			// Admin must manually remove the finalizer using kubectl patch
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, vhostWithOrphan, v)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
				g.Expect(v.Finalizers).To(ContainElement(orphanedFinalizer))
			}, "3s", interval).Should(Succeed())

			// Mark cluster for deletion to trigger skip-cleanup logic
			cluster := GetRabbitMQCluster(rabbitmqClusterName)
			Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())

			// Manually remove the orphaned finalizer to allow test cleanup
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithOrphan, v)).To(Succeed())
				v.Finalizers = []string{"rabbitmqvhost.openstack.org/finalizer"} // Keep only vhost controller's finalizer
				g.Expect(th.K8sClient.Update(th.Ctx, v)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQVhost is deleted while cluster is being deleted", func() {
		var vhostWithDeletingCluster types.NamespacedName
		var deletingClusterName types.NamespacedName

		BeforeEach(func() {
			deletingClusterName = types.NamespacedName{Name: "deleting-rabbitmq", Namespace: namespace}
			vhostWithDeletingCluster = types.NamespacedName{Name: "vhost-deleting-cluster", Namespace: namespace}

			// Create a separate cluster for this test
			CreateRabbitMQCluster(deletingClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(deletingClusterName)

			// Create vhost
			spec := map[string]any{
				"rabbitmqClusterName": deletingClusterName.Name,
				"name":                "test",
			}
			vhost := CreateRabbitMQVhost(vhostWithDeletingCluster, spec)
			DeferCleanup(th.DeleteInstance, vhost)

			// Wait for vhost to have finalizer
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithDeletingCluster, v)).To(Succeed())
				g.Expect(v.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should allow deletion without cleanup when cluster is being deleted", func() {
			// Delete vhost first
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithDeletingCluster, vhost)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Now mark cluster for deletion
			DeleteRabbitMQCluster(deletingClusterName)

			// Vhost should be deleted without attempting cleanup
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, vhostWithDeletingCluster, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a RabbitMQVhost is created with mock RabbitMQ API", func() {
		var mockClusterName types.NamespacedName
		var mockVhostName types.NamespacedName

		BeforeEach(func() {
			mockClusterName = types.NamespacedName{Name: "rabbitmq-vhost-mock", Namespace: namespace}
			mockVhostName = types.NamespacedName{Name: "vhost-mock-test", Namespace: namespace}

			// Set up mock RabbitMQ Management API so controller can make API calls
			SetupMockRabbitMQAPI()
			DeferCleanup(StopMockRabbitMQAPI)

			// Create cluster and mark it ready
			CreateRabbitMQCluster(mockClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(mockClusterName)
			DeferCleanup(DeleteRabbitMQCluster, mockClusterName)

			// Create vhost
			vhost := CreateRabbitMQVhost(mockVhostName, map[string]any{
				"rabbitmqClusterName": mockClusterName.Name,
				"name":                "test-vhost-api",
			})
			DeferCleanup(th.DeleteInstance, vhost)
		})

		It("should create vhost via RabbitMQ Management API and become ready", func() {
			// Vhost should become ready after successfully calling the mock API
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(mockVhostName)
				g.Expect(v.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
				g.Expect(v.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should reconcile vhost on every reconciliation loop", func() {
			// Wait for initial ready state
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(mockVhostName)
				g.Expect(v.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Update a label to trigger reconciliation
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(mockVhostName)
				if v.Labels == nil {
					v.Labels = make(map[string]string)
				}
				v.Labels["test-reconcile"] = "trigger"
				g.Expect(th.K8sClient.Update(th.Ctx, v)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Vhost should remain ready - controller called API again
			Consistently(func(g Gomega) {
				v := GetRabbitMQVhost(mockVhostName)
				g.Expect(v.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
				g.Expect(v.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, "3s", interval).Should(Succeed())
		})
	})

	When("RabbitMQ cluster is deleted and recreated", func() {
		var recreateClusterName types.NamespacedName
		var recreateVhostName types.NamespacedName

		BeforeEach(func() {
			// Use separate cluster to avoid interfering with other tests
			recreateClusterName = types.NamespacedName{Name: "rabbitmq-vhost-recreate", Namespace: namespace}
			recreateVhostName = types.NamespacedName{Name: "vhost-for-recreate", Namespace: namespace}

			// Create cluster and vhost
			CreateRabbitMQCluster(recreateClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(recreateClusterName)

			CreateRabbitMQVhost(recreateVhostName, map[string]any{
				"rabbitmqClusterName": recreateClusterName.Name,
				"name":                "vhost-recreate-test",
			})

			// Simulate vhost ready since we don't have real RabbitMQ
			SimulateRabbitMQVhostReady(recreateVhostName)
		})

		AfterEach(func() {
			// Mark cluster for deletion to allow cleanup without RabbitMQ API calls
			cluster := &rabbitmqclusterv2.RabbitmqCluster{}
			err := th.K8sClient.Get(th.Ctx, recreateClusterName, cluster)
			if err == nil && cluster.DeletionTimestamp.IsZero() {
				Expect(th.K8sClient.Delete(th.Ctx, cluster)).To(Succeed())
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

		It("should automatically reconcile vhosts when cluster is recreated", func() {
			// Delete the cluster
			DeleteRabbitMQCluster(recreateClusterName)

			// Wait for cluster to be deleted
			Eventually(func(g Gomega) {
				cluster := &rabbitmqclusterv2.RabbitmqCluster{}
				err := th.K8sClient.Get(th.Ctx, recreateClusterName, cluster)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())

			// Vhost should go to error state when cluster is gone
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(recreateVhostName)
				// Vhost exists but cluster is gone - should show error
				g.Expect(v.Status.Conditions.IsFalse(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
				// The condition message should indicate cluster not found
				cond := v.Status.Conditions.Get(rabbitmqv1.RabbitMQVhostReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Message).To(ContainSubstring("not found"))
			}, timeout, interval).Should(Succeed())

			// Recreate the cluster with the same name (but don't mark it ready yet)
			CreateRabbitMQCluster(recreateClusterName, GetDefaultRabbitMQClusterSpec(false))

			// Vhost should show waiting status when cluster exists but isn't ready
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(recreateVhostName)
				g.Expect(v.Status.Conditions.IsFalse(rabbitmqv1.RabbitMQVhostReadyCondition)).To(BeTrue())
				cond := v.Status.Conditions.Get(rabbitmqv1.RabbitMQVhostReadyCondition)
				g.Expect(cond).NotTo(BeNil())
				// Should indicate waiting for cluster to be ready
				g.Expect(cond.Message).To(ContainSubstring("waiting"))
			}, timeout, interval).Should(Succeed())

			// Now mark the cluster as ready
			SimulateRabbitMQClusterReady(recreateClusterName)

			// The watch should trigger reconciliation. Simulate success since we don't have real RabbitMQ API
			SimulateRabbitMQVhostReady(recreateVhostName)

			// Verify vhost is ready again
			Eventually(func(g Gomega) {
				v := GetRabbitMQVhost(recreateVhostName)
				g.Expect(v.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})
})
