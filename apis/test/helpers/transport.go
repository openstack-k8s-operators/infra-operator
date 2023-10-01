/*
Copyright 2023 Red Hat
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

package helpers

import (
	"github.com/onsi/gomega"
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// GetTransportURL retrieves a TransportURL resource with the specified name.
//
// Example usage:
//
//	th.GetTransportURL(types.NamespacedName{Name: "test-transporturl", Namespace: "test-namespace"})
func (tc *TestHelper) GetTransportURL(name types.NamespacedName) *rabbitmqv1.TransportURL {
	instance := &rabbitmqv1.TransportURL{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(tc.K8sClient.Get(tc.Ctx, name, instance)).Should(gomega.Succeed())
	}, tc.Timeout, tc.Interval).Should(gomega.Succeed())
	return instance
}

// SimulateTransportURLReady function retrieves the TransportURL and
// simulates the readiness of a TransportURL resource.
//
// Example usage:
//
//	th.SimulateTransportURLReady(types.NamespacedName{Name: "test-transporturl", Namespace: "test-namespace"})
func (tc *TestHelper) SimulateTransportURLReady(name types.NamespacedName) {
	gomega.Eventually(func(g gomega.Gomega) {
		transport := tc.GetTransportURL(name)
		transport.Status.SecretName = transport.Spec.RabbitmqClusterName + "-secret"
		transport.Status.Conditions.MarkTrue("TransportURLReady", "Ready")
		g.Expect(tc.K8sClient.Status().Update(tc.Ctx, transport)).To(gomega.Succeed())

	}, tc.Timeout, tc.Interval).Should(gomega.Succeed())
	tc.Logger.Info("Simulated TransportURL ready", "on", name)
}

// AssertTransportURLDoesNotExist ensures the TransportURL resource does not exist in a k8s cluster.
func (tc *TestHelper) AssertTransportURLDoesNotExist(name types.NamespacedName) {
	instance := &rabbitmqv1.TransportURL{}
	gomega.Eventually(func(g gomega.Gomega) {
		err := tc.K8sClient.Get(tc.Ctx, name, instance)
		g.Expect(k8s_errors.IsNotFound(err)).To(gomega.BeTrue())
	}, tc.Timeout, tc.Interval).Should(gomega.Succeed())
}
