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

var _ = Describe("RabbitMQPolicy controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var policyName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		policyName = types.NamespacedName{Name: "test-policy", Namespace: namespace}

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

	When("a RabbitMQPolicy is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"pattern":             ".*",
				"definition": map[string]interface{}{
					"max-length": 10000,
				},
			}
			policy := CreateRabbitMQPolicy(policyName, spec)
			DeferCleanup(th.DeleteInstance, policy)
		})

		It("should have spec fields set", func() {
			policy := GetRabbitMQPolicy(policyName)
			Expect(policy.Spec.RabbitmqClusterName).To(Equal(rabbitmqClusterName.Name))
			Expect(policy.Spec.Pattern).To(Equal(".*"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("max-length"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("10000"))
		})
	})

	When("a RabbitMQPolicy with custom settings is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"pattern":             "^queue.*",
				"definition": map[string]interface{}{
					"max-length": 1000,
					"expires":    3600000,
				},
				"priority": 10,
				"applyTo":  "queues",
			}
			policy := CreateRabbitMQPolicy(policyName, spec)
			DeferCleanup(th.DeleteInstance, policy)
		})

		It("should have custom settings in spec", func() {
			policy := GetRabbitMQPolicy(policyName)
			Expect(policy.Spec.Pattern).To(Equal("^queue.*"))
			Expect(policy.Spec.Priority).To(Equal(10))
			Expect(policy.Spec.ApplyTo).To(Equal("queues"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("max-length"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("1000"))
		})
	})

	When("a RabbitMQPolicy references non-existent cluster", func() {
		var policyBadCluster types.NamespacedName

		BeforeEach(func() {
			policyBadCluster = types.NamespacedName{Name: "bad-cluster-policy", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": "non-existent",
				"pattern":             ".*",
				"definition": map[string]interface{}{
					"max-length": 10000,
				},
			}
			policy := CreateRabbitMQPolicy(policyBadCluster, spec)
			DeferCleanup(th.DeleteInstance, policy)
		})

		It("should have spec with non-existent cluster reference", func() {
			policy := GetRabbitMQPolicy(policyBadCluster)
			Expect(policy.Spec.RabbitmqClusterName).To(Equal("non-existent"))
		})
	})

	When("a RabbitMQPolicy is created with mock RabbitMQ API", func() {
		var mockClusterName types.NamespacedName
		var mockVhostName types.NamespacedName
		var mockPolicyName types.NamespacedName

		BeforeEach(func() {
			mockClusterName = types.NamespacedName{Name: "rabbitmq-policy-mock", Namespace: namespace}
			mockVhostName = types.NamespacedName{Name: "vhost-policy-mock", Namespace: namespace}
			mockPolicyName = types.NamespacedName{Name: "policy-mock-test", Namespace: namespace}

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

			// Create policy
			policy := CreateRabbitMQPolicy(mockPolicyName, map[string]any{
				"rabbitmqClusterName": mockClusterName.Name,
				"vhostRef":            mockVhostName.Name,
				"pattern":             ".*",
				"definition": map[string]interface{}{
					"max-length": 10000,
				},
			})
			DeferCleanup(th.DeleteInstance, policy)
		})

		It("should create policy via RabbitMQ Management API and become ready", func() {
			// Policy should become ready after successfully calling the mock API
			Eventually(func(g Gomega) {
				p := GetRabbitMQPolicy(mockPolicyName)
				g.Expect(p.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQPolicyReadyCondition)).To(BeTrue())
				g.Expect(p.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should reconcile policy on every reconciliation loop", func() {
			// Wait for initial ready state
			Eventually(func(g Gomega) {
				p := GetRabbitMQPolicy(mockPolicyName)
				g.Expect(p.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQPolicyReadyCondition)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			// Update a label to trigger reconciliation
			Eventually(func(g Gomega) {
				p := GetRabbitMQPolicy(mockPolicyName)
				if p.Labels == nil {
					p.Labels = make(map[string]string)
				}
				p.Labels["test-reconcile"] = "trigger"
				g.Expect(th.K8sClient.Update(th.Ctx, p)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			// Policy should remain ready - controller called API again
			Consistently(func(g Gomega) {
				p := GetRabbitMQPolicy(mockPolicyName)
				g.Expect(p.Status.Conditions.IsTrue(rabbitmqv1.RabbitMQPolicyReadyCondition)).To(BeTrue())
				g.Expect(p.Status.Conditions.IsTrue(condition.ReadyCondition)).To(BeTrue())
			}, "3s", interval).Should(Succeed())
		})
	})

	When("a RabbitMQPolicy is deleted while cluster is being deleted", func() {
		var policyWithDeletingCluster types.NamespacedName
		var deletingClusterName types.NamespacedName

		BeforeEach(func() {
			deletingClusterName = types.NamespacedName{Name: "deleting-rabbitmq", Namespace: namespace}
			policyWithDeletingCluster = types.NamespacedName{Name: "policy-deleting-cluster", Namespace: namespace}

			// Create a separate cluster for this test
			CreateRabbitMQCluster(deletingClusterName, GetDefaultRabbitMQClusterSpec(false))
			SimulateRabbitMQClusterReady(deletingClusterName)

			// Create policy
			spec := map[string]any{
				"rabbitmqClusterName": deletingClusterName.Name,
				"pattern":             ".*",
				"definition": map[string]interface{}{
					"max-length": 10000,
				},
			}
			policy := CreateRabbitMQPolicy(policyWithDeletingCluster, spec)
			DeferCleanup(th.DeleteInstance, policy)

			// Wait for policy to have finalizer
			Eventually(func(g Gomega) {
				p := GetRabbitMQPolicy(policyWithDeletingCluster)
				g.Expect(p.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should allow deletion without cleanup when cluster is being deleted", func() {
			// Delete policy first
			policy := GetRabbitMQPolicy(policyWithDeletingCluster)
			Expect(th.K8sClient.Delete(th.Ctx, policy)).To(Succeed())

			// Now mark cluster for deletion
			DeleteRabbitMQCluster(deletingClusterName)

			// Policy should be deleted without attempting cleanup
			Eventually(func(g Gomega) {
				p := &rabbitmqv1.RabbitMQPolicy{}
				err := th.K8sClient.Get(th.Ctx, policyWithDeletingCluster, p)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})
})
