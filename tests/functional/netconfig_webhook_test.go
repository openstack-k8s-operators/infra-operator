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

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
)

var _ = Describe("NetConfig webhook", func() {
	var netConfigName, ipSetName types.NamespacedName

	When("a GetDefaultNetConfigSpec NetConfig gets created", func() {
		BeforeEach(func() {
			netCfg := CreateNetConfig(namespace, GetDefaultNetConfigSpec())
			netConfigName.Name = netCfg.GetName()
			netConfigName.Namespace = netCfg.GetNamespace()
			DeferCleanup(th.DeleteInstance, netCfg)
		})

		It("should have created a NetConfig", func() {
			Eventually(func(_ Gomega) {
				GetNetConfig(netConfigName)
			}, timeout, interval).Should(Succeed())
		})

		When("a second NetConfig gets created in the same namespace", func() {
			It("gets blocked by the webhook and fail", func() {

				raw := map[string]interface{}{
					"apiVersion": "network.openstack.org/v1beta1",
					"kind":       "NetConfig",
					"metadata": map[string]interface{}{
						"name":      "foo",
						"namespace": namespace,
					},
					"spec": GetDefaultNetConfigSpec(),
				}

				unstructuredObj := &unstructured.Unstructured{Object: raw}
				_, err := controllerutil.CreateOrPatch(
					th.Ctx, th.K8sClient, unstructuredObj, func() error { return nil })
				Expect(err).To(HaveOccurred())
			})
		})

		When("a there is at least one IPSet", func() {
			BeforeEach(func() {
				ipset := CreateIPSet(netConfigName.Namespace, GetIPSetSpec(false, GetIPSetNet1WithFixedIP("172.17.0.201")))
				ipSetName = types.NamespacedName{
					Name:      ipset.GetName(),
					Namespace: namespace,
				}

				DeferCleanup(th.DeleteInstance, ipset)
			})

			It("should have created an IPSet with IP 172.17.0.201 on net-1", func() {
				Eventually(func(g Gomega) {
					res := GetReservationFromNet(ipSetName, "net-1")
					g.Expect(res).To(Equal(networkv1.IPSetReservation{}))
				}, timeout, interval).Should(Succeed())
			})

			It("should not be possible to delete the NetConfig", func() {
				netcfg := &networkv1.NetConfig{}
				Eventually(func(_ Gomega) {
					netcfg = GetNetConfig(netConfigName)
				}, timeout, interval).Should(Succeed())

				Expect(k8sClient.Delete(ctx, netcfg)).To(HaveOccurred())
			})
		})
	})
})
