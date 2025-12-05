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
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RabbitMQVhost controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var vhostName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		vhostName = types.NamespacedName{Name: "test-vhost", Namespace: namespace}

		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)
		SimulateRabbitMQClusterReady(rabbitmqClusterName)
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
		})

		It("should block deletion while user references it", func() {
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, vhost)).To(Succeed())

			// Try to delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Vhost should still exist (deletion blocked by user reference)
			Consistently(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				g.Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, v)).To(Succeed())
				g.Expect(v.DeletionTimestamp).NotTo(BeNil())
				g.Expect(v.Finalizers).NotTo(BeEmpty())
			}, "2s", interval).Should(Succeed())
		})

		It("should allow deletion after user is deleted", func() {
			vhost := &rabbitmqv1.RabbitMQVhost{}
			Expect(th.K8sClient.Get(th.Ctx, vhostWithUser, vhost)).To(Succeed())

			// Try to delete vhost
			Expect(th.K8sClient.Delete(th.Ctx, vhost)).To(Succeed())

			// Delete user
			user := &rabbitmqv1.RabbitMQUser{}
			Expect(th.K8sClient.Get(th.Ctx, userRefName, user)).To(Succeed())
			Expect(th.K8sClient.Delete(th.Ctx, user)).To(Succeed())

			// Vhost should eventually be deleted
			Eventually(func(g Gomega) {
				v := &rabbitmqv1.RabbitMQVhost{}
				err := th.K8sClient.Get(th.Ctx, vhostWithUser, v)
				g.Expect(err).To(HaveOccurred())
			}, timeout, interval).Should(Succeed())
		})
	})
})
