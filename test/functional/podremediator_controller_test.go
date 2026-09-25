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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientscheme "k8s.io/client-go/kubernetes/scheme"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediation_ctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("PodRemediator controller", func() {
	var prName types.NamespacedName

	When("a PodRemediator is created without NHC/SNR in the cluster", func() {
		BeforeEach(func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(true))
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
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			// Wait for the controller to persist its finalizer so deletion
			// actually exercises reconcileDelete instead of removing the CR outright.
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance.Finalizers).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())
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
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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

		It("reacts to SNR fencing without waiting for the polling interval", func() {
			snr := &unstructured.Unstructured{}
			snr.SetGroupVersionKind(schema.GroupVersionKind{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation"})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: nodeName + "-snr"}, snr)).To(Succeed())
			Expect(unstructured.SetNestedField(snr.Object, "Pre-Reboot-Completed", "status", "phase")).To(Succeed())
			Expect(k8sClient.Update(ctx, snr)).To(Succeed())
			spec := GetPodRemediatorSpec(false)
			spec["periodicPollInterval"] = "1h"
			spec["consentPollInterval"] = "1h"
			pr := CreatePodRemediator(namespace, spec)
			DeferCleanup(th.DeleteInstance, pr)
			key := types.NamespacedName{Name: pvcName, Namespace: namespace}
			Consistently(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.PVCStuckOnNodeAnnotation))
			}, timeout/5, interval).Should(Succeed())
			Expect(unstructured.SetNestedField(snr.Object, "Reboot-Completed", "status", "phase")).To(Succeed())
			Expect(k8sClient.Update(ctx, snr)).To(Succeed())
			// The optional informer starts before the SNR CRD exists. Its list retry
			// backoff can reach 60s including jitter, so allow it to reconnect.
			// Both polling intervals are 1h: success still requires a watch event.
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(remediationv1.PVCStuckOnNodeAnnotation, nodeName))
			}, 2*time.Minute, interval).Should(Succeed())
		})

		It("requires confirmed fencing even for an already consented PVC", func() {
			snr := &unstructured.Unstructured{}
			snr.SetGroupVersionKind(schema.GroupVersionKind{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation"})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: nodeName + "-snr"}, snr)).To(Succeed())
			key := types.NamespacedName{Name: pvcName, Namespace: namespace}
			spec := GetPodRemediatorSpec(false)
			spec["consentPollInterval"] = "1s"
			pr := CreatePodRemediator(namespace, spec)
			DeferCleanup(th.DeleteInstance, pr)
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKey(remediationv1.RequestIDAnnotation))
			}, timeout, interval).Should(Succeed())
			pod := CreatePodForPVC(namespace, "fencing-"+uuid.New().String()[:8], nodeName, pvcName)
			unstructured.RemoveNestedField(snr.Object, "status")
			Expect(k8sClient.Update(ctx, snr)).To(Succeed())
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{})).To(Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})).To(Succeed())
			}, timeout/5, interval).Should(Succeed())
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			grantRemediationConsent(pvc)
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())
			Expect(unstructured.SetNestedField(snr.Object, "Fencing-Completed", "status", "phase")).To(Succeed())
			Expect(k8sClient.Update(ctx, snr)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("requires fresh consent after a disabled maintenance window", func() {
			key := types.NamespacedName{Name: pvcName, Namespace: namespace}
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			pvc.Annotations = map[string]string{remediationv1.PVCStuckOnNodeAnnotation: nodeName, remediationv1.SafeToDeleteAnnotation: "true"}
			Expect(k8sClient.Update(ctx, pvc)).To(Succeed())
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(true))
			DeferCleanup(th.DeleteInstance, pr)
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.PVCStuckOnNodeAnnotation))
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
			}, timeout, interval).Should(Succeed())
			UpdateNodeReadyCondition(nodeName, true)
			UpdateNodeReadyCondition(nodeName, false)
			Eventually(func(g Gomega) {
				instance := &remediationv1.PodRemediator{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pr), instance)).To(Succeed())
				instance.Spec.Disabled = false
				g.Expect(k8sClient.Update(ctx, instance)).To(Succeed())
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(remediationv1.PVCStuckOnNodeAnnotation, nodeName))
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
			}, timeout, interval).Should(Succeed())
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{})).To(Succeed())
			}, timeout/5, interval).Should(Succeed())
			Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			grantRemediationConsent(pvc)
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("completes consented remediation after Node deletion with explicit TopoLVM node-name affinity", func() {
			key := types.NamespacedName{Name: pvcName, Namespace: namespace}
			// A hostname label is not a Node identity after the Node object is gone.
			// TopoLVM/LVMS affinity explicitly carries the Kubernetes Node name and
			// is the supported deleted-Node compatibility path.
			oldPVC := getFunctionalPVC(pvcName)
			Expect(k8sClient.Delete(ctx, oldPVC)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			pv := &corev1.PersistentVolume{}
			pvKey := types.NamespacedName{Name: pvName}
			Expect(k8sClient.Get(ctx, pvKey, pv)).To(Succeed())
			// envtest has no PV protection controller to remove a protection
			// finalizer after the claim is gone. Clear finalizers from this old,
			// test-owned fixture using its freshly fetched resourceVersion so the
			// same PV name can be recreated with different topology affinity.
			pv.Finalizers = nil
			Expect(k8sClient.Update(ctx, pv)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pv)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, pvKey, &corev1.PersistentVolume{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			CreateLocalPVWithNodeTopologyKey(pvName, nodeName, "topology.topolvm.io/node")
			CreateBoundPVC(namespace, pvcName, pvName)
			boundPVC := getFunctionalPVC(pvcName)
			boundPVC.Status.Phase = corev1.ClaimBound
			Expect(k8sClient.Status().Update(ctx, boundPVC)).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			DeferCleanup(th.DeleteInstance, pr)
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(remediationv1.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, key, pvc)).To(Succeed())
			oldPVC = pvc.DeepCopy()
			grantRemediationConsent(pvc)
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(k8sClient.Delete(ctx, node)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &corev1.Node{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, key, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should annotate the PVC with pvc-stuck-on-node but not delete it (Phase 1)", func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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
			grantRemediationConsent(pvc)
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})
				g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("should remove pvc-stuck-on-node when node recovers (Path A)", func() {
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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
			// and keep node2 unhealthy to exercise cleanup during another fault.
			// node1 (the PVC's stuck node) not being in
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

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
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

			// Keep fencing unconfirmed so only the CR finalizer removes the annotations.
			snr := &unstructured.Unstructured{}
			snr.SetGroupVersionKind(schema.GroupVersionKind{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation"})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: nodeName + "-snr"}, snr)).To(Succeed())
			unstructured.RemoveNestedField(snr.Object, "status")
			Expect(k8sClient.Update(ctx, snr)).To(Succeed())
			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			// Wait for the finalizer so deleting the CR actually exercises reconcileDelete.
			Eventually(func(g Gomega) {
				instance := GetPodRemediator(prName)
				g.Expect(instance.Finalizers).ToNot(BeEmpty())
			}, timeout, interval).Should(Succeed())
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

	When("a non-local PVC carries forged stuck + safe-to-delete annotations", func() {
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

			// Network-attached (CSI, zone-affinity) PV: NOT node-local.
			CreateNonLocalPV(pvName, nodeName)
			DeferCleanup(func() {
				pv := &corev1.PersistentVolume{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvName}, pv); err == nil {
					_ = k8sClient.Delete(ctx, pv)
				}
			})

			CreateBoundPVC(namespace, pvcName, pvName)
		})

		It("must not delete the PVC when its PV is not node-local (Path B provenance re-check)", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			// Forge both annotations: stuck on the (real) unhealthy node + consent granted.
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = nodeName
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			// Path B must refuse: the PV is not node-local, so the forged annotations
			// cannot escalate into a delete.
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})).To(Succeed())
			}, timeout/5, interval).Should(Succeed())
		})
	})

	When("a local PVC already carries a stale safe-to-delete before the handshake starts", func() {
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

		It("strips the stale safe-to-delete when Path D starts the handshake", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			// Pre-set ONLY safe-to-delete (no pvc-stuck-on-node): stale/forged consent.
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			// Path D annotates pvc-stuck-on-node AND strips the stale consent in the same patch,
			// so the PVC must survive (Path B never sees consent for this fresh fault).
			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediationv1.PVCStuckOnNodeAnnotation, nodeName))
				g.Expect(pvc.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
			}, timeout, interval).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{})).To(Succeed())
			}, timeout/5, interval).Should(Succeed())
		})
	})

	When("a PVC is stuck on a node that no longer exists", func() {
		var livingNodeName string
		var ghostNodeName string
		var pvName string
		var pvcName string

		BeforeEach(func() {
			CreateMedik8sCRDs()
			CreateNHCInstance()
			CreateSNRTemplate(namespace)

			livingNodeName = "worker-" + uuid.New().String()[:8]
			ghostNodeName = "ghost-" + uuid.New().String()[:8]
			pvName = "pv-" + uuid.New().String()[:8]
			pvcName = "pvc-" + uuid.New().String()[:8]

			// A real unhealthy node so the controller enters the PVC scan loop.
			CreateNodeWithReadyCondition(livingNodeName, false)
			DeferCleanup(func() {
				node := &corev1.Node{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: livingNodeName}, node); err == nil {
					_ = k8sClient.Delete(ctx, node)
				}
			})
			CreateSelfNodeRemediation(namespace, livingNodeName)

			CreateLocalPV(pvName, ghostNodeName)
			DeferCleanup(func() {
				pv := &corev1.PersistentVolume{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvName}, pv); err == nil {
					_ = k8sClient.Delete(ctx, pv)
				}
			})

			CreateBoundPVC(namespace, pvcName, pvName)
		})

		It("keeps pvc-stuck-on-node instead of treating a deleted node as recovery (Path A)", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}

			// Pre-annotate the PVC as stuck on a node that never existed in the cluster.
			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			if pvc.Annotations == nil {
				pvc.Annotations = make(map[string]string)
			}
			pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation] = ghostNodeName
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			// The stuck node is absent from allNodeNames, so Path A must NOT strip the annotation.
			Consistently(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediationv1.PVCStuckOnNodeAnnotation, ghostNodeName))
			}, timeout/5, interval).Should(Succeed())
		})
	})

	When("a PVC is mounted by pods on more than one node", func() {
		var nodeName string
		var otherNodeName string
		var pvName string
		var pvcName string

		BeforeEach(func() {
			CreateMedik8sCRDs()
			CreateNHCInstance()
			CreateSNRTemplate(namespace)

			nodeName = "worker-" + uuid.New().String()[:8]
			otherNodeName = "worker-" + uuid.New().String()[:8]
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

		It("aborts remediation when a cross-node Pod uses the same PVC (Path B pod gate)", func() {
			pvcKey := types.NamespacedName{Name: pvcName, Namespace: namespace}
			stuckPodName := "pod-stuck-" + uuid.New().String()[:8]
			otherPodName := "pod-other-" + uuid.New().String()[:8]

			DeferCleanup(func() {
				current := &corev1.PersistentVolumeClaim{}
				if err := k8sClient.Get(ctx, pvcKey, current); err == nil {
					current.Finalizers = nil
					_ = k8sClient.Update(ctx, current)
					_ = k8sClient.Delete(ctx, current)
				}
			})

			stuckPod := CreatePodForPVC(namespace, stuckPodName, nodeName, pvcName)
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, stuckPod) })
			otherPod := CreatePodForPVC(namespace, otherPodName, otherNodeName, pvcName)
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, otherPod) })

			pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
			prName.Name = pr.GetName()
			prName.Namespace = pr.GetNamespace()
			DeferCleanup(th.DeleteInstance, pr)

			Eventually(func(g Gomega) {
				pvc := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
				g.Expect(pvc.Annotations).To(HaveKeyWithValue(
					remediationv1.PVCStuckOnNodeAnnotation, nodeName))
			}, timeout, interval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, pvcKey, pvc)).To(Succeed())
			oldPVC := pvc.DeepCopy()
			grantRemediationConsent(pvc)
			Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())
			Eventually(func(g Gomega) {
				currentPVC := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, currentPVC)).To(Succeed())
				g.Expect(currentPVC.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
				g.Expect(currentPVC.Annotations).ToNot(HaveKey(remediationv1.ConsentIDAnnotation))
			}, timeout, interval).Should(Succeed())

			// A claim user on another node aborts the entire deletion decision;
			// neither the stuck-node Pod nor the cross-node Pod can be deleted.
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: stuckPodName, Namespace: namespace}, &corev1.Pod{})).To(Succeed())
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: otherPodName, Namespace: namespace}, &corev1.Pod{})).To(Succeed())
				currentPVC := &corev1.PersistentVolumeClaim{}
				g.Expect(k8sClient.Get(ctx, pvcKey, currentPVC)).To(Succeed())
				g.Expect(currentPVC.DeletionTimestamp).To(BeNil())
			}, timeout/5, interval).Should(Succeed())
		})
	})
})

