package network

import (
	"context"
	"strings"
	"testing"

	"github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublishedCNAMESourceShards(t *testing.T) {
	isController := true
	ownerRef := func(uid string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "network.openstack.org/v1beta1", Kind: "DNSData", Name: "d", UID: types.UID(uid), Controller: &isController}}
	}
	host := func(pointer string) corev1.ConfigMap {
		return corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "host", Namespace: "ns", OwnerReferences: ownerRef("uid")},
			Data:       map[string]string{dnsmasq.CNAMEShardPointerKey: pointer},
		}
	}
	shard := func(name, uid, data string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", OwnerReferences: ownerRef(uid), Labels: map[string]string{dnsmasq.DNSDataCNAMEShardLabel: uid}},
			Data:       map[string]string{dnsmasq.CNAMEConfigMapKey: data},
		}
	}
	tests := []struct {
		name     string
		pointer  string
		existing []*corev1.ConfigMap
		want     int
		wantErr  string
		notFound bool
	}{
		{name: "no pointer", pointer: ""},
		{name: "valid", pointer: "s0,s1", existing: []*corev1.ConfigMap{shard("s0", "uid", "cname=a,b\n"), shard("s1", "uid", "cname=c,d\n")}, want: 2},
		{name: "missing shard", pointer: "s0", wantErr: "unavailable", notFound: true},
		{name: "empty name", pointer: "s0,", existing: []*corev1.ConfigMap{shard("s0", "uid", "cname=a,b\n")}, wantErr: "empty CNAME source shard name"},
		{name: "foreign owner", pointer: "s0", existing: []*corev1.ConfigMap{shard("s0", "other", "cname=a,b\n")}, wantErr: "does not belong"},
		{name: "no data", pointer: "s0", existing: []*corev1.ConfigMap{shard("s0", "uid", "")}, wantErr: "no CNAME data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []runtime.Object{}
			for _, cm := range tt.existing {
				objs = append(objs, cm)
			}
			c := clientfake.NewClientBuilder().WithRuntimeObjects(objs...).Build()
			r := &DNSMasqReconciler{Client: c}
			got, err := r.publishedCNAMESourceShards(context.Background(), "ns", []corev1.ConfigMap{host(tt.pointer)})
			if tt.wantErr == "" {
				if err != nil || len(got) != tt.want {
					t.Fatalf("got %d shards, err %v; want %d", len(got), err, tt.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got err %v, want %q", err, tt.wantErr)
			}
			if k8s_errors.IsNotFound(err) != tt.notFound {
				t.Fatalf("IsNotFound = %v, want %v", k8s_errors.IsNotFound(err), tt.notFound)
			}
		})
	}
}

func TestHostConfigMapHashIgnoresCNAMEInputs(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "host", Namespace: "ns"}, Data: map[string]string{"host": "192.0.2.1 a\n"}}
	base, err := hostConfigMapHash(cm)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data[dnsmasq.CNAMEShardPointerKey] = "s0"
	cm.Data[dnsmasq.CNAMEConfigMapKey] = "cname=a,b\n"
	if got, _ := hostConfigMapHash(cm); got != base {
		t.Fatal("hash changed when only CNAME inputs changed")
	}
	cm.Data["host"] = "192.0.2.2 a\n"
	if got, _ := hostConfigMapHash(cm); got == base {
		t.Fatal("hash did not change when host records changed")
	}
}
