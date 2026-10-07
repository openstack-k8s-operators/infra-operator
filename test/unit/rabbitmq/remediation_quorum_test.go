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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type rabbitMQManagementTransport func(*http.Request) (*http.Response, error)

func (f rabbitMQManagementTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPVCRemediationConsentChecksQueueMembership(t *testing.T) {
	const (
		namespace = "openstack"
		requestID = "request-uid"
	)
	testCases := []struct {
		name            string
		queues          string
		wantConsent     bool
		wantError       bool
		failStatusPatch bool
	}{
		{
			name:      "two-member quorum queue loses majority with candidate",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]}]}`,
			wantError: true,
		},
		{
			name:        "three-member quorum queue retains majority",
			queues:      `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"]}]}`,
			wantConsent: true,
		},
		{
			name:        "no queues retains broker quorum",
			queues:      `{"page":1,"page_count":0,"page_size":500,"item_count":0,"filtered_count":0,"items":[]}`,
			wantConsent: true,
		},
		{
			name:            "status recovery record is persisted before deletion consent",
			queues:          `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"]}]}`,
			wantError:       true,
			failStatusPatch: true,
		},
		{
			name:      "missing queue membership fails closed",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"jobs","vhost":"/","type":"quorum"}]}`,
			wantError: true,
		},
		{
			name:      "truncated queue pagination fails closed",
			queues:    `{"page":1,"page_count":2,"page_size":500,"item_count":0,"filtered_count":501,"items":[]}`,
			wantError: true,
		},
		{
			name:      "candidate stream member fails closed",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"events","vhost":"/","type":"stream","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]}]}`,
			wantError: true,
		},
		{
			name:      "candidate-hosted durable classic queue denies consent",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"legacy","vhost":"/","type":"classic","node":"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","durable":true}]}`,
			wantError: true,
		},
		{
			name:        "durable classic queue hosted by survivor permits consent",
			queues:      `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"legacy","vhost":"/","type":"classic","node":"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","durable":true}]}`,
			wantConsent: true,
		},
		{
			name:      "durable classic queue with missing host denies consent",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"legacy","vhost":"/","type":"classic","durable":true}]}`,
			wantError: true,
		},
		{
			name:      "classic queue with unknown durability denies consent",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":1,"items":[{"name":"legacy","vhost":"/","type":"classic","node":"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"}]}`,
			wantError: true,
		},
		{
			name:      "missing item count fails closed",
			queues:    `{"page":1,"page_count":0,"page_size":500,"filtered_count":0,"items":[]}`,
			wantError: true,
		},
		{
			name:      "incomplete single page fails closed",
			queues:    `{"page":1,"page_count":1,"page_size":500,"item_count":1,"filtered_count":2,"items":[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"]}]}`,
			wantError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			replicas := int32(3)
			instance := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq", Namespace: namespace},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{Replicas: &replicas},
				},
				Status: rabbitmqv1beta1.RabbitMqStatus{ReadyCount: replicas},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persistence-rabbitmq-server-0",
					Namespace: namespace,
					UID:       "pvc-uid",
					Labels:    map[string]string{"app.kubernetes.io/name": instance.Name},
					Annotations: map[string]string{
						remediationv1.PVCStuckOnNodeAnnotation: "worker-0",
						remediationv1.RequestIDAnnotation:      requestID,
					},
				},
			}
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := rabbitmqv1beta1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "remediation.openstack.org", Version: "v1beta1"}})
			mapper.Add(schema.GroupVersionKind{Group: "remediation.openstack.org", Version: "v1beta1", Kind: "PodRemediator"}, meta.RESTScopeNamespace)
			objects := []client.Object{pvc, instance, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-default-user", Namespace: namespace},
				Data:       map[string][]byte{"username": []byte("operator"), "password": []byte("test-password")},
			}}
			for ordinal := 0; ordinal < 3; ordinal++ {
				pod := readyRabbitMQPod(fmt.Sprintf("rabbitmq-server-%d", ordinal), fmt.Sprintf("worker-%d", ordinal))
				pod.Namespace = namespace
				pod.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
				objects = append(objects, &pod)
			}
			consentPatches := 0
			patchOrder := make([]string, 0, 2)
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithRESTMapper(mapper).
				WithStatusSubresource(&rabbitmqv1beta1.RabbitMq{}).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, delegate client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
						patchOrder = append(patchOrder, "pvc")
						consentPatches++
						data, err := patch.Data(obj)
						if err != nil {
							return err
						}
						var payload struct {
							Metadata struct {
								Annotations map[string]string `json:"annotations"`
							} `json:"metadata"`
						}
						if err := json.Unmarshal(data, &payload); err != nil {
							return err
						}
						if payload.Metadata.Annotations[remediationv1.SafeToDeleteAnnotation] != "true" ||
							payload.Metadata.Annotations[remediationv1.ConsentIDAnnotation] != requestID {
							t.Errorf("consent patch did not set safe-to-delete and consent-id together: %s", data)
						}
						persisted := &rabbitmqv1beta1.RabbitMq{}
						if err := delegate.Get(ctx, client.ObjectKeyFromObject(instance), persisted); err != nil {
							t.Errorf("get RabbitMq status before PVC consent: %v", err)
						} else {
							entry := persisted.Status.PVCRemediation[pvc.Name]
							if entry.ConsentState != rabbitmqv1beta1.PVCRemediationConsentGranted || entry.PVCUID != string(pvc.UID) || entry.RequestID != requestID {
								t.Errorf("recovery record was not durable before PVC consent: %+v", entry)
							}
						}
					}
					return delegate.Patch(ctx, obj, patch, opts...)
				},
				SubResourcePatch: func(ctx context.Context, delegate client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if _, ok := obj.(*rabbitmqv1beta1.RabbitMq); ok && subResourceName == "status" {
						if tc.failStatusPatch {
							patchOrder = append(patchOrder, "status-failed")
							return fmt.Errorf("injected status patch failure")
						}
						patchOrder = append(patchOrder, "status")
					}
					return delegate.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			queueRequests := 0
			transport := rabbitMQManagementTransport(func(request *http.Request) (*http.Response, error) {
				var body string
				switch request.URL.Path {
				case "/api/nodes":
					body = `[{"name":"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","running":true},{"name":"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","running":true},{"name":"rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack","running":true}]`
				case "/api/queues/detailed":
					queueRequests++
					if request.URL.Query().Get("page") != "1" || request.URL.Query().Get("page_size") != "500" ||
						!strings.Contains(request.URL.Query().Get("columns"), "node,durable") {
						return nil, fmt.Errorf("unexpected queue pagination %s", request.URL.RawQuery)
					}
					body = tc.queues
				default:
					return nil, fmt.Errorf("unexpected management endpoint %s", request.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
			})
			rabbitmqReconciler := &rabbitmqcontroller.Reconciler{Client: kubeClient, Scheme: scheme, ManagementHTTPClient: &http.Client{Transport: transport}}
			crHelper, err := helper.NewHelper(instance, kubeClient, nil, scheme, logr.Discard())
			if err != nil {
				t.Fatal(err)
			}
			err = rabbitmqReconciler.CheckForStuckPVCRequiringRemediation(context.Background(), instance, crHelper)
			if tc.wantError && err == nil {
				t.Fatal("expected queue safety failure")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected queue safety failure: %v", err)
			}
			if queueRequests == 0 {
				t.Fatal("queue membership was not queried")
			}
			if tc.wantConsent && consentPatches != 1 || !tc.wantConsent && consentPatches != 0 {
				t.Fatalf("consent patch count = %d, want consent %v", consentPatches, tc.wantConsent)
			}
			if tc.wantConsent && (len(patchOrder) != 2 || patchOrder[0] != "status" || patchOrder[1] != "pvc") {
				t.Fatalf("recovery status must be persisted before PVC consent; patch order = %v", patchOrder)
			}
			if tc.failStatusPatch && (len(patchOrder) != 1 || patchOrder[0] != "status-failed") {
				t.Fatalf("status patch failure must prevent PVC consent; patch order = %v", patchOrder)
			}
			updatedPVC := &corev1.PersistentVolumeClaim{}
			if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(pvc), updatedPVC); err != nil {
				t.Fatal(err)
			}
			if got := updatedPVC.Annotations[remediationv1.SafeToDeleteAnnotation]; (got == "true") != tc.wantConsent {
				t.Fatalf("safe-to-delete = %q, want consent %v", got, tc.wantConsent)
			}
			if tc.wantConsent && updatedPVC.Annotations[remediationv1.ConsentIDAnnotation] != updatedPVC.Annotations[remediationv1.RequestIDAnnotation] {
				t.Fatal("consent-id does not match the current request-id")
			}
			if tc.wantConsent {
				persisted := &rabbitmqv1beta1.RabbitMq{}
				if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(instance), persisted); err != nil {
					t.Fatal(err)
				}
				entry := persisted.Status.PVCRemediation[pvc.Name]
				if entry.ConsentState != rabbitmqv1beta1.PVCRemediationConsentGranted || entry.PVCUID != string(pvc.UID) || entry.RequestID != requestID {
					t.Fatalf("persisted recovery status = %+v, want consent, PVC UID and request ID", entry)
				}
			}
		})
	}
}

