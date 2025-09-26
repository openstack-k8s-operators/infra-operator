/*
Copyright 2022.

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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"

	//revive:disable-next-line:dot-imports
	. "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
	corev1 "k8s.io/api/core/v1"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"

	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("TransportURL controller", func() {
	var transportURLName types.NamespacedName
	var rabbitmqClusterName types.NamespacedName
	var transportURLSecretName types.NamespacedName

	BeforeEach(func() {
		transportURLName = types.NamespacedName{
			Name:      "foo",
			Namespace: namespace,
		}
		rabbitmqClusterName = types.NamespacedName{
			Name:      "rabbitmq",
			Namespace: namespace,
		}
		transportURLSecretName = types.NamespacedName{
			Name:      "rabbitmq-transport-url-" + transportURLName.Name,
			Namespace: namespace,
		}
	})

	When("a non TLS TransportURL gets created", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should have the Spec fields set", func() {
			tr := th.GetTransportURL(transportURLName)
			Expect(tr.Spec.RabbitmqClusterName).Should(Equal("rabbitmq"))
		})

		It("should have not ready Conditions initialized", func() {
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				condition.ReadyCondition,
				corev1.ConditionFalse,
			)
			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionFalse,
			)
		})

		It("should create a secret holding the _NON_ TLS transport url when rabbitmq cluster is ready", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5672/?ssl=0", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			tr := th.GetTransportURL(transportURLName)
			Expect(tr).ToNot(BeNil())
			Expect(tr.Status.SecretName).To(Equal(transportURLSecretName.Name))
		})
	})

	When("a TLS TransportURL gets created", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(true))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should create a secret holding the TLS transport url when rabbitmq cluster is ready", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5671/?ssl=1", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)

			tr := th.GetTransportURL(transportURLName)
			Expect(tr).ToNot(BeNil())
			Expect(tr.Status.SecretName).To(Equal(transportURLSecretName.Name))
		})
	})

	When("TLS gets enable for RabbitMQ, the TransportURL gets updated", func() {
		BeforeEach(func() {
			CreateRabbitMQCluster(rabbitmqClusterName, GetDefaultRabbitMQClusterSpec(false))
			DeferCleanup(DeleteRabbitMQCluster, rabbitmqClusterName)

			spec := map[string]any{
				"rabbitmqClusterName": rabbitmqClusterName.Name,
			}
			DeferCleanup(th.DeleteInstance, CreateTransportURL(transportURLName, spec))
		})

		It("should update the secret holding the TLS transport url", func() {
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			// validate non tls transport_url
			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5672/?ssl=0", namespace)))

			}, timeout, interval).Should(Succeed())

			// update rabbitmq to be tls
			UpdateRabbitMQClusterToTLS(rabbitmqClusterName)
			SimulateRabbitMQClusterReady(rabbitmqClusterName)

			Eventually(func(g Gomega) {
				s := th.GetSecret(transportURLSecretName)
				g.Expect(s.Data).To(HaveLen(1))
				g.Expect(s.Data).To(HaveKeyWithValue("transport_url", fmt.Appendf(nil, "rabbit://user:12345678@host.%s.svc:5671/?ssl=1", namespace)))

			}, timeout, interval).Should(Succeed())

			th.ExpectCondition(
				transportURLName,
				ConditionGetterFunc(TransportURLConditionGetter),
				rabbitmqv1.TransportURLReadyCondition,
				corev1.ConditionTrue,
			)
		})
	})
})
