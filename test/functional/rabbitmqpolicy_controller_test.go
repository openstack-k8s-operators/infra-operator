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
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RabbitMQPolicy controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var policyName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		policyName = types.NamespacedName{Name: "test-policy", Namespace: namespace}

		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)
		SimulateRabbitMQClusterReady(rabbitmqClusterName)
	})

	When("a RabbitMQPolicy is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"pattern":             ".*",
				"definition": map[string]interface{}{
					"ha-mode": "all",
				},
			}
			policy := CreateRabbitMQPolicy(policyName, spec)
			DeferCleanup(th.DeleteInstance, policy)
		})

		It("should have spec fields set", func() {
			policy := GetRabbitMQPolicy(policyName)
			Expect(policy.Spec.RabbitmqClusterName).To(Equal(rabbitmqClusterName.Name))
			Expect(policy.Spec.Pattern).To(Equal(".*"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("ha-mode"))
			Expect(string(policy.Spec.Definition.Raw)).To(ContainSubstring("all"))
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
					"ha-mode": "all",
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
})