func grantRemediationConsent(pvc *corev1.PersistentVolumeClaim) {
	requestID := pvc.Annotations[remediationv1.RequestIDAnnotation]
	Expect(requestID).NotTo(BeEmpty())
	if pvc.Annotations == nil {
		pvc.Annotations = make(map[string]string)
	}
	pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
	pvc.Annotations[remediationv1.ConsentIDAnnotation] = requestID
}

var _ = Describe("PodRemediator ConfigMap-backed deletion commit", func() {
	const podHoldFinalizer = "functional-test.openstack.org/hold-pod-deletion"

	var (
		nodeName  string
		otherNode string
		pvName    string
		pvcName   string
		podName   string
		pod       *corev1.Pod
		prKey     types.NamespacedName
	)

	BeforeEach(func() {
		CreateMedik8sCRDs()
		CreateNHCInstance()
		CreateSNRTemplate(namespace)

		nodeName = "worker-" + uuid.New().String()[:8]
		otherNode = "worker-" + uuid.New().String()[:8]
		pvName = "pv-" + uuid.New().String()[:8]
		pvcName = "pvc-" + uuid.New().String()[:8]
		podName = "pod-" + uuid.New().String()[:8]

		CreateNodeWithReadyCondition(nodeName, false)
		DeferCleanup(func() {
			current := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, current); err == nil {
				_ = k8sClient.Delete(ctx, current)
			}
		})
		CreateSelfNodeRemediation(namespace, nodeName)
		DeferCleanup(func() {
			snr := &unstructured.Unstructured{}
			snr.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation",
			})
			snr.SetNamespace(namespace)
			snr.SetName(nodeName + "-snr")
			_ = k8sClient.Delete(ctx, snr)
		})

		CreateLocalPV(pvName, nodeName)
		DeferCleanup(func() {
			current := &corev1.PersistentVolume{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvName}, current); err == nil {
				_ = k8sClient.Delete(ctx, current)
			}
		})
		CreateBoundPVC(namespace, pvcName, pvName)
		// envtest also has no persistent-volume binder; mark the fixture claim as
		// bound after binding it by volumeName so this case covers a bound PVC.
		boundPVC := getFunctionalPVC(pvcName)
		boundPVC.Status.Phase = corev1.ClaimBound
		Expect(k8sClient.Status().Update(ctx, boundPVC)).To(Succeed())
		DeferCleanup(func() {
			current := &corev1.PersistentVolumeClaim{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, current); err == nil {
				current.Finalizers = nil
				_ = k8sClient.Update(ctx, current)
				_ = k8sClient.Delete(ctx, current)
			}
		})

		// envtest has no kube-controller-manager, garbage collector, or PVC
		// protection controller. This finalizer holds the Pod API object so these
		// tests can cover ConfigMap persistence and reconciler behavior only.
		pod = CreatePodForPVC(namespace, podName, nodeName, pvcName)
		oldPod := pod.DeepCopy()
		pod.Finalizers = []string{podHoldFinalizer}
		Expect(k8sClient.Patch(ctx, pod, client.MergeFrom(oldPod))).To(Succeed())
		DeferCleanup(func() {
			current := &corev1.Pod{}
			key := types.NamespacedName{Namespace: namespace, Name: podName}
			if err := k8sClient.Get(ctx, key, current); err == nil {
				current.Finalizers = nil
				_ = k8sClient.Update(ctx, current)
				_ = k8sClient.Delete(ctx, current)
			}
		})

		pr := CreatePodRemediator(namespace, GetPodRemediatorSpec(false))
		prKey = types.NamespacedName{Namespace: pr.GetNamespace(), Name: pr.GetName()}
		DeferCleanup(func() {
			// A committed-deletion test may intentionally leave this Pod terminating
			// behind its test finalizer. Release it before deleting the PodRemediator,
			// otherwise its own deletion correctly waits for the committed cleanup.
			current := &corev1.Pod{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, current); err == nil && len(current.Finalizers) > 0 {
				current.Finalizers = nil
				Expect(k8sClient.Update(ctx, current)).To(Succeed())
			}
			th.DeleteInstance(pr)
		})

		Eventually(func(g Gomega) {
			instance := GetPodRemediator(prKey)
			pvc := &corev1.PersistentVolumeClaim{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, pvc)).To(Succeed())
			g.Expect(pvc.Annotations[remediationv1.RequestIDAnnotation]).NotTo(BeEmpty())
			g.Expect(pvc.Annotations[remediationv1.RemediatorUIDAnnotation]).To(Equal(string(instance.UID)))
		}, timeout, interval).Should(Succeed())

	})

	It("persists PVC and Pod identities in a namespaced ConfigMap before cleanup", func() {
		commit := startConfigMapBackedDeletion(namespace, pvcName, podName, nodeName, prKey, pod)
		state := decodeFunctionalDeletionCommit(commit)

		Expect(commit.Namespace).To(Equal(namespace))
		Expect(state.PVCName).To(Equal(pvcName))
		Expect(state.PVCUID).To(Equal(string(getFunctionalPVC(pvcName).UID)))
		Expect(state.RemediatorUID).To(Equal(string(GetPodRemediator(prKey).UID)))
		Expect(state.Node).To(Equal(nodeName))
		Expect(state.Pods).To(ConsistOf(functionalCommittedPod{Name: podName, UID: string(pod.UID)}))
		Expect(getFunctionalPVC(pvcName).Spec.VolumeName).To(Equal(pvName))
		Expect(getFunctionalPVC(pvcName).Status.Phase).To(Equal(corev1.ClaimBound))
		Expect(getFunctionalPVC(pvcName).Annotations[remediationv1.PVCDeletionCommittedAnnotation]).ToNot(BeEmpty())

		Eventually(func(g Gomega) {
			current := &corev1.Pod{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, current)).To(Succeed())
			g.Expect(current.DeletionTimestamp).ToNot(BeNil())
			g.Expect(current.Finalizers).To(ContainElement(podHoldFinalizer))
		}, timeout, interval).Should(Succeed())
	})

	It("resumes a persisted commit after SNR loss, Node recovery, disablement, and CR deletion", func() {
		pvcUID := string(getFunctionalPVC(pvcName).UID)
		commit := startConfigMapBackedDeletion(namespace, pvcName, podName, nodeName, prKey, pod)

		snr := &unstructured.Unstructured{}
		snr.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation",
		})
		snr.SetNamespace(namespace)
		snr.SetName(nodeName + "-snr")
		Expect(k8sClient.Delete(ctx, snr)).To(Succeed())
		UpdateNodeReadyCondition(nodeName, true)
		instance := GetPodRemediator(prKey)
		instance.Spec.Disabled = true
		Expect(k8sClient.Update(ctx, instance)).To(Succeed())

		freshReconciler := newFunctionalPodRemediatorReconciler()
		Eventually(func(g Gomega) {
			_, reconcileErr := freshReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: prKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())
		}, timeout, interval).Should(Succeed())
		stillCommitted, err := findFunctionalDeletionCommit(namespace, pvcUID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stillCommitted.UID).To(Equal(commit.UID))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, &corev1.Pod{})).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &corev1.PersistentVolumeClaim{})).To(Succeed())

		deleting := GetPodRemediator(prKey)
		Expect(k8sClient.Delete(ctx, deleting)).To(Succeed())
		Eventually(func(g Gomega) {
			_, reconcileErr := freshReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: prKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())
			current := &remediationv1.PodRemediator{}
			g.Expect(k8sClient.Get(ctx, prKey, current)).To(Succeed())
			g.Expect(current.DeletionTimestamp).ToNot(BeNil())
			g.Expect(current.Finalizers).NotTo(BeEmpty())
			currentConfigMap, commitErr := findFunctionalDeletionCommit(namespace, pvcUID)
			g.Expect(commitErr).NotTo(HaveOccurred())
			g.Expect(currentConfigMap.UID).To(Equal(commit.UID))
		}, timeout, interval).Should(Succeed())

		removeFunctionalPodFinalizer(podName)
		Eventually(func(g Gomega) {
			_, reconcileErr := freshReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: prKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())
			g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, prKey, &remediationv1.PodRemediator{}))).To(BeTrue())
			g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &corev1.PersistentVolumeClaim{}))).To(BeTrue())
			expectNoFunctionalDeletionCommit(g, namespace, pvcUID)
		}, timeout, interval).Should(Succeed())
	})

	It("scrubs an informational commit marker without a ConfigMap and rejects replacement claim users", func() {
		pvc := getFunctionalPVC(pvcName)
		forgedPVCUID := string(pvc.UID)
		pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = "v1|forged-node|forged-pvc|forged-remediator"
		pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
		pvc.Annotations[remediationv1.ConsentIDAnnotation] = "forged-consent"
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())

		Eventually(func(g Gomega) {
			current := getFunctionalPVC(pvcName)
			g.Expect(current.Annotations).ToNot(HaveKey(remediationv1.PVCDeletionCommittedAnnotation))
			g.Expect(remediationv1.HasRemediationConsent(current.Annotations)).To(BeFalse())
			expectNoFunctionalDeletionCommit(g, namespace, forgedPVCUID)
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, &corev1.Pod{})).To(Succeed())
		}, timeout, interval).Should(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &corev1.PersistentVolumeClaim{})).To(Succeed())

		pvcUID := string(getFunctionalPVC(pvcName).UID)
		commit := startConfigMapBackedDeletion(namespace, pvcName, podName, nodeName, prKey, pod)
		otherNodeObject := CreateNodeWithReadyCondition(otherNode, true)
		DeferCleanup(func() {
			current := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: otherNodeObject.Name}, current); err == nil {
				_ = k8sClient.Delete(ctx, current)
			}
		})
		replacement := CreatePodForPVC(namespace, "replacement-"+uuid.New().String()[:8], otherNodeObject.Name, pvcName)
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, replacement) })

		freshReconciler := newFunctionalPodRemediatorReconciler()
		Eventually(func(g Gomega) {
			_, reconcileErr := freshReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: prKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())
			expectNoFunctionalDeletionCommit(g, namespace, pvcUID)
			current := getFunctionalPVC(pvcName)
			// A later reconcile may begin a fresh handshake for the same unhealthy
			// Node. It must not preserve commit authority or the previous consent.
			g.Expect(current.DeletionTimestamp).To(BeNil())
			g.Expect(current.Annotations).ToNot(HaveKey(remediationv1.PVCDeletionCommittedAnnotation))
			g.Expect(current.Annotations).ToNot(HaveKey(remediationv1.SafeToDeleteAnnotation))
			g.Expect(current.Annotations).ToNot(HaveKey(remediationv1.ConsentIDAnnotation))
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), &corev1.Pod{})).To(Succeed())
		}, timeout, interval).Should(Succeed())
		Expect(commit.UID).NotTo(BeEmpty())

		deleting := GetPodRemediator(prKey)
		Expect(k8sClient.Delete(ctx, deleting)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8s_errors.IsNotFound(k8sClient.Get(ctx, prKey, &remediationv1.PodRemediator{}))).To(BeTrue())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), &corev1.Pod{})).To(Succeed())
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: pvcName}, &corev1.PersistentVolumeClaim{})).To(Succeed())
		}, timeout, interval).Should(Succeed())
	})
})

type functionalCommittedPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type functionalDeletionCommit struct {
	PVCName       string                   `json:"pvcName"`
	PVCUID        string                   `json:"pvcUID"`
	RemediatorUID string                   `json:"remediatorUID"`
	Node          string                   `json:"node"`
	Pods          []functionalCommittedPod `json:"pods"`
}

func newFunctionalPodRemediatorReconciler() *remediation_ctrl.PodRemediatorReconciler {
	kclient, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	return &remediation_ctrl.PodRemediatorReconciler{
		Client:        k8sClient,
		APIReader:     k8sClient,
		Scheme:        clientscheme.Scheme,
		Kclient:       kclient,
		DynamicClient: dynClient,
	}
}

func startConfigMapBackedDeletion(namespace, pvcName, podName, nodeName string, prKey types.NamespacedName, pod *corev1.Pod) *corev1.ConfigMap {
	pvc := getFunctionalPVC(pvcName)
	oldPVC := pvc.DeepCopy()
	grantRemediationConsent(pvc)
	Expect(k8sClient.Patch(ctx, pvc, client.MergeFrom(oldPVC))).To(Succeed())

	var commit *corev1.ConfigMap
	Eventually(func(g Gomega) {
		var err error
		commit, err = findFunctionalDeletionCommit(namespace, string(pvc.UID))
		if err != nil {
			g.Expect(err).NotTo(HaveOccurred())
			return
		}
		currentPVC := getFunctionalPVC(pvcName)
		g.Expect(currentPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation]).ToNot(BeEmpty())
		currentPod := &corev1.Pod{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podName}, currentPod)).To(Succeed())
		g.Expect(currentPod.DeletionTimestamp).ToNot(BeNil())
	}, timeout, interval).Should(Succeed())

	state := decodeFunctionalDeletionCommit(commit)
	Expect(state.PVCName).To(Equal(pvcName))
	Expect(state.PVCUID).To(Equal(string(pvc.UID)))
	Expect(state.Node).To(Equal(nodeName))
	Expect(state.Pods).To(ConsistOf(functionalCommittedPod{Name: podName, UID: string(pod.UID)}))
	Expect(commit.Namespace).To(Equal(namespace))
	Expect(prKey.Name).NotTo(BeEmpty())
	return commit
}

