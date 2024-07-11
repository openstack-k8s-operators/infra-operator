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

var _ = Describe("Memcached webhook", func() {
	var memcacheName types.NamespacedName

	When("a default Memcache gets created", func() {
		BeforeEach(func() {
			memcache := CreateMemcachedConfig(namespace, GetDefaultMemcachedSpec())
			memcacheName.Name = memcache.GetName()
			memcacheName.Namespace = memcache.GetNamespace()
			DeferCleanup(th.DeleteInstance, memcache)
		})

		It("should have created a Memcache", func() {
			Eventually(func(_ Gomega) {
				GetMemcached(memcacheName)
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a Memcache gets created with a name longer then 52 chars", func() {
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
})
