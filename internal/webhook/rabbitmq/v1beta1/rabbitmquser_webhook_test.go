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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("RabbitMQUser webhook", func() {
	Context("Default method", func() {
		It("should default Username to CR name when not specified", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
				},
			}

			user.Default(k8sClient)

			Expect(user.Spec.Username).To(Equal("test-user"))
		})

		It("should not override explicitly set Username", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "custom-username",
				},
			}

			user.Default(k8sClient)

			Expect(user.Spec.Username).To(Equal("custom-username"))
		})
	})

	Context("ValidateCreate method", func() {
		It("should accept valid usernames", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "valid-user_name.123:test",
				},
			}

			_, err := user.ValidateCreate(k8sClient)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject usernames with invalid characters", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "invalid@user",
				},
			}

			_, err := user.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid character"))
		})

		It("should reject empty usernames", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "",
				},
			}

			_, err := user.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be empty"))
		})

		It("should reject usernames with spaces", func() {
			user := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "invalid user",
				},
			}

			_, err := user.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid character"))
		})
	})

	Context("ValidateUpdate method", func() {
		It("should reject updates that change the username", func() {
			oldUser := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "original-username",
				},
			}

			newUser := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "changed-username",
				},
			}

			_, err := newUser.ValidateUpdate(k8sClient, oldUser)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("username cannot be changed"))
		})

		It("should allow updates that do not change the username", func() {
			oldUser := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "my-username",
					Tags:                []string{"administrator"},
				},
			}

			newUser := &rabbitmqv1beta1.RabbitMQUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-user",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQUserSpec{
					RabbitmqClusterName: "test-cluster",
					Username:            "my-username",
					Tags:                []string{"management"},
				},
			}

			_, err := newUser.ValidateUpdate(k8sClient, oldUser)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