func TestPVCRemediationConsentRequiresHealthySurvivorQuorum(t *testing.T) {
	testCases := []struct {
		name       string
		replicas   int32
		readyCount int32
		pods       []corev1.Pod
	}{
		{
			name:       "two replicas cannot retain quorum after losing one",
			replicas:   2,
			readyCount: 2,
			pods: []corev1.Pod{
				readyRabbitMQPod("rabbitmq-server-0", "worker-0"),
				readyRabbitMQPod("rabbitmq-server-1", "worker-1"),
			},
		},
		{
			name:       "candidate and one survivor are ready but third member is unavailable",
			replicas:   3,
			readyCount: 2,
			pods: []corev1.Pod{
				readyRabbitMQPod("rabbitmq-server-0", "worker-0"),
				readyRabbitMQPod("rabbitmq-server-1", "worker-1"),
				unavailableRabbitMQPod("rabbitmq-server-2", "worker-2"),
			},
		},
		{
			name:       "survivor with ready containers but rejoin readiness gate false does not count",
			replicas:   3,
			readyCount: 2,
			pods: []corev1.Pod{
				readyRabbitMQPod("rabbitmq-server-0", "worker-0"),
				rejoinPendingRabbitMQPod("rabbitmq-server-1", "worker-1"),
				unavailableRabbitMQPod("rabbitmq-server-2", "worker-2"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			const namespace = "openstack"
			replicas := tc.replicas
			instance := &rabbitmqv1beta1.RabbitMq{
				ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq", Namespace: namespace},
				Spec: rabbitmqv1beta1.RabbitMqSpec{
					RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{Replicas: &replicas},
				},
				Status: rabbitmqv1beta1.RabbitMqStatus{ReadyCount: tc.readyCount},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persistence-rabbitmq-server-0",
					Namespace: namespace,
					UID:       "pvc-uid",
					Labels:    map[string]string{"app.kubernetes.io/name": instance.Name},
					Annotations: map[string]string{
						remediationv1.PVCStuckOnNodeAnnotation: "worker-0",
						remediationv1.RequestIDAnnotation:      "request-uid",
					},
				},
			}

			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := rabbitmqv1beta1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "remediation.openstack.org", Version: "v1beta1"}})
			mapper.Add(schema.GroupVersionKind{Group: "remediation.openstack.org", Version: "v1beta1", Kind: "PodRemediator"}, meta.RESTScopeNamespace)
			objects := []client.Object{pvc}
			for i := range tc.pods {
				pod := tc.pods[i].DeepCopy()
				pod.Namespace = namespace
				pod.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
				objects = append(objects, pod)
			}
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRESTMapper(mapper).
				WithObjects(objects...).
				Build()
			rabbitmqReconciler := &rabbitmqcontroller.Reconciler{Client: kubeClient, Scheme: scheme}
			crHelper, err := helper.NewHelper(instance, kubeClient, nil, scheme, logr.Discard())
			if err != nil {
				t.Fatal(err)
			}

			if err := rabbitmqReconciler.CheckForStuckPVCRequiringRemediation(context.Background(), instance, crHelper); err != nil {
				t.Fatalf("CheckForStuckPVCRequiringRemediation() error = %v", err)
			}

			updatedPVC := &corev1.PersistentVolumeClaim{}
			if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(pvc), updatedPVC); err != nil {
				t.Fatal(err)
			}
			if got := updatedPVC.Annotations[remediationv1.SafeToDeleteAnnotation]; got == "true" {
				t.Fatal("unsafe PVC deletion consent was granted")
			}
		})
	}
}

func readyRabbitMQPod(name, nodeName string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "rabbitmq", Ready: true}},
		},
	}
}

func rejoinPendingRabbitMQPod(name, nodeName string) corev1.Pod {
	pod := readyRabbitMQPod(name, nodeName)
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{
		ConditionType: corev1.PodConditionType(rabbitmqv1beta1.RabbitMQRejoinReadyCondition),
	}}
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
		{Type: corev1.PodConditionType(rabbitmqv1beta1.RabbitMQRejoinReadyCondition), Status: corev1.ConditionFalse},
	}
	return pod
}

func unavailableRabbitMQPod(name, nodeName string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}
