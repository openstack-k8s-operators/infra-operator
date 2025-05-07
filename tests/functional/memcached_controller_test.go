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

package functional_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	//revive:disable-next-line:dot-imports
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	memcachedDefaultName = "memcached-0"
)

var _ = Describe("Memcached Controller", func() {
	var memcachedName types.NamespacedName
	var memcachedTopologies []types.NamespacedName

	When("a default Memcached gets created", func() {
		BeforeEach(func() {
			memcached := CreateMemcachedConfig(namespace, GetDefaultMemcachedSpec())
			memcachedName.Name = memcached.GetName()
			memcachedName.Namespace = memcached.GetNamespace()
			DeferCleanup(th.DeleteInstance, memcached)
		})

		It("should have created a Memcached", func() {
			Eventually(func(_ Gomega) {
				GetMemcached(memcachedName)
			}, timeout, interval).Should(Succeed())
		})
	})

	When("Deployment rollout is progressing", func() {
		BeforeEach(func() {
			memcached := CreateMemcachedConfig(namespace, GetDefaultMemcachedSpec())
			memcachedName.Name = memcached.GetName()
			memcachedName.Namespace = memcached.GetNamespace()
			DeferCleanup(th.DeleteInstance, memcached)

			th.SimulateStatefulSetProgressing(memcachedName)
		})

		It("shows the deployment progressing in DeploymentReadyCondition", func() {
			th.ExpectConditionWithDetails(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)

			th.ExpectCondition(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("reaches Ready when deployment rollout finished", func() {
			th.ExpectConditionWithDetails(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
				condition.RequestedReason,
				condition.DeploymentReadyRunningMessage,
			)

			th.ExpectCondition(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionFalse,
			)

			th.SimulateStatefulSetReplicaReady(memcachedName)

			th.ExpectCondition(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionTrue,
			)

			th.ExpectCondition(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.DeploymentReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})

	When("a default Memcached gets created with topologyRef", func() {
		var topologyRef, topologyRefAlt *topologyv1.TopoRef
		BeforeEach(func() {
			// Build the topology Spec
			spec := GetDefaultMemcachedSpec()
			memcachedName.Name = memcachedDefaultName
			memcachedName.Namespace = namespace

			memcachedTopologies = GetTopologyRef(memcachedName.Name, namespace)
			// Define the two topology references used in this test
			topologyRef = &topologyv1.TopoRef{
				Name:      memcachedTopologies[0].Name,
				Namespace: namespace,
			}
			topologyRefAlt = &topologyv1.TopoRef{
				Name:      memcachedTopologies[1].Name,
				Namespace: namespace,
			}
			//memcachedTopologies = GetTopologyRef(memcachefName.Name, memcachedName.Namespace)
			spec["topologyRef"] = map[string]interface{}{
				"name": topologyRef.Name,
			}
			memcached := CreateMemcachedConfigWithName(memcachedName.Name, namespace, spec)
			// Create Test Topologies
			for _, t := range memcachedTopologies {
				// Build topologySpec
				topologySpec, _ := GetSampleTopologySpec(memcachedName.Name)
				CreateTopology(t, topologySpec)
			}
			DeferCleanup(th.DeleteInstance, memcached)
		})

		It("sets topology in CR status", func() {
			Eventually(func(g Gomega) {
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRef.Name,
					Namespace: topologyRef.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(HaveLen(1))
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology).ToNot(BeNil())
				g.Expect(mc.Status.LastAppliedTopology).To(Equal(topologyRef))
				g.Expect(finalizers).To(ContainElement(
					fmt.Sprintf("openstack.org/memcached-%s", memcachedName.Name)))
			}, timeout, interval).Should(Succeed())
		})

		It("sets topology in CR deployment", func() {
			Eventually(func(g Gomega) {
				_, topologySpecObj := GetSampleTopologySpec(memcachedName.Name)
				g.Expect(th.GetStatefulSet(memcachedName).Spec.Template.Spec.TopologySpreadConstraints).ToNot(BeNil())
				g.Expect(th.GetStatefulSet(memcachedName).Spec.Template.Spec.TopologySpreadConstraints).To(Equal(topologySpecObj))
				g.Expect(th.GetStatefulSet(memcachedName).Spec.Template.Spec.Affinity).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})
		It("updates topology when the reference changes", func() {
			memcachedTopologies = GetTopologyRef(memcachedName.Name, memcachedName.Namespace)
			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				mc.Spec.TopologyRef.Name = topologyRefAlt.Name
				g.Expect(k8sClient.Update(ctx, mc)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRefAlt.Name,
					Namespace: topologyRefAlt.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(HaveLen(1))
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology).ToNot(BeNil())
				g.Expect(mc.Status.LastAppliedTopology).To(Equal(topologyRefAlt))
				g.Expect(finalizers).To(ContainElement(
					fmt.Sprintf("openstack.org/memcached-%s", memcachedName.Name)))
			}, timeout, interval).Should(Succeed())
		})
		It("checks the memcached topology Condition", func() {
			th.ExpectCondition(
				memcachedName,
				ConditionGetterFunc(MemcachedConditionGetter),
				condition.TopologyReadyCondition,
				corev1.ConditionTrue,
			)
		})
		It("checks the previous topology has no reference anymore", func() {
			Eventually(func(g Gomega) {
				// Verify the previous referenced topology has no finalizers
				tp := GetTopology(types.NamespacedName{
					Name:      topologyRef.Name,
					Namespace: topologyRef.Namespace,
				})
				finalizers := tp.GetFinalizers()
				g.Expect(finalizers).To(BeEmpty())
			}, timeout, interval).Should(Succeed())
		})
		It("removes topologyRef from the spec", func() {
			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				// Remove the TopologyRef from the existing Memecached .Spec
				mc.Spec.TopologyRef = nil
				g.Expect(k8sClient.Update(ctx, mc)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology).Should(BeNil())
			}, timeout, interval).Should(Succeed())

			// Verify the existing topologies have no finalizer anymore
			Eventually(func(g Gomega) {
				for _, topology := range memcachedTopologies {
					tp := GetTopology(types.NamespacedName{
						Name:      topology.Name,
						Namespace: topology.Namespace,
					})
					finalizers := tp.GetFinalizers()
					g.Expect(finalizers).To(BeEmpty())
				}
			}, timeout, interval).Should(Succeed())
		})
	})
	When("a Memcached gets created with a name longer then 52 chars", func() {
		It("gets blocked by the webhook and fail", func() {

			raw := map[string]interface{}{
				"apiVersion": "memcached.openstack.org/v1beta1",
				"kind":       "Memcached",
				"metadata": map[string]interface{}{
					"name":      "foo-1234567890-1234567890-1234567890-1234567890-1234567890",
					"namespace": namespace,
				},
				"spec": GetDefaultMemcachedSpec(),
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			_, err := controllerutil.CreateOrPatch(
				th.Ctx, th.K8sClient, unstructuredObj, func() error { return nil })
			Expect(err).To(HaveOccurred())
		})
	})

	When("a Memcached gets created with topologyRef", func() {
		It("gets blocked by the webhook and fail", func() {

			spec := GetDefaultMemcachedSpec()
			// Reference a top-level topology
			spec["topologyRef"] = map[string]interface{}{
				"name":      "foo",
				"namespace": "bar",
			}
			raw := map[string]interface{}{
				"apiVersion": "memcached.openstack.org/v1beta1",
				"kind":       "Memcached",
				"metadata": map[string]interface{}{
					"name":      memcachedDefaultName,
					"namespace": namespace,
				},
				"spec": spec,
			}

			unstructuredObj := &unstructured.Unstructured{Object: raw}
			_, err := controllerutil.CreateOrPatch(
				th.Ctx, th.K8sClient, unstructuredObj, func() error { return nil })
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(
				ContainSubstring(
					"Invalid value: \"namespace\": Customizing namespace field is not supported"),
			)
		})
	})
})
