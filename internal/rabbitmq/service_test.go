/*
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

package rabbitmq

import (
	"testing"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClientServiceLoadBalancerClassOverride(t *testing.T) {
	lbClass := "metallb.io/metallb"

	r := &rabbitmqv1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rabbitmq",
			Namespace: "openstack",
		},
		Spec: rabbitmqv1.RabbitMqSpec{
			RabbitMqSpecCore: rabbitmqv1.RabbitMqSpecCore{
				Override: rabbitmqv1.RabbitMQOverrideSpec{
					Service: &rabbitmqv1.RabbitMQServiceOverride{
						Spec: &corev1.ServiceSpec{
							Type:              corev1.ServiceTypeLoadBalancer,
							LoadBalancerClass: &lbClass,
						},
					},
				},
			},
		},
	}

	svc := ClientService(r)

	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf(
			"expected service type %q, got %q",
			corev1.ServiceTypeLoadBalancer,
			svc.Spec.Type,
		)
	}

	// FIXME: This documents the current bug. The RabbitMq CRD accepts
	// override.service.spec.loadBalancerClass, but ClientService does not
	// propagate it to the generated Service. The fix should update this
	// assertion to require svc.Spec.LoadBalancerClass to equal lbClass.
	if svc.Spec.LoadBalancerClass != nil {
		t.Fatalf(
			"expected loadBalancerClass to be unset for current broken behavior, got %q",
			*svc.Spec.LoadBalancerClass,
		)
	}
}
