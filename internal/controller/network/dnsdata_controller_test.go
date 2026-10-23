package network

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	commonenv "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	commonhelper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGenerateServiceConfigMapsFiltersInvalidDNSNames(t *testing.T) {
	const injectedName = "alias.example.com\nserver=/#/203.0.113.9"

	data, err := generateDNSDataConfigMap(t, []networkv1.DNSHost{
		{
			IP:        "192.0.2.1",
			Hostnames: []string{"host.example.com"},
			CNAMEs:    []string{"alias.example.com", injectedName},
		},
		{
			IP:        "192.0.2.2",
			Hostnames: []string{injectedName},
			CNAMEs:    []string{"other.example.com"},
		},
	})
	if !errors.Is(err, ErrInvalidDNSData) {
		t.Fatalf("expected invalid DNSData error, got %v", err)
	}
	if got, want := data["dnsdata-test"], "192.0.2.1 host.example.com\n"; got != want {
		t.Fatalf("unexpected hosts data: got %q, want %q", got, want)
	}
	if got, want := data["dnsdata-test-cnames"], "cname=alias.example.com,host.example.com\n"; got != want {
		t.Fatalf("unexpected CNAME data: got %q, want %q", got, want)
	}
}

func TestGenerateServiceConfigMapsHandlesCNAMEAliases(t *testing.T) {
	tests := []struct {
		name          string
		hosts         []networkv1.DNSHost
		wantCNAMEData string
		wantErr       bool
	}{
		{
			name: "deduplicates aliases with the same canonical target",
			hosts: []networkv1.DNSHost{
				{IP: "192.0.2.1", Hostnames: []string{"host.example.com"}, CNAMEs: []string{"alias.example.com"}},
				{IP: "192.0.2.2", Hostnames: []string{"host.example.com"}, CNAMEs: []string{"alias.example.com"}},
			},
			wantCNAMEData: "cname=alias.example.com,host.example.com\n",
		},
		{
			name: "rejects a self-referencing alias",
			hosts: []networkv1.DNSHost{
				{IP: "192.0.2.1", Hostnames: []string{"host.example.com"}, CNAMEs: []string{"host.example.com"}},
			},
			wantErr: true,
		},
		{
			name: "rejects an alias with conflicting canonical targets",
			hosts: []networkv1.DNSHost{
				{IP: "192.0.2.1", Hostnames: []string{"host1.example.com"}, CNAMEs: []string{"alias.example.com"}},
				{IP: "192.0.2.2", Hostnames: []string{"host2.example.com"}, CNAMEs: []string{"alias.example.com"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := generateDNSDataConfigMap(t, tt.hosts)
			if tt.wantErr && !errors.Is(err, ErrInvalidDNSData) {
				t.Fatalf("expected invalid DNSData error, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got, want := data["dnsdata-test-cnames"], tt.wantCNAMEData; got != want {
				t.Fatalf("unexpected CNAME data: got %q, want %q", got, want)
			}
		})
	}
}

func generateDNSDataConfigMap(t *testing.T, hosts []networkv1.DNSHost) (map[string]string, error) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := networkv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	instance := &networkv1.DNSData{
		ObjectMeta: metav1.ObjectMeta{Name: "dnsdata-test", Namespace: "default"},
		Spec:       networkv1.DNSDataSpec{Hosts: hosts},
	}

	h, err := commonhelper.NewHelper(instance, client, kubernetesfake.NewSimpleClientset(), scheme, logr.Discard())
	if err != nil {
		return nil, err
	}

	envVars := make(map[string]commonenv.Setter)
	reconciler := &DNSDataReconciler{Client: client, Scheme: scheme}
	err = reconciler.generateServiceConfigMaps(context.Background(), h, instance, &envVars)

	configMap := &corev1.ConfigMap{}
	if getErr := client.Get(context.Background(), types.NamespacedName{Name: "dnsdata-test", Namespace: "default"}, configMap); getErr != nil {
		return nil, getErr
	}
	return configMap.Data, err
}
