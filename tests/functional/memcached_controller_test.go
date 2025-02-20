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
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	memcachedDefaultName = "dnsmasq-0"
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

	When("a default Memcached gets created with topologyRef", func() {
		BeforeEach(func() {
			// Build the topology Spec
			spec := GetDefaultMemcachedSpec()
			memcachedName.Name = memcachedDefaultName
			memcachedName.Namespace = namespace

			memcachedTopologies = GetTopologyRef(memcachedName.Name, memcachedName.Namespace)
			spec["topologyRef"] = map[string]interface{}{
				"name": memcachedTopologies[0].Name,
			}

			memcached := CreateMemcachedConfigWithName(memcachedName.Name, namespace, spec)
			// Create Test Topologies
			topologySpec := GetSampleTopologySpec(memcachedName.Name)
			for _, t := range memcachedTopologies {
				CreateTopology(t, topologySpec)
			}
			DeferCleanup(th.DeleteInstance, memcached)
		})

		It("sets topology in CR status", func() {
			memcachedTopologies = GetTopologyRef(memcachedName.Name, memcachedName.Namespace)
			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology.Name).To(Equal(memcachedTopologies[0].Name))
			}, timeout, interval).Should(Succeed())
		})

		It("sets topology in CR deployment", func() {
			Eventually(func(g Gomega) {
				g.Expect(th.GetStatefulSet(memcachedName).Spec.Template.Spec.TopologySpreadConstraints).ToNot(BeNil())
				g.Expect(th.GetStatefulSet(memcachedName).Spec.Template.Spec.Affinity).To(BeNil())
			}, timeout, interval).Should(Succeed())
		})
		It("updates topology when the reference changes", func() {
			memcachedTopologies = GetTopologyRef(memcachedName.Name, memcachedName.Namespace)
			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				mc.Spec.TopologyRef.Name = memcachedTopologies[1].Name
				g.Expect(k8sClient.Update(ctx, mc)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology).ToNot(BeNil())
			}, timeout, interval).Should(Succeed())

			Eventually(func(g Gomega) {
				mc := GetMemcached(memcachedName)
				g.Expect(mc.Status.LastAppliedTopology.Name).To(Equal(memcachedTopologies[1].Name))
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
