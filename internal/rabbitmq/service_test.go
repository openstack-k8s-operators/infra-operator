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
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func baseRabbitMq() *rabbitmqv1.RabbitMq {
	return &rabbitmqv1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rabbitmq",
			Namespace: "openstack",
		},
	}
}

func TestClientServiceNoOverride(t *testing.T) {
	r := baseRabbitMq()

	svc, err := ClientService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("expected type %q, got %q", corev1.ServiceTypeClusterIP, svc.Spec.Type)
	}
	if svc.Spec.LoadBalancerClass != nil {
		t.Fatalf("expected nil loadBalancerClass, got %q", *svc.Spec.LoadBalancerClass)
	}
	if svc.Spec.IPFamilyPolicy != nil {
		t.Fatalf("expected nil IPFamilyPolicy, got %v", *svc.Spec.IPFamilyPolicy)
	}
}

func TestClientServiceLoadBalancerClassOverride(t *testing.T) {
	lbClass := "metallb.io/metallb"

	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			Spec: &corev1.ServiceSpec{
				Type:              corev1.ServiceTypeLoadBalancer,
				LoadBalancerClass: &lbClass,
			},
		},
	}

	svc, err := ClientService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("expected service type %q, got %q", corev1.ServiceTypeLoadBalancer, svc.Spec.Type)
	}
	if svc.Spec.LoadBalancerClass == nil {
		t.Fatal("expected loadBalancerClass override to be propagated, got nil")
	}
	if *svc.Spec.LoadBalancerClass != lbClass {
		t.Fatalf("expected loadBalancerClass %q, got %q", lbClass, *svc.Spec.LoadBalancerClass)
	}

	hostname, ok := svc.Annotations["dnsmasq.network.openstack.org/hostname"]
	if !ok {
		t.Fatal("expected dnsmasq hostname annotation for LoadBalancer service")
	}
	if hostname != "rabbitmq.openstack.svc" {
		t.Fatalf("expected hostname %q, got %q", "rabbitmq.openstack.svc", hostname)
	}
}

func TestClientServiceMultipleOverrides(t *testing.T) {
	lbClass := "metallb.io/metallb"
	ipfp := corev1.IPFamilyPolicySingleStack

	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			Spec: &corev1.ServiceSpec{
				Type:                  corev1.ServiceTypeLoadBalancer,
				LoadBalancerClass:     &lbClass,
				IPFamilyPolicy:        &ipfp,
				ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
				SessionAffinity:       corev1.ServiceAffinityClientIP,
			},
		},
	}

	svc, err := ClientService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("expected type %q, got %q", corev1.ServiceTypeLoadBalancer, svc.Spec.Type)
	}
	if svc.Spec.LoadBalancerClass == nil || *svc.Spec.LoadBalancerClass != lbClass {
		t.Fatalf("expected loadBalancerClass %q", lbClass)
	}
	if svc.Spec.IPFamilyPolicy == nil || *svc.Spec.IPFamilyPolicy != ipfp {
		t.Fatalf("expected IPFamilyPolicy %q", ipfp)
	}
	if svc.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal {
		t.Fatalf("expected ExternalTrafficPolicy %q, got %q", corev1.ServiceExternalTrafficPolicyLocal, svc.Spec.ExternalTrafficPolicy)
	}
	if svc.Spec.SessionAffinity != corev1.ServiceAffinityClientIP {
		t.Fatalf("expected SessionAffinity %q, got %q", corev1.ServiceAffinityClientIP, svc.Spec.SessionAffinity)
	}
}

func TestClientServiceMetadataOverride(t *testing.T) {
	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			EmbeddedLabelsAnnotations: &service.EmbeddedLabelsAnnotations{
				Labels:      map[string]string{"custom-label": "value"},
				Annotations: map[string]string{"custom-annotation": "value"},
			},
		},
	}

	svc, err := ClientService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if svc.Labels["custom-label"] != "value" {
		t.Fatalf("expected custom-label in labels, got %v", svc.Labels)
	}
	if svc.Annotations["custom-annotation"] != "value" {
		t.Fatalf("expected custom-annotation in annotations, got %v", svc.Annotations)
	}
}

func TestClientServiceUnsafeFieldsIgnored(t *testing.T) {
	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			Spec: &corev1.ServiceSpec{
				Selector:  map[string]string{"should": "be-ignored"},
				ClusterIP: "10.0.0.99",
			},
		},
	}

	svc, err := ClientService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if svc.Spec.ClusterIP == "10.0.0.99" {
		t.Fatal("override should not be able to set ClusterIP")
	}
	if svc.Spec.Selector["should"] == "be-ignored" {
		t.Fatal("override should not be able to set Selector")
	}
}

func TestHeadlessServiceNoOverride(t *testing.T) {
	r := baseRabbitMq()

	svc, err := HeadlessService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.Spec.ClusterIP != "None" {
		t.Fatalf("expected headless ClusterIP 'None', got %q", svc.Spec.ClusterIP)
	}
	if svc.Name != "rabbitmq-nodes" {
		t.Fatalf("expected name %q, got %q", "rabbitmq-nodes", svc.Name)
	}
}

func TestHeadlessServiceIgnoresClientOverrides(t *testing.T) {
	lbClass := "metallb.io/metallb"
	ipfp := corev1.IPFamilyPolicyPreferDualStack

	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			EmbeddedLabelsAnnotations: &service.EmbeddedLabelsAnnotations{
				Labels: map[string]string{"custom": "label"},
			},
			Spec: &corev1.ServiceSpec{
				Type:                  corev1.ServiceTypeLoadBalancer,
				LoadBalancerClass:     &lbClass,
				ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
				IPFamilyPolicy:        &ipfp,
			},
		},
	}

	svc, err := HeadlessService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("headless service type must stay ClusterIP, got %q", svc.Spec.Type)
	}
	if svc.Spec.ClusterIP != "None" {
		t.Fatalf("headless service clusterIP must stay None, got %q", svc.Spec.ClusterIP)
	}
	if svc.Spec.ExternalTrafficPolicy != "" {
		t.Fatalf("headless service must not have ExternalTrafficPolicy, got %q", svc.Spec.ExternalTrafficPolicy)
	}
	if svc.Spec.LoadBalancerClass != nil {
		t.Fatalf("headless service must not have LoadBalancerClass, got %q", *svc.Spec.LoadBalancerClass)
	}
	// IPFamilyPolicy IS applied to headless services
	if svc.Spec.IPFamilyPolicy == nil || *svc.Spec.IPFamilyPolicy != ipfp {
		t.Fatalf("expected IPFamilyPolicy %q on headless service", ipfp)
	}
	// Metadata overrides ARE applied to headless services
	if svc.Labels["custom"] != "label" {
		t.Fatalf("expected metadata override on headless service, got labels %v", svc.Labels)
	}
}

func TestHeadlessServiceIPFamilyPolicyOverride(t *testing.T) {
	ipfp := corev1.IPFamilyPolicyPreferDualStack

	r := baseRabbitMq()
	r.Spec.Override = rabbitmqv1.RabbitMQOverrideSpec{
		Service: &rabbitmqv1.RabbitMQServiceOverride{
			Spec: &corev1.ServiceSpec{
				IPFamilyPolicy: &ipfp,
			},
		},
	}

	svc, err := HeadlessService(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.Spec.IPFamilyPolicy == nil || *svc.Spec.IPFamilyPolicy != ipfp {
		t.Fatalf("expected IPFamilyPolicy %q", ipfp)
	}
}
