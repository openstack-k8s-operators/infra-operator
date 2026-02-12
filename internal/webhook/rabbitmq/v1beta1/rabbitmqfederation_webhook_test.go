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

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("RabbitMQFederation webhook", func() {
	BeforeEach(func() {
		// Create test clusters for validation
		testCluster := &rabbitmqclusterv2.RabbitmqCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "default",
			},
			Spec: rabbitmqclusterv2.RabbitmqClusterSpec{},
		}
		Expect(k8sClient.Create(ctx, testCluster)).To(Succeed())

		upstreamCluster := &rabbitmqclusterv2.RabbitmqCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "upstream-cluster",
				Namespace: "default",
			},
			Spec: rabbitmqclusterv2.RabbitmqClusterSpec{},
		}
		Expect(k8sClient.Create(ctx, upstreamCluster)).To(Succeed())
	})

	AfterEach(func() {
		// Clean up test clusters
		testCluster := &rabbitmqclusterv2.RabbitmqCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cluster",
				Namespace: "default",
			},
		}
		_ = k8sClient.Delete(ctx, testCluster)

		upstreamCluster := &rabbitmqclusterv2.RabbitmqCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "upstream-cluster",
				Namespace: "default",
			},
		}
		_ = k8sClient.Delete(ctx, upstreamCluster)
	})
	Context("Default method", func() {
		It("should set default AckMode", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.AckMode).To(Equal("on-confirm"))
		})

		It("should set default Expires", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.Expires).To(Equal(int32(1800000)))
		})

		It("should set default MaxHops", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.MaxHops).To(Equal(int32(1)))
		})

		It("should set default PrefetchCount", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.PrefetchCount).To(Equal(int32(1000)))
		})

		It("should set default ReconnectDelay", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.ReconnectDelay).To(Equal(int32(5)))
		})

		It("should set default PolicyPattern", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.PolicyPattern).To(Equal(".*"))
		})

		It("should not override explicitly set values", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					AckMode:             "on-publish",
					Expires:             3600000,
					MaxHops:             2,
					PrefetchCount:       500,
					ReconnectDelay:      10,
					PolicyPattern:       "^federated.*",
				},
			}

			fed.Default(k8sClient)

			Expect(fed.Spec.AckMode).To(Equal("on-publish"))
			Expect(fed.Spec.Expires).To(Equal(int32(3600000)))
			Expect(fed.Spec.MaxHops).To(Equal(int32(2)))
			Expect(fed.Spec.PrefetchCount).To(Equal(int32(500)))
			Expect(fed.Spec.ReconnectDelay).To(Equal(int32(10)))
			Expect(fed.Spec.PolicyPattern).To(Equal("^federated.*"))
		})
	})

	Context("ValidateCreate method", func() {
		It("should accept valid UpstreamName", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "valid-upstream_name.123:test",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			_, err := fed.ValidateCreate(k8sClient)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject UpstreamName with invalid characters", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "invalid@upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			_, err := fed.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid character"))
		})

		It("should reject when neither UpstreamClusterName nor UpstreamSecretRef is specified", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
				},
			}

			_, err := fed.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must specify either upstreamClusterName or upstreamSecretRef"))
		})

		It("should reject when both UpstreamClusterName and UpstreamSecretRef are specified", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					UpstreamSecretRef:   &corev1.LocalObjectReference{Name: "test-secret"},
				},
			}

			_, err := fed.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})

		It("should reject invalid PolicyPattern regex", func() {
			fed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					PolicyPattern:       "[invalid(regex",
				},
			}

			_, err := fed.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid regex pattern"))
		})
	})

	Context("ValidateUpdate method", func() {
		It("should reject updates that change UpstreamName", func() {
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "original-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "changed-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("upstreamName cannot be changed"))
		})

		It("should reject updates that change RabbitmqClusterName", func() {
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "original-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "changed-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("rabbitmqClusterName cannot be changed"))
		})

		It("should reject changing from UpstreamClusterName to UpstreamSecretRef", func() {
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamSecretRef:   &corev1.LocalObjectReference{Name: "test-secret"},
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot change between upstreamClusterName and upstreamSecretRef modes"))
		})

		It("should reject changing from UpstreamSecretRef to UpstreamClusterName", func() {
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamSecretRef:   &corev1.LocalObjectReference{Name: "test-secret"},
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot change between upstreamClusterName and upstreamSecretRef modes"))
		})

		It("should allow updates that do not change immutable fields", func() {
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					AckMode:             "on-confirm",
					Priority:            0,
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-federation",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					AckMode:             "on-publish", // Changed - allowed
					Priority:            100,          // Changed - allowed
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject changing VhostRef during deletion", func() {
			now := metav1.Now()
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-federation",
					Namespace:         "default",
					DeletionTimestamp: &now,
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					VhostRef:            "old-vhost",
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-federation",
					Namespace:         "default",
					DeletionTimestamp: &now,
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "upstream-cluster",
					VhostRef:            "new-vhost",
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vhostRef cannot be changed while resource is being deleted"))
		})

		It("should reject changing UpstreamClusterName during deletion", func() {
			now := metav1.Now()
			oldFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-federation",
					Namespace:         "default",
					DeletionTimestamp: &now,
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "old-upstream",
				},
			}

			newFed := &rabbitmqv1beta1.RabbitMQFederation{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-federation",
					Namespace:         "default",
					DeletionTimestamp: &now,
				},
				Spec: rabbitmqv1beta1.RabbitMQFederationSpec{
					RabbitmqClusterName: "test-cluster",
					UpstreamName:        "test-upstream",
					UpstreamClusterName: "new-upstream",
				},
			}

			_, err := newFed.ValidateUpdate(k8sClient, oldFed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("upstreamClusterName cannot be changed while resource is being deleted"))
		})
	})
})
