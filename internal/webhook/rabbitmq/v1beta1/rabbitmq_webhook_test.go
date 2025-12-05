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
	"context"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("RabbitMq webhook", func() {
	Context("Default method", func() {
		It("should set QueueType to Quorum for new clusters", func() {
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-new",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{},
			}

			rabbitmq.Default(k8sClient)

			Expect(rabbitmq.Spec.QueueType).NotTo(BeNil())
			Expect(*rabbitmq.Spec.QueueType).To(Equal("Quorum"))
		})

		It("should not set QueueType for existing clusters", func() {
			ctx := context.Background()

			// Create a RabbitMQCluster to simulate an existing cluster
			cluster := &rabbitmqv2.RabbitmqCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-existing",
					Namespace: "default",
				},
				Spec: rabbitmqv2.RabbitmqClusterSpec{
					Replicas: ptr.To(int32(1)),
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cluster) }()

			// Wait for the cluster to have a CreationTimestamp
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}, cluster)
				return err == nil && !cluster.CreationTimestamp.IsZero()
			}).Should(BeTrue())

			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-existing",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{},
			}

			rabbitmq.Default(k8sClient)

			Expect(rabbitmq.Spec.QueueType).To(BeNil())
		})

		It("should not override explicitly set QueueType for new clusters", func() {
			mirrored := "Mirrored"
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-custom",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						QueueType: &mirrored,
					},
				},
			}

			rabbitmq.Default(k8sClient)

			Expect(rabbitmq.Spec.QueueType).NotTo(BeNil())
			Expect(*rabbitmq.Spec.QueueType).To(Equal("Mirrored"))
		})

		It("should set ContainerImage default for existing clusters when not set", func() {
			ctx := context.Background()
			rabbitmqv1beta1.SetupRabbitMqDefaults(rabbitmqv1beta1.RabbitMqDefaults{
				ContainerImageURL: "test-image:latest",
			})

			// Create a RabbitMQCluster to simulate an existing cluster
			cluster := &rabbitmqv2.RabbitmqCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-image",
					Namespace: "default",
				},
				Spec: rabbitmqv2.RabbitmqClusterSpec{
					Replicas: ptr.To(int32(1)),
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cluster) }()

			// Wait for the cluster to have a CreationTimestamp
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}, cluster)
				return err == nil && !cluster.CreationTimestamp.IsZero()
			}).Should(BeTrue())

			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-image",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{},
			}

			rabbitmq.Default(k8sClient)

			Expect(rabbitmq.Spec.ContainerImage).To(Equal("test-image:latest"))
			Expect(rabbitmq.Spec.QueueType).To(BeNil())
		})

		It("should not override existing ContainerImage for existing clusters", func() {
			ctx := context.Background()
			rabbitmqv1beta1.SetupRabbitMqDefaults(rabbitmqv1beta1.RabbitMqDefaults{
				ContainerImageURL: "test-image:latest",
			})

			// Create a RabbitMQCluster to simulate an existing cluster
			cluster := &rabbitmqv2.RabbitmqCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-custom-image",
					Namespace: "default",
				},
				Spec: rabbitmqv2.RabbitmqClusterSpec{
					Replicas: ptr.To(int32(1)),
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, cluster) }()

			// Wait for the cluster to have a CreationTimestamp
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: cluster.Name, Namespace: cluster.Namespace}, cluster)
				return err == nil && !cluster.CreationTimestamp.IsZero()
			}).Should(BeTrue())

			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-custom-image",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage: "custom-image:v1",
				},
			}

			rabbitmq.Default(k8sClient)

			Expect(rabbitmq.Spec.ContainerImage).To(Equal("custom-image:v1"))
		})

		It("should preserve QueueType across updates from parent controller", func() {
			ctx := context.Background()

			// Create initial RabbitMq CR
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{},
			}
			Expect(k8sClient.Create(ctx, rabbitmq)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, rabbitmq) }()

			// Wait for it to be created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				return err == nil
			}).Should(BeTrue())

			// First webhook call - should set QueueType to Quorum
			rabbitmq.Default(k8sClient)
			Expect(rabbitmq.Spec.QueueType).NotTo(BeNil())
			Expect(*rabbitmq.Spec.QueueType).To(Equal("Quorum"))

			// Update the CR to persist QueueType
			Expect(k8sClient.Update(ctx, rabbitmq)).To(Succeed())

			// Simulate parent controller updating the CR without QueueType (like OpenStackControlPlane does)
			freshRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					// Parent controller updates spec without QueueType
					ContainerImage: "new-image:latest",
				},
			}

			// Webhook should preserve QueueType from existing CR
			freshRabbitMq.Default(k8sClient)
			Expect(freshRabbitMq.Spec.QueueType).NotTo(BeNil())
			Expect(*freshRabbitMq.Spec.QueueType).To(Equal("Quorum"), "QueueType should be preserved from existing CR")
		})
	})
})
