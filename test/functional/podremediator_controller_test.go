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
				g.Expect(string(ready.Reason)).To(Equal(remediation_ctrl.NHCNotFoundReason))
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
					remediationv1.PVCStuckOnNodeAnnotation, nodeName))
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
					remediationv1.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
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
					remediationv1.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			UpdateNodeReadyCondition(nodeName, true)

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.PVCStuckOnNodeAnnotation))
			}, timeout, interval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
		})

		It("should not remove pvc-stuck-on-node when node is still unhealthy but has no active SNR", func() {
			// Path A must only fire when the node is truly back to Ready (not in rawUnhealthyNodes).
			// If a node is still NotReady but its SNR CR has expired (e.g. post-fencing, hardware
			// issue), the annotation must be preserved so the handshake can continue or be
			// completed once the node finally recovers.
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			// node2: unhealthy but NO SNR CR — simulates post-fencing or pre-NHC-decision state.
			node2Name := "worker-" + uuid.New().String()[:8]
			CreateNodeWithReadyCondition(node2Name, false)
			DeferCleanup(func() {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: node2Name}, node); err == nil {
					_ = k8sClient.Delete(ctx, node)
				}
			})

			// Pre-annotate the PVC as stuck on node2 (which has no SNR).
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = node2Name
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			// Controller: rawUnhealthyNodes={nodeName, node2Name}, snrGatedNodes={nodeName}.
			// PVC stuckOnNode=node2Name is in rawUnhealthyNodes → NOT Path A → annotation kept.
			Consistently(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediationv1.PVCStuckOnNodeAnnotation, node2Name))
			}, timeout/5, interval).Should(Succeed())
		})

		It("should remove safe-to-delete together with pvc-stuck-on-node on node recovery (Path A)", func() {
			// Verify that consent granted by the app operator does not carry over to a future
			// fault on the same node. Both annotations must be cleared when the node recovers.
			//
			// Test design: pre-set both annotations on the PVC with node1 already healthy,
			// and keep node2 unhealthy so the controller enters the PVC loop (Path A requires
			// at least one unhealthy node). node1 (the PVC's stuck node) not being in
			// unhealthyNodes triggers Path A, which must remove both annotations.
			// This avoids races between safe-to-delete being set and Path B firing.
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			// Make the PVC's node (node1) healthy so Path A fires instead of Path B.
			UpdateNodeReadyCondition(nodeName, true)

			// node2: kept unhealthy so the controller enters the PVC scan loop.
			node2Name := "worker-" + uuid.New().String()[:8]
			CreateNodeWithReadyCondition(node2Name, false)
			DeferCleanup(func() {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: node2Name}, node); err == nil {
					_ = k8sClient.Delete(ctx, node)
				}
			})
			CreateSelfNodeRemediation(namespace, node2Name)

			// Pre-annotate PVC with both annotations to simulate: node was unhealthy → PodRemediator
			// set pvc-stuck-on-node → app operator set safe-to-delete → node then recovered.
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = nodeName
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false, nil))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			// Controller: node2 unhealthy → enters PVC loop → PVC has stuckOnNode=node1 but
			// node1 is healthy → Path A → removes both annotations. PVC must NOT be deleted.
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.PVCStuckOnNodeAnnotation))
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
			}, timeout, interval).Should(Succeed())
		})

It("should clean up both pvc-stuck-on-node and safe-to-delete on CR deletion", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = nodeName
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
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
			Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.PVCStuckOnNodeAnnotation))
			Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
		})
	})
})
