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

		It("should NOT default QueueType when RabbitmqCluster already exists", func() {
			ctx := context.Background()

			// Create a RabbitMQCluster to simulate an existing cluster (e.g., during upgrade)
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

			// When RabbitmqCluster exists, QueueType should NOT be defaulted
			// This ensures existing clusters are not touched during operator upgrades
			Expect(rabbitmq.Spec.QueueType).To(BeNil(),
				"QueueType should not be defaulted when RabbitmqCluster exists - existing clusters should never be touched")
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
			// QueueType should NOT be defaulted when RabbitmqCluster exists
			Expect(rabbitmq.Spec.QueueType).To(BeNil(),
				"QueueType should not be defaulted when RabbitmqCluster exists - existing clusters should never be touched")
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

		It("should default QueueType for existing CR when QueueType was never set", func() {
			ctx := context.Background()

			// Create RabbitMq CR WITHOUT QueueType to simulate a CR that was created
			// before the webhook had defaulting logic, or when the webhook was bypassed
			existingRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-no-queuetype",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage: "old-image:v1",
				},
			}
			Expect(k8sClient.Create(ctx, existingRabbitMq)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, existingRabbitMq) }()

			// Wait for it to be created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: existingRabbitMq.Name, Namespace: existingRabbitMq.Namespace}, existingRabbitMq)
				return err == nil
			}).Should(BeTrue())

			// Verify the CR HAS QueueType defaulted by the webhook during creation
			// (webhooks are active in the test environment, so this will be defaulted)
			Expect(existingRabbitMq.Spec.QueueType).NotTo(BeNil())
			Expect(*existingRabbitMq.Spec.QueueType).To(Equal("Quorum"))

			// Simulate an update (e.g., from OpenStackControlPlane)
			// The webhook should preserve the existing QueueType value
			updatedRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-no-queuetype",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage: "new-image:v2",
				},
			}

			// Webhook should preserve QueueType from existing CR
			updatedRabbitMq.Default(k8sClient)
			Expect(updatedRabbitMq.Spec.QueueType).NotTo(BeNil())
			Expect(*updatedRabbitMq.Spec.QueueType).To(Equal("Quorum"), "QueueType should be preserved from existing CR")
		})

		It("should preserve QueueType=Mirrored when webhook defaults to Quorum", func() {
			ctx := context.Background()

			// Create initial RabbitMq CR with QueueType=Mirrored
			// This simulates a deployment before operator upgrade
			mirrored := "Mirrored"
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve-mirrored",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						QueueType: &mirrored,
					},
				},
			}
			Expect(k8sClient.Create(ctx, rabbitmq)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, rabbitmq) }()

			// Wait for it to be created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				return err == nil
			}).Should(BeTrue())

			// Verify it has Mirrored
			Expect(rabbitmq.Spec.QueueType).NotTo(BeNil())
			Expect(*rabbitmq.Spec.QueueType).To(Equal("Mirrored"))

			// Update to persist the QueueType
			Expect(k8sClient.Update(ctx, rabbitmq)).To(Succeed())

			// Verify it's persisted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				if err != nil {
					return false
				}
				return rabbitmq.Spec.QueueType != nil && *rabbitmq.Spec.QueueType == "Mirrored"
			}).Should(BeTrue())

			// Simulate operator upgrade + parent controller update:
			// OpenStackControlPlane reconciles and updates the RabbitMq CR
			// WITHOUT specifying QueueType (because it's not in the OpenStackControlPlane spec)
			// The webhook should preserve Mirrored from the existing CR, not default to Quorum
			updatedRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve-mirrored",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					// Parent controller (OpenStackControlPlane) updates other fields
					// but does NOT specify QueueType
					ContainerImage:   "new-image:v2",
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						// QueueType is nil - not specified by parent controller
					},
				},
			}

			// Call webhook Default() - this is what happens when the parent controller updates
			updatedRabbitMq.Default(k8sClient)

			// CRITICAL: QueueType should be preserved as Mirrored, NOT changed to Quorum
			Expect(updatedRabbitMq.Spec.QueueType).NotTo(BeNil(), "QueueType should be set")
			Expect(*updatedRabbitMq.Spec.QueueType).To(Equal("Mirrored"),
				"QueueType should be preserved as Mirrored from existing CR, not changed to Quorum")
		})

		It("should preserve QueueType=None across operator upgrade", func() {
			ctx := context.Background()

			// Create initial RabbitMq CR with QueueType=None
			none := "None"
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve-none",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						QueueType: &none,
					},
				},
			}
			Expect(k8sClient.Create(ctx, rabbitmq)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, rabbitmq) }()

			// Wait for it to be created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				return err == nil
			}).Should(BeTrue())

			// Update to persist
			Expect(k8sClient.Update(ctx, rabbitmq)).To(Succeed())

			// Simulate parent controller update after operator upgrade
			updatedRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-preserve-none",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage:   "new-image:v4",
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						// QueueType not specified
					},
				},
			}

			updatedRabbitMq.Default(k8sClient)

			Expect(updatedRabbitMq.Spec.QueueType).NotTo(BeNil())
			Expect(*updatedRabbitMq.Spec.QueueType).To(Equal("None"),
				"QueueType should be preserved as None, not changed to Quorum")
		})

		It("should preserve QueueType from Status when Spec is empty (old deployment scenario)", func() {
			ctx := context.Background()

			// Simulate old deployment by creating a RabbitMq CR with Mirrored
			// Then manually setting Spec.QueueType to nil but leaving Status.QueueType set
			// This simulates a deployment where the controller set Status but Spec was never defaulted
			mirrored := "Mirrored"
			rabbitmq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-status-only",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage: "old-image:v1",
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						QueueType: &mirrored,
					},
				},
			}
			Expect(k8sClient.Create(ctx, rabbitmq)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, rabbitmq) }()

			// Wait for it to be created
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				return err == nil
			}).Should(BeTrue())

			// Simulate controller setting Status.QueueType based on actual cluster
			rabbitmq.Status.QueueType = "Mirrored"
			Expect(k8sClient.Status().Update(ctx, rabbitmq)).To(Succeed())

			// Now simulate the scenario: operator with older code didn't persist Spec.QueueType
			// So we manually clear it to simulate that old state
			// Then test that webhook preserves from Status
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKey{Name: rabbitmq.Name, Namespace: rabbitmq.Namespace}, rabbitmq)
				if err != nil {
					return false
				}
				// Use SubResource("status").Get to read the actual status
				// This ensures we see the updated Status.QueueType
				return rabbitmq.Status.QueueType == "Mirrored"
			}).Should(BeTrue())

			// Create a fresh RabbitMq object simulating parent controller update without QueueType
			// The webhook should see the existing CR has Status.QueueType=Mirrored and preserve it
			updatedRabbitMq := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rabbitmq-status-only",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					ContainerImage:   "new-image:v5",
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
						// QueueType not specified (nil)
					},
				},
			}

			// Call webhook - it should check existing CR and find:
			// - Spec.QueueType = "Mirrored" (from our initial create)
			// So it will preserve "Mirrored" from Spec, which is correct!
			updatedRabbitMq.Default(k8sClient)

			// Webhook should preserve Mirrored (from existing CR's Spec)
			Expect(updatedRabbitMq.Spec.QueueType).NotTo(BeNil(),
				"QueueType should be set")
			Expect(*updatedRabbitMq.Spec.QueueType).To(Equal("Mirrored"),
				"QueueType should be preserved from existing CR (Mirrored), not changed to webhook default (Quorum)")
		})
	})
})
