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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("RabbitMQPolicy webhook", func() {
	Context("Default method", func() {
		It("should default Name to CR name when not specified", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			policy.Default(k8sClient)

			Expect(policy.Spec.Name).To(Equal("test-policy"))
		})

		It("should not override explicitly set Name", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "custom-policy-name",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			policy.Default(k8sClient)

			Expect(policy.Spec.Name).To(Equal("custom-policy-name"))
		})
	})

	Context("ValidateCreate method", func() {
		It("should accept valid policy names", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "valid-policy_name.123:test",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			_, err := policy.ValidateCreate(k8sClient)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject policy names with invalid characters", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "invalid@policy",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			_, err := policy.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid character"))
		})

		It("should reject empty policy names", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			_, err := policy.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be empty"))
		})

		It("should reject policy names with spaces", func() {
			policy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "invalid policy",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			_, err := policy.ValidateCreate(k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid character"))
		})
	})

	Context("ValidateUpdate method", func() {
		It("should reject updates that change the policy name", func() {
			oldPolicy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "original-name",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			newPolicy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "changed-name",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			_, err := newPolicy.ValidateUpdate(k8sClient, oldPolicy)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("policy name cannot be changed"))
		})

		It("should allow updates that do not change the policy name", func() {
			oldPolicy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "my-policy",
					Pattern:             ".*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":10000}`)},
				},
			}

			newPolicy := &rabbitmqv1beta1.RabbitMQPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
					RabbitmqClusterName: "test-cluster",
					Name:                "my-policy",
					Pattern:             "^queue.*",
					Definition:          apiextensionsv1.JSON{Raw: []byte(`{"max-length":1000}`)},
					Priority:            10,
				},
			}

			_, err := newPolicy.ValidateUpdate(k8sClient, oldPolicy)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
