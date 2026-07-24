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
	instancehav1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("InstanceHa webhook", func() {
	Context("Default method", func() {
		It("should set all defaults on an empty spec", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instanceha-defaults",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{},
			}

			instanceha.Default()

			Expect(instanceha.Spec.ContainerImage).To(BeEmpty())
			Expect(instanceha.Spec.OpenStackCloud).To(Equal(instancehav1beta1.OpenStackCloud))
			Expect(instanceha.Spec.OpenStackConfigMap).To(Equal("openstack-config"))
			Expect(instanceha.Spec.OpenStackConfigSecret).To(Equal("openstack-config-secret"))
			Expect(instanceha.Spec.FencingSecret).To(Equal("fencing-secret"))
			Expect(instanceha.Spec.InstanceHaConfigMap).To(Equal("instanceha-config"))
			Expect(instanceha.Spec.InstanceHaKdumpPort).To(Equal(int32(7410)))
			Expect(instanceha.Spec.InstanceHaHeartbeatPort).To(Equal(int32(7411)))
			Expect(instanceha.Spec.MetricsTLS.MinTLSVersion).To(Equal("1.2"))
			Expect(instanceha.Spec.MetricsTLS.CipherSuites).To(Equal("HIGH:!aNULL:!MD5:!RC4:!3DES:!kRSA"))
		})

		It("should not override an explicitly set containerImage", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instanceha-explicit-image",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					ContainerImage: "my-custom-image:v1",
				},
			}

			instanceha.Default()

			Expect(instanceha.Spec.ContainerImage).To(Equal("my-custom-image:v1"))
		})

		It("should not override explicitly set values", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-instanceha-explicit",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud:          "my-cloud",
					OpenStackConfigMap:      "my-config",
					OpenStackConfigSecret:   "my-secret",
					FencingSecret:           "my-fencing",
					InstanceHaConfigMap:     "my-instanceha-config",
					InstanceHaKdumpPort:     8000,
					InstanceHaHeartbeatPort: 9000,
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						MinTLSVersion: "1.3",
						CipherSuites:  "HIGH",
					},
				},
			}

			instanceha.Default()

			Expect(instanceha.Spec.OpenStackCloud).To(Equal("my-cloud"))
			Expect(instanceha.Spec.OpenStackConfigMap).To(Equal("my-config"))
			Expect(instanceha.Spec.OpenStackConfigSecret).To(Equal("my-secret"))
			Expect(instanceha.Spec.FencingSecret).To(Equal("my-fencing"))
			Expect(instanceha.Spec.InstanceHaConfigMap).To(Equal("my-instanceha-config"))
			Expect(instanceha.Spec.InstanceHaKdumpPort).To(Equal(int32(8000)))
			Expect(instanceha.Spec.InstanceHaHeartbeatPort).To(Equal(int32(9000)))
			Expect(instanceha.Spec.MetricsTLS.MinTLSVersion).To(Equal("1.3"))
			Expect(instanceha.Spec.MetricsTLS.CipherSuites).To(Equal("HIGH"))
		})
	})

	Context("ValidateCreate", func() {
		It("should accept a valid InstanceHa", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-valid",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "default",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:!aNULL:!MD5",
					},
				},
			}

			warnings, err := instanceha.ValidateCreate(ctx, k8sClient)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject NULL cipher suite without ! prefix", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-null-cipher",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "default",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:NULL",
					},
				},
			}

			_, err := instanceha.ValidateCreate(ctx, k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("NULL"))
		})

		It("should reject ALL cipher suite", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-all-cipher",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "default",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "ALL",
					},
				},
			}

			_, err := instanceha.ValidateCreate(ctx, k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ALL"))
		})

		It("should reject @SECLEVEL=0 cipher suite", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-seclevel0",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "default",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:@SECLEVEL=0",
					},
				},
			}

			_, err := instanceha.ValidateCreate(ctx, k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("SECLEVEL=0"))
		})

		It("should accept cipher suite with !NULL (negated)", func() {
			instanceha := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-negated-null",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "default",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:!NULL:!aNULL",
					},
				},
			}

			warnings, err := instanceha.ValidateCreate(ctx, k8sClient)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject duplicate OpenStackCloud in the same namespace", func() {
			existing := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-existing",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					ContainerImage: "test:latest",
					OpenStackCloud: "shared-cloud",
					Disabled:       "False",
				},
			}
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())
			DeferCleanup(func() {
				Expect(k8sClient.Delete(ctx, existing)).To(Succeed())
			})

			duplicate := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-duplicate",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "shared-cloud",
				},
			}

			_, err := duplicate.ValidateCreate(ctx, k8sClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already manages OpenStackCloud"))
		})
	})

	Context("ValidateUpdate", func() {
		It("should reject changing openStackCloud", func() {
			oldInstance := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-immutable",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "original-cloud",
				},
			}

			newInstance := oldInstance.DeepCopy()
			newInstance.Spec.OpenStackCloud = "different-cloud"

			_, err := newInstance.ValidateUpdate(ctx, k8sClient, oldInstance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be changed after creation"))
		})

		It("should accept update when openStackCloud is unchanged", func() {
			oldInstance := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-valid",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "my-cloud",
					Disabled:       "False",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:!aNULL",
					},
				},
			}

			newInstance := oldInstance.DeepCopy()
			newInstance.Spec.Disabled = "True"

			warnings, err := newInstance.ValidateUpdate(ctx, k8sClient, oldInstance)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})

		It("should reject unsafe cipher suites on update", func() {
			oldInstance := &instancehav1beta1.InstanceHa{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-cipher",
					Namespace: "default",
				},
				Spec: instancehav1beta1.InstanceHaSpec{
					OpenStackCloud: "my-cloud",
					MetricsTLS: instancehav1beta1.InstanceHaMetricsTLS{
						CipherSuites: "HIGH:!aNULL",
					},
				},
			}

			newInstance := oldInstance.DeepCopy()
			newInstance.Spec.MetricsTLS.CipherSuites = "ALL"

			_, err := newInstance.ValidateUpdate(ctx, k8sClient, oldInstance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ALL"))
		})
	})
})
