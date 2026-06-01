/*
Copyright 2024.

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
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	//revive:disable-next-line:dot-imports
	instanceha "github.com/openstack-k8s-operators/infra-operator/internal/instanceha"
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("InstanceHa Controller", func() {
	var instanceHaName types.NamespacedName

	When("a default InstanceHa gets created", func() {
		BeforeEach(func() {
			ih := CreateInstanceHaConfig(namespace, GetDefaultInstanceHaSpec())
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)
		})

		It("should have created an InstanceHa", func() {
			Eventually(func(_ Gomega) {
				GetInstanceHa(instanceHaName)
			}, timeout, interval).Should(Succeed())
		})

		It("should be waiting for input resources", func() {
			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.InputReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("should have the finalizer set on the CR", func() {
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				g.Expect(instance.Finalizers).To(ContainElement("openstack.org/instanceha"))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("prerequisite resources exist", func() {
		BeforeEach(func() {
			ih := CreateInstanceHaConfig(namespace, GetDefaultInstanceHaSpec())
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))
		})

		It("should create a metrics Service with correct labels and port", func() {
			metricsServiceName := types.NamespacedName{
				Name:      instanceHaName.Name + "-metrics",
				Namespace: instanceHaName.Namespace,
			}

			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, metricsServiceName, svc)).Should(Succeed())

				g.Expect(svc.Labels).To(HaveKeyWithValue("service", "instanceha"))
				g.Expect(svc.Labels).To(HaveKeyWithValue("metrics", "enabled"))

				g.Expect(svc.Spec.Selector).To(HaveKeyWithValue("service", instanceHaName.Name))

				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].Name).To(Equal("metrics"))
				g.Expect(svc.Spec.Ports[0].Port).To(Equal(int32(8080)))
				g.Expect(svc.Spec.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))
			}, timeout, interval).Should(Succeed())
		})

		It("should have the Service owned by the InstanceHa CR", func() {
			metricsServiceName := types.NamespacedName{
				Name:      instanceHaName.Name + "-metrics",
				Namespace: instanceHaName.Namespace,
			}

			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, metricsServiceName, svc)).Should(Succeed())

				ownerRef := svc.GetOwnerReferences()
				g.Expect(ownerRef).To(HaveLen(1))
				g.Expect(ownerRef[0].Kind).To(Equal("InstanceHa"))
				g.Expect(ownerRef[0].Name).To(Equal(instanceHaName.Name))
			}, timeout, interval).Should(Succeed())
		})

		It("should mark CreateServiceReady condition as True", func() {
			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.CreateServiceReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("prerequisite resources exist and deployment is ready", func() {
		BeforeEach(func() {
			ih := CreateInstanceHaConfig(namespace, GetDefaultInstanceHaSpec())
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))

			th.SimulateDeploymentReplicaReady(instanceHaName)
		})

		It("should mark the InstanceHa as ready", func() {
			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("MetricsTLS is configured without the TLS secret", func() {
		BeforeEach(func() {
			spec := GetDefaultInstanceHaSpec()
			spec["metricsTLS"] = map[string]any{
				"secretName": "cert-instanceha-metrics",
			}
			ih := CreateInstanceHaConfig(namespace, spec)
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))
		})

		It("should wait for the metrics TLS secret", func() {
			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.TLSInputReadyCondition,
				corev1.ConditionFalse,
			)
		})
	})

	When("the default metrics TLS cert secret exists", func() {
		BeforeEach(func() {
			certSecret := CreateCertSecret(types.NamespacedName{
				Name:      "cert-instanceha-metrics",
				Namespace: namespace,
			})
			DeferCleanup(k8sClient.Delete, ctx, certSecret)

			ih := CreateInstanceHaConfig(namespace, GetDefaultInstanceHaSpec())
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))
		})

		It("should mark TLSInputReady as True", func() {
			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.TLSInputReadyCondition,
				corev1.ConditionTrue,
			)
		})

		It("should become fully ready when the deployment is ready", func() {
			th.SimulateDeploymentReplicaReady(instanceHaName)

			th.ExpectCondition(
				instanceHaName,
				ConditionGetterFunc(InstanceHaConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionTrue,
			)
		})

		It("should mount metrics TLS volumes and set env vars in the deployment", func() {
			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, instanceHaName, dep)).Should(Succeed())

				volumes := dep.Spec.Template.Spec.Volumes
				var volumeNames []string
				var found bool
				for _, v := range volumes {
					volumeNames = append(volumeNames, v.Name)
					if v.Name == "metrics-certs-tls-certs" {
						found = true
						g.Expect(v.VolumeSource.Secret).ToNot(BeNil())
						g.Expect(v.VolumeSource.Secret.SecretName).To(Equal("cert-instanceha-metrics"))
						break
					}
				}
				g.Expect(found).To(BeTrue(), "metrics-certs-tls-certs volume not found in: %v", volumeNames)

				container := dep.Spec.Template.Spec.Containers[0]

				var certMountFound, keyMountFound bool
				for _, vm := range container.VolumeMounts {
					if vm.Name == "metrics-certs-tls-certs" && vm.MountPath == instanceha.MetricsCertPath {
						certMountFound = true
					}
					if vm.Name == "metrics-certs-tls-certs" && vm.MountPath == instanceha.MetricsKeyPath {
						keyMountFound = true
					}
				}
				g.Expect(certMountFound).To(BeTrue(), "metrics cert volume mount not found")
				g.Expect(keyMountFound).To(BeTrue(), "metrics key volume mount not found")

				var certEnvFound, keyEnvFound, minVerEnvFound, ciphersEnvFound bool
				for _, e := range container.Env {
					if e.Name == "METRICS_TLS_CERT" && e.Value == instanceha.MetricsCertPath {
						certEnvFound = true
					}
					if e.Name == "METRICS_TLS_KEY" && e.Value == instanceha.MetricsKeyPath {
						keyEnvFound = true
					}
					if e.Name == "METRICS_TLS_MIN_VERSION" && e.Value == "1.2" {
						minVerEnvFound = true
					}
					if e.Name == "METRICS_TLS_CIPHERS" && e.Value == "HIGH:!aNULL:!MD5:!RC4:!3DES:!kRSA" {
						ciphersEnvFound = true
					}
				}
				g.Expect(certEnvFound).To(BeTrue(), "METRICS_TLS_CERT env var not found")
				g.Expect(keyEnvFound).To(BeTrue(), "METRICS_TLS_KEY env var not found")
				g.Expect(minVerEnvFound).To(BeTrue(), "METRICS_TLS_MIN_VERSION env var not found or wrong value")
				g.Expect(ciphersEnvFound).To(BeTrue(), "METRICS_TLS_CIPHERS env var not found or wrong value")

				g.Expect(container.LivenessProbe.HTTPGet.Scheme).To(Equal(corev1.URISchemeHTTPS))
				g.Expect(container.ReadinessProbe.HTTPGet.Scheme).To(Equal(corev1.URISchemeHTTPS))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("MetricsTLS is configured with custom TLS version and ciphers", func() {
		BeforeEach(func() {
			spec := GetDefaultInstanceHaSpec()
			spec["metricsTLS"] = map[string]any{
				"minTLSVersion": "1.3",
				"cipherSuites":  "ECDHE+AESGCM:ECDHE+CHACHA20",
			}

			certSecret := CreateCertSecret(types.NamespacedName{
				Name:      "cert-instanceha-metrics",
				Namespace: namespace,
			})
			DeferCleanup(k8sClient.Delete, ctx, certSecret)

			ih := CreateInstanceHaConfig(namespace, spec)
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))
		})

		It("should pass custom TLS config as env vars in the deployment", func() {
			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, instanceHaName, dep)).Should(Succeed())

				container := dep.Spec.Template.Spec.Containers[0]

				var minVerEnvFound, ciphersEnvFound bool
				for _, e := range container.Env {
					if e.Name == "METRICS_TLS_MIN_VERSION" && e.Value == "1.3" {
						minVerEnvFound = true
					}
					if e.Name == "METRICS_TLS_CIPHERS" && e.Value == "ECDHE+AESGCM:ECDHE+CHACHA20" {
						ciphersEnvFound = true
					}
				}
				g.Expect(minVerEnvFound).To(BeTrue(), "METRICS_TLS_MIN_VERSION env var not found or wrong value")
				g.Expect(ciphersEnvFound).To(BeTrue(), "METRICS_TLS_CIPHERS env var not found or wrong value")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("prerequisite resources exist for HMAC testing", func() {
		BeforeEach(func() {
			ih := CreateInstanceHaConfig(namespace, GetDefaultInstanceHaSpec())
			instanceHaName.Name = ih.GetName()
			instanceHaName.Namespace = ih.GetNamespace()
			DeferCleanup(th.DeleteInstance, ih)

			DeferCleanup(k8sClient.Delete, ctx, th.CreateConfigMap(types.NamespacedName{
				Name:      "openstack-config",
				Namespace: namespace,
			}, map[string]any{
				"clouds.yaml": "test-data",
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "openstack-config-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"secure.yaml": []byte("test-data"),
			}))

			DeferCleanup(k8sClient.Delete, ctx, th.CreateSecret(types.NamespacedName{
				Name:      "fencing-secret",
				Namespace: namespace,
			}, map[string][]byte{
				"fencing.yaml": []byte("test-data"),
			}))
		})

		It("should auto-generate the heartbeat HMAC secret", func() {
			Eventually(func(g Gomega) {
				hmacSecretName := types.NamespacedName{
					Name:      instanceHaName.Name + "-heartbeat-hmac",
					Namespace: instanceHaName.Namespace,
				}
				hmacSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, hmacSecretName, hmacSecret)).Should(Succeed())
				g.Expect(hmacSecret.Data).To(HaveKey("hmac-key"))
				g.Expect(hmacSecret.Data).To(HaveKey("hmac-key-previous"))
				g.Expect(hmacSecret.Data["hmac-key"]).ToNot(BeEmpty())
				g.Expect(hmacSecret.Data["hmac-key-previous"]).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})

		It("should publish HeartbeatHMACSecret in CR status", func() {
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				g.Expect(instance.Status.HeartbeatHMACSecret).To(Equal(instanceHaName.Name + "-heartbeat-hmac"))
			}, timeout, interval).Should(Succeed())
		})

		It("should mount the HMAC secret and set env vars in the deployment", func() {
			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, instanceHaName, dep)).Should(Succeed())

				container := dep.Spec.Template.Spec.Containers[0]

				var keyPathFound, prevPathFound bool
				for _, e := range container.Env {
					if e.Name == "HEARTBEAT_HMAC_KEY_PATH" {
						keyPathFound = true
					}
					if e.Name == "HEARTBEAT_HMAC_KEY_PREVIOUS_PATH" {
						prevPathFound = true
					}
				}
				g.Expect(keyPathFound).To(BeTrue(), "HEARTBEAT_HMAC_KEY_PATH env var not found")
				g.Expect(prevPathFound).To(BeTrue(), "HEARTBEAT_HMAC_KEY_PREVIOUS_PATH env var not found")

				var hmacVolumeFound bool
				for _, v := range dep.Spec.Template.Spec.Volumes {
					if v.Name == "heartbeat-hmac" {
						hmacVolumeFound = true
						g.Expect(v.VolumeSource.Secret).ToNot(BeNil())
						g.Expect(v.VolumeSource.Secret.SecretName).To(Equal(instanceHaName.Name + "-heartbeat-hmac"))
					}
				}
				g.Expect(hmacVolumeFound).To(BeTrue(), "heartbeat-hmac volume not found")
			}, timeout, interval).Should(Succeed())
		})

		It("should rotate the HMAC key when the rotate annotation is set", func() {
			// Wait for the HMAC secret to be created
			hmacSecretName := types.NamespacedName{
				Name:      instanceHaName.Name + "-heartbeat-hmac",
				Namespace: instanceHaName.Namespace,
			}
			var originalKey []byte
			Eventually(func(g Gomega) {
				hmacSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, hmacSecretName, hmacSecret)).Should(Succeed())
				g.Expect(hmacSecret.Data["hmac-key"]).ToNot(BeEmpty())
				originalKey = make([]byte, len(hmacSecret.Data["hmac-key"]))
				copy(originalKey, hmacSecret.Data["hmac-key"])
			}, timeout, interval).Should(Succeed())

			// Set the rotation annotation
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				if instance.Annotations == nil {
					instance.Annotations = map[string]string{}
				}
				instance.Annotations["instanceha.openstack.org/rotate-hmac-key"] = "true"
				g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// Verify the key was rotated: new current key, previous = old current
			Eventually(func(g Gomega) {
				hmacSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, hmacSecretName, hmacSecret)).Should(Succeed())
				g.Expect(hmacSecret.Data["hmac-key"]).ToNot(Equal(originalKey), "current key should have changed")
				g.Expect(hmacSecret.Data["hmac-key-previous"]).To(Equal(originalKey), "previous key should be the old current key")
			}, timeout, interval).Should(Succeed())

			// Verify the annotation was removed
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				_, exists := instance.Annotations["instanceha.openstack.org/rotate-hmac-key"]
				g.Expect(exists).To(BeFalse(), "rotate annotation should have been removed")
			}, timeout, interval).Should(Succeed())
		})

		It("should not double-rotate when the annotation contains the secret ResourceVersion", func() {
			hmacSecretName := types.NamespacedName{
				Name:      instanceHaName.Name + "-heartbeat-hmac",
				Namespace: instanceHaName.Namespace,
			}

			// Capture current key and secret ResourceVersion
			var currentKey []byte
			var secretRV string
			Eventually(func(g Gomega) {
				hmacSecret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, hmacSecretName, hmacSecret)).Should(Succeed())
				g.Expect(hmacSecret.Data["hmac-key"]).ToNot(BeEmpty())
				currentKey = make([]byte, len(hmacSecret.Data["hmac-key"]))
				copy(currentKey, hmacSecret.Data["hmac-key"])
				secretRV = hmacSecret.ResourceVersion
			}, timeout, interval).Should(Succeed())

			// Set the annotation to the secret's ResourceVersion, simulating
			// a prior rotation where the stamp succeeded but removal failed.
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				if instance.Annotations == nil {
					instance.Annotations = map[string]string{}
				}
				instance.Annotations["instanceha.openstack.org/rotate-hmac-key"] = secretRV
				g.Expect(k8sClient.Update(ctx, instance)).Should(Succeed())
			}, timeout, interval).Should(Succeed())

			// The controller should recognize the rotation already happened
			// and just remove the annotation without changing the key.
			Eventually(func(g Gomega) {
				instance := GetInstanceHa(instanceHaName)
				_, exists := instance.Annotations["instanceha.openstack.org/rotate-hmac-key"]
				g.Expect(exists).To(BeFalse(), "rotate annotation should have been removed")
			}, timeout, interval).Should(Succeed())

			// Verify the key was NOT changed
			hmacSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, hmacSecretName, hmacSecret)).Should(Succeed())
			Expect(hmacSecret.Data["hmac-key"]).To(Equal(currentKey), "key should not have changed during idempotent removal")
		})
	})
})
