package network

import (
	"context"
	"slices"
	"testing"

	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetServiceDNSDataKeepsIngressCNAMEsIndependent(t *testing.T) {
	const namespace = "test"
	want := []string{"custom.example.com", "public.example.com"}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: namespace,
			Annotations: map[string]string{
				service.AnnotationHostnameKey: "test.example.com",
				service.AnnotationCNAMEsKey:   "custom.example.com,public.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{
				{IP: "172.20.0.80"},
				{IP: "172.20.0.81"},
			}},
		},
	}
	r := &ServiceReconciler{Client: clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()}

	hosts, err := r.getServiceDNSData(context.Background(), namespace)
	if err != nil {
		t.Fatal(err)
	}

	first := hosts["172.20.0.80"]
	second := hosts["172.20.0.81"]
	if !slices.Equal(first.CNAMEs, want) || !slices.Equal(second.CNAMEs, want) {
		t.Fatalf("unexpected initial CNAMEs: first=%v second=%v", first.CNAMEs, second.CNAMEs)
	}

	first.CNAMEs[0] = "changed.example.com"
	if !slices.Equal(second.CNAMEs, want) {
		t.Fatalf("updating one ingress IP changed another IP's CNAMEs: got %v, want %v", second.CNAMEs, want)
	}
}
