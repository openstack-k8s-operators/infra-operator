/*
Copyright 2023.

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

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

var _ = Describe("RabbitMQ Webhook", func() {
	Context("When creating RabbitMQ resources", func() {
		It("Sets queueType to Quorum for new RabbitMQ with empty queueType", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-empty",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](3),
						},
						QueueType: "", // Empty queueType
					},
				},
			}

			// Apply webhook defaulting
			rabbitmq.Default()

			Expect(rabbitmq.Spec.QueueType).Should(Equal("Quorum"),
				"Empty queueType should default to Quorum")
		})

		It("Sets queueType to Quorum for new RabbitMQ with CRD default 'Mirrored'", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-mirrored",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](3),
						},
						QueueType: "Mirrored", // CRD default value
					},
				},
			}

			// Apply webhook defaulting
			rabbitmq.Default()

			Expect(rabbitmq.Spec.QueueType).Should(Equal("Quorum"),
				"CRD default 'Mirrored' queueType should be converted to Quorum")
		})

		It("Preserves explicitly set queueType 'None'", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-none",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](3),
						},
						QueueType: "None", // Explicit non-default value
					},
				},
			}

			// Apply webhook defaulting
			rabbitmq.Default()

			Expect(rabbitmq.Spec.QueueType).Should(Equal("None"),
				"Explicitly set queueType 'None' should be preserved")
		})

		It("Preserves explicitly set queueType 'Quorum'", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-quorum",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](3),
						},
						QueueType: "Quorum", // Explicit desired value
					},
				},
			}

			// Apply webhook defaulting
			rabbitmq.Default()

			Expect(rabbitmq.Spec.QueueType).Should(Equal("Quorum"),
				"Explicitly set queueType 'Quorum' should be preserved")
		})
	})

	Context("RabbitMqSpecCore Default method", func() {
		It("Handles empty queueType correctly", func() {
			spec := RabbitMqSpecCore{
				RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
					Replicas: ptr.To[int32](1),
				},
				QueueType: "",
			}

			spec.Default()

			Expect(spec.QueueType).Should(Equal("Quorum"))
		})

		It("Handles CRD default 'Mirrored' correctly", func() {
			spec := RabbitMqSpecCore{
				RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
					Replicas: ptr.To[int32](1),
				},
				QueueType: "Mirrored",
			}

			spec.Default()

			Expect(spec.QueueType).Should(Equal("Quorum"))
		})

		It("Preserves non-default values", func() {
			testCases := []struct {
				input    string
				expected string
				desc     string
			}{
				{"None", "None", "None value should be preserved"},
				{"Quorum", "Quorum", "Quorum value should be preserved"},
			}

			for _, tc := range testCases {
				spec := RabbitMqSpecCore{
					RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
						Replicas: ptr.To[int32](1),
					},
					QueueType: tc.input,
				}

				spec.Default()

				Expect(spec.QueueType).Should(Equal(tc.expected), tc.desc)
			}
		})
	})

	Context("Integration with webhook pipeline", func() {
		It("Works correctly when creating via Kubernetes API", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-integration",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](1),
						},
						// No queueType specified - simulates new resource creation
					},
				},
			}

			err := k8sClient.Create(ctx, rabbitmq)
			Expect(err).NotTo(HaveOccurred())

			// Fetch the created resource to verify webhook was applied
			createdRabbitMQ := &RabbitMq{}
			err = k8sClient.Get(ctx,
				types.NamespacedName{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace},
				createdRabbitMQ)
			Expect(err).NotTo(HaveOccurred())

			Expect(createdRabbitMQ.Spec.QueueType).Should(Equal("Quorum"),
				"Webhook should set queueType to Quorum for new resources")

			// Clean up
			err = k8sClient.Delete(ctx, createdRabbitMQ)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Preserves explicit values when updating via Kubernetes API", func() {
			rabbitmq := &RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update",
					Namespace: "default",
				},
				Spec: RabbitMqSpec{
					ContainerImage: "quay.io/test/rabbitmq:latest",
					RabbitMqSpecCore: RabbitMqSpecCore{
						RabbitmqClusterSpecCore: rabbitmqv2.RabbitmqClusterSpecCore{
							Replicas: ptr.To[int32](1),
						},
						QueueType: "None", // Explicit value
					},
				},
			}

			err := k8sClient.Create(ctx, rabbitmq)
			Expect(err).NotTo(HaveOccurred())

			// Fetch and verify the explicit value is preserved
			createdRabbitMQ := &RabbitMq{}
			err = k8sClient.Get(ctx,
				types.NamespacedName{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace},
				createdRabbitMQ)
			Expect(err).NotTo(HaveOccurred())

			Expect(createdRabbitMQ.Spec.QueueType).Should(Equal("None"),
				"Webhook should preserve explicitly set queueType")

			// Clean up
			err = k8sClient.Delete(ctx, createdRabbitMQ)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
