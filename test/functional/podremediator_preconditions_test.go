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
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These cases use the real API server to verify the preconditions relied on by
// controller fault-injection tests. No PodRemediator is created, so a background
// reconcile cannot race the deliberately ordered writes in these cases.
var _ = Describe("PodRemediator API preconditions", func() {
	var pvc *corev1.PersistentVolumeClaim
	BeforeEach(func() {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "preconditions-" + uuid.NewString()},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
			},
		}
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pvc))).To(Succeed()) })
	})

	It("rejects finalization from a PVC snapshot taken before consent withdrawal", func() {
		pvc.Annotations = map[string]string{
			remediationv1.RequestIDAnnotation:    "request-a",
			remediationv1.ConsentIDAnnotation:    "request-a",
			remediationv1.SafeToDeleteAnnotation: "true",
		}
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())
		observed := pvc.DeepCopy()
		finalized := pvc.DeepCopy()
		finalized.Annotations["remediation.openstack.org/pvc-deletion-commit-finalized"] = "token-a"
		delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
		delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
		Expect(k8sClient.Update(ctx, pvc)).To(Succeed())
		err := k8sClient.Patch(ctx, finalized, client.MergeFromWithOptions(observed, client.MergeFromWithOptimisticLock{}))
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "stale finalization must conflict: %v", err)
		current := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pvc), current)).To(Succeed())
		Expect(current.Annotations).NotTo(HaveKey("remediation.openstack.org/pvc-deletion-commit-finalized"))
		Expect(remediationv1.HasRemediationConsent(current.Annotations)).To(BeFalse())
		Expect(current.DeletionTimestamp).To(BeNil())
	})

	It("rejects stale resource versions and replacement UIDs for PVC and Pod deletion", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "preconditions-" + uuid.NewString()},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.invalid/fixture:latest"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed())
		})
		for _, obj := range []client.Object{pvc, pod} {
			By("rejecting a stale version for " + obj.GetName())
			uid, staleVersion := obj.GetUID(), obj.GetResourceVersion()
			obj.SetAnnotations(map[string]string{"test.example/updated": "true"})
			Expect(k8sClient.Update(ctx, obj)).To(Succeed())
			err := k8sClient.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &staleVersion}})
			Expect(apierrors.IsConflict(err)).To(BeTrue(), "stale delete must conflict: %v", err)
			By("rejecting a different object identity for " + obj.GetName())
			wrongUID := types.UID("previous-object-uid")
			err = k8sClient.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &wrongUID}})
			Expect(apierrors.IsConflict(err)).To(BeTrue(), "wrong UID must conflict: %v", err)
			current := obj.DeepCopyObject().(client.Object)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), current)).To(Succeed())
			Expect(current.GetUID()).To(Equal(uid))
			Expect(current.GetDeletionTimestamp().IsZero()).To(BeTrue())
		}
	})
})

var _ = Describe("PodRemediator poll interval validation", func() {
	It("rejects invalid intervals on create and update", func() {
		for _, field := range []string{"consentPollInterval", "periodicPollInterval"} {
			for _, value := range []string{"0s", "-1s", "999ms", "not-a-duration"} {
				By(field + "=" + value)
				obj := &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "remediation.openstack.org/v1beta1", "kind": "PodRemediator",
					"metadata": map[string]interface{}{"name": "interval-" + uuid.NewString(), "namespace": namespace},
					"spec":     map[string]interface{}{field: value, "disabled": true},
				}}
				err := k8sClient.Create(ctx, obj)
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "invalid interval must be rejected: %v", err)
			}
			valid := &remediationv1.PodRemediator{ObjectMeta: metav1.ObjectMeta{Name: "interval-" + uuid.NewString(), Namespace: namespace}, Spec: remediationv1.PodRemediatorSpec{Disabled: true}}
			Expect(k8sClient.Create(ctx, valid)).To(Succeed())
			DeferCleanup(th.DeleteInstance, valid)
			// Use a patch so concurrent status/finalizer writes cannot turn a
			// schema-validation assertion into an unrelated version conflict.
			err := k8sClient.Patch(ctx, valid, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"`+field+`":"0s"}}`)))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "invalid update must be rejected: %v", err)
			Expect(k8sClient.Patch(ctx, valid, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"`+field+`":"1s"}}`)))).To(Succeed())
		}
	})
})
