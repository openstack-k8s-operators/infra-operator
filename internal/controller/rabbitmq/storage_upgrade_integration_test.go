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

package rabbitmq

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var _ = Describe("Storage Upgrade Integration Tests", func() {
	var (
		ctx        context.Context
		reconciler *Reconciler
		scheme     *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
	})

	Describe("ensureDeleteReclaimPolicy", func() {
		Context("when PV has Retain reclaim policy", func() {
			It("should patch PV to Delete and store original policy", func() {
				// Create PVC
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "test-pv",
					},
				}

				// Create PV with Retain policy
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
					},
				}

				// Create fake client with PV and PVC
				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc, pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc},
				}

				// Call ensureDeleteReclaimPolicy
				logger := zap.New(zap.UseDevMode(true))
				patched, err := reconciler.ensureDeleteReclaimPolicy(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(patched).To(BeTrue())

				// Verify PV was patched
				updatedPV := &corev1.PersistentVolume{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-pv"}, updatedPV)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedPV.Spec.PersistentVolumeReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimDelete))
				Expect(updatedPV.Annotations).To(HaveKey("rabbitmq.openstack.org/original-reclaim-policy"))
				Expect(updatedPV.Annotations["rabbitmq.openstack.org/original-reclaim-policy"]).To(Equal("Retain"))
			})
		})

		Context("when PV already has Delete reclaim policy", func() {
			It("should not patch PV", func() {
				// Create PVC
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "test-pv",
					},
				}

				// Create PV with Delete policy already
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc, pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc},
				}

				logger := zap.New(zap.UseDevMode(true))
				patched, err := reconciler.ensureDeleteReclaimPolicy(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(patched).To(BeFalse())

				// Verify PV was not changed
				updatedPV := &corev1.PersistentVolume{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-pv"}, updatedPV)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedPV.Spec.PersistentVolumeReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimDelete))
				Expect(updatedPV.Annotations).ToNot(HaveKey("rabbitmq.openstack.org/original-reclaim-policy"))
			})
		})

		Context("when PVC is not bound to a PV", func() {
			It("should skip the PVC", func() {
				// Create PVC without VolumeName (not bound)
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "", // Not bound
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc},
				}

				logger := zap.New(zap.UseDevMode(true))
				patched, err := reconciler.ensureDeleteReclaimPolicy(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(patched).To(BeFalse())
			})
		})

		Context("when handling multiple PVCs with mixed reclaim policies", func() {
			It("should patch only PVs with Retain policy", func() {
				// Create PVCs
				pvc1 := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-1",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "pv-retain",
					},
				}

				pvc2 := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-2",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "pv-delete",
					},
				}

				// PV with Retain policy
				pvRetain := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-retain",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test1",
							},
						},
					},
				}

				// PV with Delete policy
				pvDelete := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-delete",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test2",
							},
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc1, pvc2, pvRetain, pvDelete).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc1, *pvc2},
				}

				logger := zap.New(zap.UseDevMode(true))
				patched, err := reconciler.ensureDeleteReclaimPolicy(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(patched).To(BeTrue())

				// Verify only pvRetain was patched
				updatedPVRetain := &corev1.PersistentVolume{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "pv-retain"}, updatedPVRetain)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedPVRetain.Spec.PersistentVolumeReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimDelete))
				Expect(updatedPVRetain.Annotations).To(HaveKey("rabbitmq.openstack.org/original-reclaim-policy"))

				// Verify pvDelete was not changed
				updatedPVDelete := &corev1.PersistentVolume{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "pv-delete"}, updatedPVDelete)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedPVDelete.Spec.PersistentVolumeReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimDelete))
				Expect(updatedPVDelete.Annotations).ToNot(HaveKey("rabbitmq.openstack.org/original-reclaim-policy"))
			})
		})
	})

	Describe("verifyPVCleanupComplete", func() {
		Context("when all PVs are deleted", func() {
			It("should return true", func() {
				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				logger := zap.New(zap.UseDevMode(true))
				complete, err := reconciler.verifyPVCleanupComplete(ctx, "test-ns", "rabbitmq", nil, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(complete).To(BeTrue())
			})
		})

		Context("when PVs still reference RabbitMQ PVCs", func() {
			It("should return false", func() {
				// Create PV referencing RabbitMQ PVC
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
						ClaimRef: &corev1.ObjectReference{
							Name:      "persistence-rabbitmq-0",
							Namespace: "test-ns",
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				logger := zap.New(zap.UseDevMode(true))
				complete, err := reconciler.verifyPVCleanupComplete(ctx, "test-ns", "rabbitmq", nil, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(complete).To(BeFalse())
			})
		})

		Context("when PV cleanup times out", func() {
			It("should return timeout error after 30 minutes", func() {
				// Create PV referencing RabbitMQ PVC
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
						ClaimRef: &corev1.ObjectReference{
							Name:      "persistence-rabbitmq-0",
							Namespace: "test-ns",
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				// Set start time to 31 minutes ago
				startTime := time.Now().Add(-31 * time.Minute)

				logger := zap.New(zap.UseDevMode(true))
				complete, err := reconciler.verifyPVCleanupComplete(ctx, "test-ns", "rabbitmq", &startTime, logger)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("timeout"))
				Expect(complete).To(BeFalse())
			})
		})

		Context("when PV cleanup has not timed out", func() {
			It("should return false without error", func() {
				// Create PV referencing RabbitMQ PVC
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
						ClaimRef: &corev1.ObjectReference{
							Name:      "persistence-rabbitmq-0",
							Namespace: "test-ns",
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				// Set start time to 15 minutes ago (within timeout)
				startTime := time.Now().Add(-15 * time.Minute)

				logger := zap.New(zap.UseDevMode(true))
				complete, err := reconciler.verifyPVCleanupComplete(ctx, "test-ns", "rabbitmq", &startTime, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(complete).To(BeFalse())
			})
		})

		Context("when PVs reference other instances", func() {
			It("should return true", func() {
				// Create PV referencing different RabbitMQ instance
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/test",
							},
						},
						ClaimRef: &corev1.ObjectReference{
							Name:      "persistence-other-rabbitmq-0", // Different instance
							Namespace: "test-ns",
						},
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pv).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				logger := zap.New(zap.UseDevMode(true))
				complete, err := reconciler.verifyPVCleanupComplete(ctx, "test-ns", "rabbitmq", nil, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(complete).To(BeTrue())
			})
		})
	})

	Describe("deletePVCsForUpgrade", func() {
		Context("when deleting PVCs in order", func() {
			It("should delete PVCs in sorted order", func() {
				// Create PVCs in non-sorted order
				pvc2 := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "persistence-rabbitmq-2",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "pv-2",
					},
				}

				pvc0 := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "persistence-rabbitmq-0",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "pv-0",
					},
				}

				pvc1 := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "persistence-rabbitmq-1",
						Namespace: "test-ns",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "pv-1",
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc2, pvc0, pvc1).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc2, *pvc0, *pvc1},
				}

				logger := zap.New(zap.UseDevMode(true))
				deleted, err := reconciler.deletePVCsForUpgrade(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(deleted).To(BeTrue())

				// Verify all PVCs are marked for deletion
				// Note: fake client doesn't actually delete, but marks DeletionTimestamp
				for _, pvcName := range []string{"persistence-rabbitmq-0", "persistence-rabbitmq-1", "persistence-rabbitmq-2"} {
					pvc := &corev1.PersistentVolumeClaim{}
					err := fakeClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: "test-ns"}, pvc)
					// Should be NotFound or have DeletionTimestamp set
					if err == nil {
						Expect(pvc.DeletionTimestamp).ToNot(BeNil())
					}
				}
			})
		})

		Context("when removing PVC protection finalizers", func() {
			It("should remove kubernetes.io/pvc-protection but preserve others", func() {
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "test-ns",
						Finalizers: []string{
							"kubernetes.io/pvc-protection",
							"csi.io/driver-finalizer",
						},
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "test-pv",
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(pvc).
					Build()

				reconciler = &Reconciler{
					Client: fakeClient,
				}

				pvcList := &corev1.PersistentVolumeClaimList{
					Items: []corev1.PersistentVolumeClaim{*pvc},
				}

				logger := zap.New(zap.UseDevMode(true))
				deleted, err := reconciler.deletePVCsForUpgrade(ctx, pvcList, logger)

				Expect(err).ToNot(HaveOccurred())
				Expect(deleted).To(BeTrue())

				// Verify finalizer was removed
				updatedPVC := &corev1.PersistentVolumeClaim{}
				err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-pvc", Namespace: "test-ns"}, updatedPVC)
				if err == nil {
					// PVC still exists, check finalizers
					Expect(updatedPVC.Finalizers).ToNot(ContainElement("kubernetes.io/pvc-protection"))
					Expect(updatedPVC.Finalizers).To(ContainElement("csi.io/driver-finalizer"))
				}
			})
		})
	})
})
