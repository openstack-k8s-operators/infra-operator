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

var _ = Describe("RabbitMQUser controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var vhostName types.NamespacedName
	var userName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		vhostName = types.NamespacedName{Name: "test-vhost", Namespace: namespace}
		userName = types.NamespacedName{Name: "test-user", Namespace: namespace}

		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)
		SimulateRabbitMQClusterReady(rabbitmqClusterName)

		vhost := CreateRabbitMQVhost(vhostName, map[string]any{
			"rabbitmqClusterName": rabbitmqClusterName.Name,
			"name":                "test",
		})
		DeferCleanup(th.DeleteInstance, vhost)
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
				g.Expect(user.Status.Conditions.Has(rabbitmqv1.UserReadyCondition)).To(BeTrue())
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
		var userWithBadVhost types.NamespacedName

		BeforeEach(func() {
			userWithBadVhost = types.NamespacedName{Name: "bad-vhost-user", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"vhostRef":            "non-existent",
			}
			user := CreateRabbitMQUser(userWithBadVhost, spec)
			DeferCleanup(th.DeleteInstance, user)
		})

		It("should have spec with non-existent vhost reference", func() {
			user := GetRabbitMQUser(userWithBadVhost)
			Expect(user.Spec.VhostRef).To(Equal("non-existent"))
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
				g.Expect(user.Finalizers).To(ContainElement(rabbitmqv1.UserFinalizer))
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
				g.Expect(u.Finalizers).To(ContainElement(rabbitmqv1.UserFinalizer))
			}, "2s", interval).Should(Succeed())
		})

		It("should allow deletion after TransportURL is deleted", func() {
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
})