func findFunctionalDeletionCommit(namespace, pvcUID string) (*corev1.ConfigMap, error) {
	commits := &corev1.ConfigMapList{}
	if err := k8sClient.List(ctx, commits, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	for i := range commits.Items {
		commit := &commits.Items[i]
		if !strings.HasPrefix(commit.Name, "podremediator-deletion-") {
			continue
		}
		var state functionalDeletionCommit
		if err := json.Unmarshal([]byte(commit.Annotations["remediation.openstack.org/deletion-commit-state"]), &state); err != nil {
			return nil, err
		}
		if state.PVCUID == pvcUID {
			return commit, nil
		}
	}
	return nil, fmt.Errorf("no deletion commit ConfigMap found for PVC UID %s", pvcUID)
}

func expectNoFunctionalDeletionCommit(g Gomega, namespace, pvcUID string) {
	_, err := findFunctionalDeletionCommit(namespace, pvcUID)
	g.Expect(err).To(MatchError(fmt.Sprintf("no deletion commit ConfigMap found for PVC UID %s", pvcUID)))
}

func decodeFunctionalDeletionCommit(commit *corev1.ConfigMap) functionalDeletionCommit {
	var state functionalDeletionCommit
	Expect(json.Unmarshal([]byte(commit.Annotations["remediation.openstack.org/deletion-commit-state"]), &state)).To(Succeed())
	return state
}

func getFunctionalPVC(name string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pvc)).To(Succeed())
	return pvc
}

func removeFunctionalPodFinalizer(name string) {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	pod := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, key, pod)).To(Succeed())
	oldPod := pod.DeepCopy()
	pod.Finalizers = nil
	Expect(k8sClient.Patch(ctx, pod, client.MergeFrom(oldPod))).To(Succeed())
}
