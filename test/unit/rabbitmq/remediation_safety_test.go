/*
Copyright 2026.

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

package rabbitmq_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	rabbitmqcontroller "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newConsentFixture supplies a healthy three-broker cluster with no queues, so
// only the candidate's identity and management TLS settings can block consent.
func newConsentFixture(t *testing.T, pvcName string) (*rabbitmqcontroller.Reconciler, *rabbitmqv1beta1.RabbitMq, *helper.Helper) {
	t.Helper()
	instance := instanceWith(3)
	instance.Status.ReadyCount = 3
	pvc := replacementPVC(oldPVCUID)
	pvc.Name = pvcName
	pvc.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
	pvc.Annotations = map[string]string{
		remediationv1.PVCStuckOnNodeAnnotation: "worker-0",
		remediationv1.RequestIDAnnotation:      testRequestID,
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, rabbitmqv1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{remediationv1.GroupVersion})
	mapper.Add(remediationv1.GroupVersion.WithKind("PodRemediator"), meta.RESTScopeNamespace)
	objects := []client.Object{instance, pvc, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testName + "-default-user", Namespace: testNS},
		Data:       map[string][]byte{"username": []byte("operator"), "password": []byte("test-password")},
	}}
	for ordinal := 0; ordinal < 3; ordinal++ {
		pod := readyRabbitMQPod(fmt.Sprintf("%s-server-%d", testName, ordinal), fmt.Sprintf("worker-%d", ordinal))
		pod.Namespace = testNS
		pod.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
		objects = append(objects, &pod)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).
		WithStatusSubresource(&rabbitmqv1beta1.RabbitMq{}).WithObjects(objects...).Build()
	r := &rabbitmqcontroller.Reconciler{Client: c, Scheme: scheme,
		ManagementHTTPClient: &http.Client{Transport: rabbitMQManagementTransport(func(req *http.Request) (*http.Response, error) {
			body := `{"page":1,"page_count":0,"page_size":500,"item_count":0,"filtered_count":0,"items":[]}`
			if req.URL.Path == "/api/nodes" {
				body = `[{"name":"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","running":true},{"name":"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","running":true},{"name":"rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack","running":true}]`
			} else if req.URL.Path != "/api/queues/detailed" {
				return nil, fmt.Errorf("unexpected management endpoint %s", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
		})},
	}
	h, err := helper.NewHelper(instance, c, nil, scheme, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	return r, instance, h
}

func TestPVCRemediationConsentRequiresClusterClaimName(t *testing.T) {
	cases := []struct {
		name        string
		wantConsent bool
	}{
		{"persistence-rabbitmq-server-0", true},
		{"unrelated-0", false},
		{"data-rabbitmq-server-0", false},
		{"persistence-other-server-0", false},
		{"persistence-rabbitmq-server-00", false},
		{"persistence-rabbitmq-server--1", false},
		{"persistence-rabbitmq-server-0-extra-0", false},
		{"persistence-rabbitmq-server-99999999999999999999", false},
		{"persistence-rabbitmq-server-x", false},
		{"persistence-rabbitmq-server-", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, instance, h := newConsentFixture(t, tc.name)
			if err := r.CheckForStuckPVCRequiringRemediation(context.Background(), instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true}); err != nil {
				t.Fatal(err)
			}
			pvc := &corev1.PersistentVolumeClaim{}
			if err := r.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: tc.name}, pvc); err != nil {
				t.Fatal(err)
			}
			if got := remediationv1.HasRemediationConsent(pvc.Annotations); got != tc.wantConsent {
				t.Errorf("deletion consent = %v, want %v", got, tc.wantConsent)
			}
			if !tc.wantConsent && len(instance.Status.PVCRemediation) != 0 {
				t.Errorf("unrelated or malformed claim entered remediation status: %+v", instance.Status.PVCRemediation)
			}
		})
	}
}

func TestPVCRemediationRejectsEmptyConfiguredCA(t *testing.T) {
	for _, caName := range []string{"server-tls", "separate-ca"} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ca-key-present=%t", caName, present), func(t *testing.T) {
				r, instance, h := newConsentFixture(t, testPVCName)
				instance.Spec.TLS = rabbitmqv1beta1.RabbitMQTLSSpec{SecretName: "server-tls", CaSecretName: caName}
				data := map[string][]byte{}
				if present {
					data["ca.crt"] = []byte{}
				}
				if err := r.Create(context.Background(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: caName, Namespace: testNS}, Data: data,
				}); err != nil {
					t.Fatal(err)
				}
				// Exercise the production TLS client, with a short deadline to bound
				// any network request made if CA validation regresses.
				r.ManagementHTTPClient = nil
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true})
				if err == nil || !strings.Contains(err.Error(), "has no ca.crt") {
					t.Fatalf("expected configured CA validation failure, got %v", err)
				}
				pvc := &corev1.PersistentVolumeClaim{}
				if err := r.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: testPVCName}, pvc); err != nil {
					t.Fatal(err)
				}
				if remediationv1.HasRemediationConsent(pvc.Annotations) {
					t.Fatal("empty CA allowed deletion consent")
				}
			})
		}
	}
}
