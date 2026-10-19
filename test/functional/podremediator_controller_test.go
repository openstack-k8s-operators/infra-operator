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
	"github.com/google/uuid"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	remediation_ctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("PodRemediator controller", func() {
	var prName types.NamespacedName

	When("a PodRemediator is created without NHC/SNR in the cluster", func() {
		BeforeEach(func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)
		})

		It("should set Ready condition to False with NHC/SNR required message", func() {
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance).To(Not(BeNil()))
				ready := instance.Status.Conditions.Get(condition.ReadyCondition)
				g.Expect(ready).To(Not(BeNil()))
				g.Expect(ready.Status).To(Equal(corev1.ConditionFalse))
				g.Expect(ready.Reason).To(Equal("NHC/SNRNotFound"))
				g.Expect(ready.Message).To(ContainSubstring("Node Health Check"))
				g.Expect(ready.Message).To(ContainSubstring("Self Node Remediation"))
			}, timeout, interval).Should(Succeed())
		})

		It("should set InputReady condition to False", func() {
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance).To(Not(BeNil()))
				inputReady := instance.Status.Conditions.Get(condition.InputReadyCondition)
				g.Expect(inputReady).To(Not(BeNil()))
				g.Expect(inputReady.Status).To(Equal(corev1.ConditionFalse))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a PodRemediator is created with disabled true", func() {
		BeforeEach(func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(true, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)
		})

		It("should still report Ready False when NHC/SNR are missing", func() {
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance).To(Not(BeNil()))
				g.Expect(instance.Spec.Disabled).To(BeTrue())
				ready := instance.Status.Conditions.Get(condition.ReadyCondition)
				g.Expect(ready).To(Not(BeNil()))
				g.Expect(ready.Status).To(Equal(corev1.ConditionFalse))
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a PodRemediator is deleted", func() {
		BeforeEach(func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			_ = GetPodRemediator(prName)
			th.DeleteInstance(pr)
		})

		It("should remove the CR after finalizer runs", func() {
			Eventually(func(g Gomega) {
				instance := &remediationv1.PodRemediator{}
				err := k8sClient.Get(ctx, prName, instance)
				g.Expect(err).To(HaveOccurred())
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a PodRemediator has the operator finalizer", func() {
		BeforeEach(func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)
		})

		It("should have the finalizer set on the CR", func() {
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance).To(Not(BeNil()))
				g.Expect(instance.ObjectMeta.Finalizers).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("NHC/SNR are present and a local PVC is on an unhealthy node", func() {
		var nodeName string
		var pvName string
		var pvcName string

		BeforeEach(func() {
			CreateMedik8sCRDs()
			CreateNHCInstance()
			CreateSNRTemplate(namespace)

			nodeName = "worker-" + uuid.New().String()[:8]
			pvName = "pv-" + uuid.New().String()[:8]
			pvcName = "pvc-" + uuid.New().String()[:8]

			CreateNodeWithReadyCondition(nodeName, false)
			DeferCleanup(func() {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err == nil {
					_ = k8sClient.Delete(ctx, node)
				}
			})

			CreateSelfNodeRemediation(namespace, nodeName)

			CreateLocalPV(pvName, nodeName)
			DeferCleanup(func() {
				pv := &corev1.PersistentVolume{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvName}, pv); err == nil {
					_ = k8sClient.Delete(ctx, pv)
				}
			})

			CreateBoundPVC(namespace, pvcName, pvName)
		})

		It("should annotate the PVC with pvc-stuck-on-node but not delete it (Phase 1)", func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediation_ctrl.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			Consistently(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			}, timeout/5, interval).Should(Succeed())
		})

		It("should delete the PVC when safe-to-delete is set (Phase 3)", func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediation_ctrl.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediation_ctrl.SafeToDeleteAnnotation] = "true"
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should remove pvc-stuck-on-node when node recovers (Path A)", func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediation_ctrl.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			UpdateNodeReadyCondition(nodeName, true)

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediation_ctrl.PVCStuckOnNodeAnnotation))
			}, timeout, interval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
		})

		It("should clean up pvc-stuck-on-node but not safe-to-delete on CR deletion", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediation_ctrl.PVCStuckOnNodeAnnotation] = nodeName
			pvc.Annotations[remediation_ctrl.SafeToDeleteAnnotation] = "true"
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			_ = GetPodRemediator(prName)
			th.DeleteInstance(pr)

			Eventually(func(g Gomega) {
				instance := &remediationv1.PodRemediator{}
				err := k8sClient.Get(ctx, prName, instance)
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())

			pvc = &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			Expect(pvc.Annotations).ToNot(HaveKey(remediation_ctrl.PVCStuckOnNodeAnnotation))
			Expect(pvc.Annotations).To(HaveKeyWithValue(remediation_ctrl.SafeToDeleteAnnotation, "true"))
		})
	})
})
