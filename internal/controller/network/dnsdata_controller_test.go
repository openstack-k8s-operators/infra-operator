package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
	commonconfigmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	commonenv "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	commonhelper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	commonutil "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDNSDataPublishesOnlyCompleteCNAMEShardGenerations(t *testing.T) {
	oldBudget := dnsmasq.CNAMEConfigMapMaxBytes
	dnsmasq.CNAMEConfigMapMaxBytes = 8 * 1024
	t.Cleanup(func() { dnsmasq.CNAMEConfigMapMaxBytes = oldBudget })
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := networkv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	instance := &networkv1.DNSData{
		ObjectMeta: metav1.ObjectMeta{Name: "dnsdata-test", Namespace: "default", UID: types.UID("test-uid")},
		Spec: networkv1.DNSDataSpec{Hosts: []networkv1.DNSHost{{
			IP: "192.0.2.1", Hostnames: []string{"host.example.com"}, CNAMEs: []string{"old.example.com"},
		}}},
	}
	client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	h, err := commonhelper.NewHelper(instance, client, kubernetesfake.NewSimpleClientset(), scheme, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	r := &DNSDataReconciler{Client: client, Scheme: scheme}
	envVars := map[string]commonenv.Setter{}
	if err := r.generateServiceConfigMaps(ctx, h, instance, &envVars); err != nil {
		t.Fatal(err)
	}
	host := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := client.Get(ctx, key, host); err != nil {
		t.Fatal(err)
	}
	oldPointer := host.Data[dnsmasq.CNAMEShardPointerKey]

	longDomain := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60)
	aliases := make([]string, 40)
	var directives strings.Builder
	for i := range aliases {
		aliases[i] = fmt.Sprintf("alias-%04d.%s", i, longDomain)
		fmt.Fprintf(&directives, "cname=%s,%s\n", aliases[i], longDomain)
	}
	instance.Spec.Hosts = []networkv1.DNSHost{{IP: "192.0.2.1", Hostnames: []string{longDomain}, CNAMEs: aliases}}
	generation, err := commonutil.ObjectHash(directives.String())
	if err != nil {
		t.Fatal(err)
	}
	blockedName := dnsmasq.DNSDataCNAMEShardName(string(instance.UID), generation, 1)
	if err := client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: blockedName, Namespace: instance.Namespace}}); err != nil {
		t.Fatal(err)
	}
	if err := r.generateServiceConfigMaps(ctx, h, instance, &envVars); !errors.Is(err, commonconfigmap.ErrExistingConfigMapNotOwned) {
		t.Fatalf("expected second-shard ownership conflict, got %v", err)
	}
	if err := client.Get(ctx, key, host); err != nil {
		t.Fatal(err)
	}
	if host.Data[dnsmasq.CNAMEShardPointerKey] != oldPointer {
		t.Fatalf("published a partial generation: got %q, want %q", host.Data[dnsmasq.CNAMEShardPointerKey], oldPointer)
	}
	staged := &corev1.ConfigMap{}
	if err := client.Get(ctx, types.NamespacedName{Name: dnsmasq.DNSDataCNAMEShardName(string(instance.UID), generation, 0), Namespace: instance.Namespace}, staged); err != nil {
		t.Fatal(err)
	}
	if _, selected := staged.Labels[networkv1.DNSDataLabelSelectorKey]; selected {
		t.Fatal("unpublished CNAME shard is visible to DNSMasq")
	}
}

func TestGenerateServiceConfigMapsFiltersInvalidDNSNames(t *testing.T) {
	const injectedName = "alias.example.com\nserver=/#/203.0.113.9"

	hostData, cnameData, err := generateDNSDataConfigMaps(t, "dnsdata-test", []networkv1.DNSHost{
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
	if got, want := hostData["dnsdata-test"], "192.0.2.1 host.example.com\n"; got != want {
		t.Fatalf("unexpected hosts data: got %q, want %q", got, want)
	}
	if _, exists := hostData[dnsmasq.CNAMEConfigMapKey]; exists {
		t.Fatal("host ConfigMap still contains CNAME data")
	}
	if got, want := cnameData[dnsmasq.CNAMEConfigMapKey], "cname=alias.example.com,host.example.com\n"; got != want {
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
			_, cnameData, err := generateDNSDataConfigMaps(t, "dnsdata-test", tt.hosts)
			if tt.wantErr && !errors.Is(err, ErrInvalidDNSData) {
				t.Fatalf("expected invalid DNSData error, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got, want := cnameData[dnsmasq.CNAMEConfigMapKey], tt.wantCNAMEData; got != want {
				t.Fatalf("unexpected CNAME data: got %q, want %q", got, want)
			}
		})
	}
}

func TestGenerateServiceConfigMapsUsesFixedCNAMEKeyForLongNames(t *testing.T) {
	label := strings.Repeat("a", 61)
	name := strings.Join([]string{label, label, label, label}, ".")

	_, cnameData, err := generateDNSDataConfigMaps(t, name, []networkv1.DNSHost{{
		IP:        "192.0.2.1",
		Hostnames: []string{"host.example.com"},
		CNAMEs:    []string{"alias.example.com"},
	}})
	if err != nil {
		t.Fatalf("unexpected error for valid long DNSData name: %v", err)
	}
	if len(name+"-src-cnames-0") <= 253 {
		t.Fatalf("test name is not long enough to exceed the ConfigMap name limit: %d", len(name+"-src-cnames-0"))
	}
	if len(dnsmasq.DNSDataCNAMEShardName("test-uid", strings.Repeat("a", 64), 0)) > 253 {
		t.Fatal("generated CNAME source ConfigMap name exceeds the Kubernetes limit")
	}
	if got, want := cnameData[dnsmasq.CNAMEConfigMapKey], "cname=alias.example.com,host.example.com\n"; got != want {
		t.Fatalf("unexpected CNAME data: got %q, want %q", got, want)
	}
}

func generateDNSDataConfigMaps(t *testing.T, name string, hosts []networkv1.DNSHost) (map[string]string, map[string]string, error) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := networkv1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}

	client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	instance := &networkv1.DNSData{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("test-uid")},
		Spec:       networkv1.DNSDataSpec{Hosts: hosts},
	}

	h, err := commonhelper.NewHelper(instance, client, kubernetesfake.NewSimpleClientset(), scheme, logr.Discard())
	if err != nil {
		return nil, nil, err
	}

	envVars := make(map[string]commonenv.Setter)
	reconciler := &DNSDataReconciler{Client: client, Scheme: scheme}
	err = reconciler.generateServiceConfigMaps(context.Background(), h, instance, &envVars)

	configMap := &corev1.ConfigMap{}
	if getErr := client.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, configMap); getErr != nil {
		return nil, nil, getErr
	}
	cnameMap := &corev1.ConfigMap{}
	shardNames := strings.Split(configMap.Data[dnsmasq.CNAMEShardPointerKey], ",")
	if shardNames[0] == "" {
		return configMap.Data, nil, err
	}
	getErr := client.Get(context.Background(), types.NamespacedName{Name: shardNames[0], Namespace: "default"}, cnameMap)
	if k8s_errors.IsNotFound(getErr) {
		return configMap.Data, nil, err
	}
	if getErr != nil {
		return nil, nil, getErr
	}
	return configMap.Data, cnameMap.Data, err
}
