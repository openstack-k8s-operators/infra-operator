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
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("RabbitMQFederation controller", func() {
	var rabbitmqClusterName types.NamespacedName
	var upstreamClusterName types.NamespacedName
	var vhostName types.NamespacedName
	var federationName types.NamespacedName

	BeforeEach(func() {
		rabbitmqClusterName = types.NamespacedName{Name: "rabbitmq", Namespace: namespace}
		upstreamClusterName = types.NamespacedName{Name: "rabbitmq-upstream", Namespace: namespace}
		vhostName = types.NamespacedName{Name: "test-vhost", Namespace: namespace}
		federationName = types.NamespacedName{Name: "test-federation", Namespace: namespace}

		// Create local cluster
		CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
		SimulateRabbitMQClusterReady(rabbitmqClusterName)
		DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

		// Create upstream cluster
		CreateRabbitMQCluster(upstreamClusterName, GetDefaultRabbitMQClusterSpec(false))
		SimulateRabbitMQClusterReady(upstreamClusterName)
		DeferCleanup(DeleteRabbitMQCluster, upstreamClusterName)

		// Create vhost on local cluster
		CreateRabbitMQVhost(vhostName, map[string]any{
			"rabbitmqClusterName": rabbitmqClusterName.Name,
			"name":                "test",
		})
		// Note: No DeferCleanup - namespace cleanup handles vhost deletion
	})

	// Mark clusters for deletion before cleanup phase to trigger skip-cleanup logic
	AfterEach(func() {
		for _, clusterName := range []types.NamespacedName{rabbitmqClusterName, upstreamClusterName} {
			cluster := &rabbitmqclusterv2.RabbitmqCluster{}
			err := th.K8sClient.Get(th.Ctx, clusterName, cluster)
			if err == nil && cluster.DeletionTimestamp.IsZero() {
				_ = th.K8sClient.Delete(th.Ctx, cluster)
			}
		}
	})

	When("a RabbitMQFederation with UpstreamClusterName is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
				"upstreamClusterName": upstreamClusterName.Name,
				"vhostRef":            vhostName.Name,
			}
			CreateRabbitMQFederation(federationName, spec)
			// Note: No DeferCleanup - namespace cleanup handles federation deletion
		})

		It("should have spec fields set correctly", func() {
			fed := GetRabbitMQFederation(federationName)
			Expect(fed.Spec.RabbitmqClusterName).To(Equal(rabbitmqClusterName.Name))
			Expect(fed.Spec.UpstreamName).To(Equal("test-upstream"))
			Expect(fed.Spec.UpstreamClusterName).To(Equal(upstreamClusterName.Name))
			Expect(fed.Spec.VhostRef).To(Equal(vhostName.Name))
		})

		It("should have default values set", func() {
			fed := GetRabbitMQFederation(federationName)
			Expect(fed.Spec.AckMode).To(Equal("on-confirm"))
			Expect(fed.Spec.Expires).To(Equal(int32(1800000)))
			Expect(fed.Spec.MaxHops).To(Equal(int32(1)))
			Expect(fed.Spec.PrefetchCount).To(Equal(int32(1000)))
			Expect(fed.Spec.ReconnectDelay).To(Equal(int32(5)))
			Expect(fed.Spec.PolicyPattern).To(Equal(".*"))
		})
	})

	When("a RabbitMQFederation with UpstreamSecretRef is created", func() {
		var secretName types.NamespacedName

		BeforeEach(func() {
			secretName = types.NamespacedName{Name: "upstream-uri-secret", Namespace: namespace}

			// Create secret with upstream URI
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName.Name,
					Namespace: secretName.Namespace,
				},
				Data: map[string][]byte{
					"uri": []byte("amqp://user:pass@remote-host:5672/%2F"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, secret)).Should(Succeed())
			// Note: No DeferCleanup for secret - namespace cleanup handles it

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "remote-upstream",
				"upstreamSecretRef": map[string]any{
					"name": secretName.Name,
				},
				"vhostRef": vhostName.Name,
			}
			CreateRabbitMQFederation(federationName, spec)
			// Note: No DeferCleanup - namespace cleanup handles federation deletion
		})

		It("should have spec fields set correctly", func() {
			fed := GetRabbitMQFederation(federationName)
			Expect(fed.Spec.UpstreamSecretRef).NotTo(BeNil())
			Expect(fed.Spec.UpstreamSecretRef.Name).To(Equal(secretName.Name))
			Expect(fed.Spec.UpstreamClusterName).To(BeEmpty())
		})
	})

	When("a RabbitMQFederation with custom federation parameters is created", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "custom-upstream",
				"upstreamClusterName": upstreamClusterName.Name,
				"vhostRef":            vhostName.Name,
				"ackMode":             "on-publish",
				"expires":             3600000,
				"maxHops":             2,
				"prefetchCount":       500,
				"reconnectDelay":      10,
				"priority":            100,
				"policyPattern":       "^federated.*",
			}
			CreateRabbitMQFederation(federationName, spec)
			// Note: No DeferCleanup - namespace cleanup handles federation deletion
		})

		It("should have custom parameters set", func() {
			fed := GetRabbitMQFederation(federationName)
			Expect(fed.Spec.AckMode).To(Equal("on-publish"))
			Expect(fed.Spec.Expires).To(Equal(int32(3600000)))
			Expect(fed.Spec.MaxHops).To(Equal(int32(2)))
			Expect(fed.Spec.PrefetchCount).To(Equal(int32(500)))
			Expect(fed.Spec.ReconnectDelay).To(Equal(int32(10)))
			Expect(fed.Spec.Priority).To(Equal(int32(100)))
			Expect(fed.Spec.PolicyPattern).To(Equal("^federated.*"))
		})
	})

	When("a RabbitMQFederation without VhostRef is created (default vhost)", func() {
		BeforeEach(func() {
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "default-vhost-upstream",
				"upstreamClusterName": upstreamClusterName.Name,
			}
			CreateRabbitMQFederation(federationName, spec)
			// Note: No DeferCleanup - namespace cleanup handles federation deletion
		})

		It("should not have VhostRef set", func() {
			fed := GetRabbitMQFederation(federationName)
			Expect(fed.Spec.VhostRef).To(BeEmpty())
		})
	})

	When("a RabbitMQFederation references a non-existent vhost", func() {
		It("should reject creation with validation error", func() {
			badFedName := types.NamespacedName{Name: "bad-vhost-federation", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
				"upstreamClusterName": upstreamClusterName.Name,
				"vhostRef":            "non-existent",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQFederation",
				"metadata": map[string]any{
					"name":      badFedName.Name,
					"namespace": badFedName.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("referenced vhost does not exist"))
		})
	})

	When("a RabbitMQFederation references a non-existent upstream cluster", func() {
		It("should reject creation with validation error", func() {
			badFedName := types.NamespacedName{Name: "bad-cluster-federation", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
				"upstreamClusterName": "non-existent",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQFederation",
				"metadata": map[string]any{
					"name":      badFedName.Name,
					"namespace": badFedName.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("referenced RabbitMQ cluster does not exist"))
		})
	})

	When("a RabbitMQFederation references a non-existent secret", func() {
		It("should reject creation with validation error", func() {
			badFedName := types.NamespacedName{Name: "bad-secret-federation", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
				"upstreamSecretRef": map[string]any{
					"name": "non-existent",
				},
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQFederation",
				"metadata": map[string]any{
					"name":      badFedName.Name,
					"namespace": badFedName.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("referenced secret does not exist"))
		})
	})

	When("a RabbitMQFederation is created with neither UpstreamClusterName nor UpstreamSecretRef", func() {
		It("should reject creation with validation error", func() {
			badFedName := types.NamespacedName{Name: "no-upstream-federation", Namespace: namespace}
			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQFederation",
				"metadata": map[string]any{
					"name":      badFedName.Name,
					"namespace": badFedName.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must specify either upstreamClusterName or upstreamSecretRef"))
		})
	})

	When("a RabbitMQFederation is created with both UpstreamClusterName and UpstreamSecretRef", func() {
		It("should reject creation with validation error", func() {
			badFedName := types.NamespacedName{Name: "both-upstream-federation", Namespace: namespace}

			// Create a secret first
			secretName := types.NamespacedName{Name: "test-secret", Namespace: namespace}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName.Name,
					Namespace: secretName.Namespace,
				},
				Data: map[string][]byte{
					"uri": []byte("amqp://user:pass@host:5672/%2F"),
				},
			}
			Expect(th.K8sClient.Create(th.Ctx, secret)).Should(Succeed())
			DeferCleanup(th.K8sClient.Delete, th.Ctx, secret)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
				"upstreamName":        "test-upstream",
				"upstreamClusterName": upstreamClusterName.Name,
				"upstreamSecretRef": map[string]any{
					"name": secretName.Name,
				},
			}
			raw := map[string]any{
				"apiVersion": "rabbitmq.openstack.org/v1beta1",
				"kind":       "RabbitMQFederation",
				"metadata": map[string]any{
					"name":      badFedName.Name,
					"namespace": badFedName.Namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			err := th.K8sClient.Create(th.Ctx, unstructuredObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})
	})

})
