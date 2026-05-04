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
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
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
})
