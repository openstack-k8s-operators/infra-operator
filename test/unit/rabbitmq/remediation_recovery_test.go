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

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	rabbitmqcontroller "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"
	rabbitmqresources "github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func managementClientWithMembership(replicas int, offline map[int]bool, queues string) *http.Client {
	return &http.Client{Transport: rabbitMQManagementTransport(func(req *http.Request) (*http.Response, error) {
		body := queues
		switch req.URL.Path {
		case "/api/nodes":
			nodes := make([]string, 0, replicas)
			for ordinal := 0; ordinal < replicas; ordinal++ {
				nodes = append(nodes, fmt.Sprintf(`{"name":"rabbit@rabbitmq-server-%d.rabbitmq-nodes.openstack","running":%t}`, ordinal, !offline[ordinal]))
			}
			body = "[" + strings.Join(nodes, ",") + "]"
		case "/api/queues/detailed":
			if body == "" {
				body = `[]`
			}
		default:
			return nil, fmt.Errorf("unexpected management endpoint %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
}

type rabbitMQAPIReader struct {
	client.Reader
	instance *rabbitmqv1.RabbitMq
}

func (r rabbitMQAPIReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if target, ok := obj.(*rabbitmqv1.RabbitMq); ok {
		*target = *r.instance.DeepCopy()
		return nil
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestConsentBelowLiveQuorumRequestsRetry(t *testing.T) {
	ctx := context.Background()
	r, instance, h := newConsentFixture(t, testPVCName)
	r.ManagementHTTPClient = managementClientWithMembership(3, map[int]bool{0: true, 2: true}, "")
	if err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true}); err == nil {
		t.Fatal("live-quorum deferral must return an error so Reconcile schedules a retry")
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testPVCName}, claim); err != nil {
		t.Fatal(err)
	}
	if remediationv1.HasRemediationConsent(claim.Annotations) {
		t.Fatal("consent granted below live quorum")
	}
	r.ManagementHTTPClient = managementClientWithMembership(3, map[int]bool{0: true}, "")
	if err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(claim), claim); err != nil {
		t.Fatal(err)
	}
	if !remediationv1.HasRemediationConsent(claim.Annotations) {
		t.Fatal("consent did not recover after live quorum recovered")
	}
}

func TestDeletionValidationRefreshesRabbitMQSafety(t *testing.T) {
	for _, tc := range []struct {
		name          string
		offline       map[int]bool
		queues        string
		missingRecord bool
		wantError     bool
		wantErrorText string
		readFails     bool
	}{
		{name: "unchanged healthy survivors"},
		{name: "survivor fails after consent", offline: map[int]bool{0: true, 2: true}, wantError: true},
		{name: "queue loses majority after consent", queues: `[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]}]`, wantError: true, wantErrorText: "too few online safe members"},
		{name: "online replicas lose Ra consensus after consent", queues: `[{"name":"jobs","vhost":"/","type":"quorum","members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],"online":["rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack","rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"]}]`, readFails: true, wantError: true},
		{name: "durable classic queue appears on candidate", queues: `[{"name":"legacy","vhost":"/","type":"classic","node":"rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack","durable":true}]`, wantError: true},
		{name: "recovery record withdrawn", missingRecord: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, instance, h := newConsentFixture(t, testPVCName)
			if err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true}); err != nil {
				t.Fatal(err)
			}
			claim := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testPVCName}, claim); err != nil {
				t.Fatal(err)
			}
			if tc.missingRecord {
				current := &rabbitmqv1.RabbitMq{}
				if err := r.Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
					t.Fatal(err)
				}
				current.Status.PVCRemediation = nil
				r.APIReader = rabbitMQAPIReader{Reader: r.Client, instance: current}
			} else {
				// The ordinary client is no longer trusted for commit-time reads.
				r.APIReader = r.Client
			}
			r.ManagementHTTPClient = managementClientWithMembership(3, tc.offline, tc.queues)
			executor := &fakeExec{}
			if tc.readFails {
				executor.quorumReadErr = fmt.Errorf("Ra group cannot confirm leadership")
			}
			r.PodCommandExecutor = executor.run
			err := r.ValidatePVCDeletion(ctx, claim)
			if (err != nil) != tc.wantError {
				t.Fatalf("validation error = %v, want error %v", err, tc.wantError)
			}
			if tc.wantErrorText != "" && !strings.Contains(err.Error(), tc.wantErrorText) {
				t.Fatalf("validation error = %v, want %q", err, tc.wantErrorText)
			}
		})
	}
}

func TestDeletionValidationUsesRecoveryRecordAfterLabelRemoval(t *testing.T) {
	ctx := context.Background()
	r, instance, h := newConsentFixture(t, testPVCName)
	if err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true}); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testPVCName}, claim); err != nil {
		t.Fatal(err)
	}
	claim.Labels = nil
	if err := r.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}

	r.APIReader = r.Client
	r.ManagementHTTPClient = managementClientWithMembership(3, map[int]bool{0: true, 2: true}, "")
	r.PodCommandExecutor = (&fakeExec{}).run
	if err := r.ValidatePVCDeletion(ctx, claim); err == nil {
		t.Fatal("removing the editable app label bypassed RabbitMQ deletion validation")
	}
}

func TestDeletionValidationLeavesClaimsWithoutRecoveryOnGenericPath(t *testing.T) {
	r, _, _ := newConsentFixture(t, testPVCName)
	for _, claimName := range []string{testPVCName, "persistence-unrelated-server-0"} {
		claim := replacementPVC(oldPVCUID)
		claim.Name = claimName
		if err := r.ValidatePVCDeletion(context.Background(), claim); err != nil {
			t.Fatalf("claim %s without a RabbitMQ recovery record must remain on the generic path: %v", claimName, err)
		}
	}
}

func TestRecoveredReplacementUIDChangeRequiresFreshRejoin(t *testing.T) {
	ctx := context.Background()
	r, instance, h := newConsentFixture(t, testPVCName)
	instance.Status.PVCRemediation = instanceAfterRecovery(3).Status.PVCRemediation
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testPVCName}, claim); err != nil {
		t.Fatal(err)
	}
	claim.UID = "third-pvc-uid"
	if err := r.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}

	err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h,
		rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true})
	if err == nil {
		t.Fatal("a changed replacement PVC must keep the recovery slot occupied")
	}
	record := instance.Status.PVCRemediation[testPVCName]
	if record.Phase != rabbitmqv1.PVCRemediationConsented || record.ReplacementPVCUID != "" {
		t.Fatalf("changed replacement was not reclassified for rejoin: %+v", record)
	}
}

func TestRecoverySlotRequiresReplacementClaim(t *testing.T) {
	for _, tc := range []struct {
		name          string
		terminating   bool
		original      bool
		missing       bool
		unrelatedDown bool
		wantConsent   bool
	}{
		{name: "terminating original cannot finish recovery", terminating: true, original: true},
		{name: "terminating replacement cannot finish recovery", terminating: true},
		{name: "missing replacement claim keeps slot", missing: true},
		{name: "recovered replacement allows next consent with unrelated broker down", unrelatedDown: true, wantConsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, instance, h := newConsentFixture(t, testPVCName)
			instance.Spec.Replicas = ptr.To(int32(5))
			instance.Status.ReadyCount = 4
			instance.Status.CurrentVersion = "4.0.9" // Rejoin does not mask the handshake's own safety gate.
			instance.Status.PVCRemediation = instanceWithRecovery(5).Status.PVCRemediation
			claim := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testPVCName}, claim); err != nil {
				t.Fatal(err)
			}
			if !tc.original {
				claim.UID = types.UID(newPVCUID)
			}
			claim.Annotations = nil
			if tc.terminating {
				claim.Finalizers = []string{"test.example/hold"}
				claim.Annotations = map[string]string{remediationv1.PVCStuckOnNodeAnnotation: "worker-0", remediationv1.RequestIDAnnotation: testRequestID, remediationv1.SafeToDeleteAnnotation: "true", remediationv1.ConsentIDAnnotation: testRequestID}
			}
			if err := r.Update(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if tc.terminating || tc.missing {
				if err := r.Delete(ctx, claim); err != nil {
					t.Fatal(err)
				}
			}
			next := replacementPVC("second-pvc-uid")
			next.Name = "persistence-rabbitmq-server-1"
			next.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
			next.Annotations = map[string]string{remediationv1.PVCStuckOnNodeAnnotation: "worker-1", remediationv1.RequestIDAnnotation: "second-request"}
			if err := r.Create(ctx, next); err != nil {
				t.Fatal(err)
			}
			pod0 := &corev1.Pod{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testName + "-server-0"}, pod0); err != nil {
				t.Fatal(err)
			}
			pod0.Spec.Volumes = []corev1.Volume{{Name: "persistence", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: testPVCName}}}}
			if err := r.Update(ctx, pod0); err != nil {
				t.Fatal(err)
			}
			for ordinal := 3; ordinal < 5; ordinal++ {
				pod := readyRabbitMQPod(fmt.Sprintf("%s-server-%d", testName, ordinal), fmt.Sprintf("worker-%d", ordinal))
				pod.Namespace = testNS
				pod.Labels = map[string]string{"app.kubernetes.io/name": instance.Name}
				if err := r.Create(ctx, &pod); err != nil {
					t.Fatal(err)
				}
			}
			offline := map[int]bool{}
			if tc.unrelatedDown {
				pod := &corev1.Pod{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: testNS, Name: testName + "-server-2"}, pod); err != nil {
					t.Fatal(err)
				}
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
				if err := r.Status().Update(ctx, pod); err != nil {
					t.Fatal(err)
				}
				offline[2] = true
			}
			r.ManagementHTTPClient = managementClientWithMembership(5, offline, "")
			if again, err := r.ReconcileNodeRejoin(ctx, instance, true); err != nil || again {
				t.Fatalf("unexpected rejoin gate %v, %v", again, err)
			}
			err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h, rabbitmqcontroller.RemediationDecision{AllowNewConsent: true, RecoveryInFlight: true})
			if tc.wantConsent && err != nil {
				t.Fatal(err)
			}
			if !tc.wantConsent && err == nil {
				t.Fatal("pending replacement should request retry")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(next), next); err != nil {
				t.Fatal(err)
			}
			if got := remediationv1.HasRemediationConsent(next.Annotations); got != tc.wantConsent {
				t.Fatalf("next consent=%v, want %v", got, tc.wantConsent)
			}
		})
	}
}

func TestNewPodStartsOutsideClientServices(t *testing.T) {
	instance := instanceWith(3)
	sts := rabbitmqresources.StatefulSet(instance, "config", nil, nil, "4.2", false, rabbitmqresources.ProxyConfig{}, true)
	if got := sts.Spec.Template.Labels[rabbitmqv1.RabbitMQServingLabel]; got != rabbitmqv1.RabbitMQServingFalse {
		t.Fatalf("new pod serving=%q", got)
	}
	service, err := rabbitmqresources.ClientService(instance, true)
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Selector[rabbitmqv1.RabbitMQServingLabel] != rabbitmqv1.RabbitMQServingTrue {
		t.Fatal("client Service does not exclude new pods")
	}
}

// Without PodRemediator nothing can ever remediate a PVC, so none of the
// machinery that makes a broker depend on a running operator may be deployed:
// an unavailable operator must not be able to stop RabbitMQ from starting or
// from serving clients.
func TestWithoutRemediationNothingWaitsForTheOperator(t *testing.T) {
	instance := instanceWith(3)
	sts := rabbitmqresources.StatefulSet(instance, "config", nil, nil, "4.2", false, rabbitmqresources.ProxyConfig{}, false)
	for _, init := range sts.Spec.Template.Spec.InitContainers {
		if init.Name == "startup-gate" {
			t.Fatal("startup gate must not be deployed without PodRemediator")
		}
	}
	if _, ok := sts.Spec.Template.Labels[rabbitmqv1.RabbitMQServingLabel]; ok {
		t.Fatal("pods must not require admission without PodRemediator")
	}
	service, err := rabbitmqresources.ClientService(instance, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Spec.Selector[rabbitmqv1.RabbitMQServingLabel]; ok {
		t.Fatal("client Service must not require admission without PodRemediator")
	}
}

// ObservedGeneration records that the controller saw the current spec.
// Readiness remains governed by the individual input and child conditions.
func TestObservedGenerationUpdatedWhileInputsAreMissing(t *testing.T) {
	ctx := context.Background()
	r, instance, _ := newConsentFixture(t, testPVCName)
	if err := r.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	instance.Generation = 7
	instance.Finalizers = []string{"openstack.org/rabbitmq"}
	instance.Spec.TLS.SecretName = "missing-tls-secret"
	if err := r.Update(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	if instance.Status.ObservedGeneration != instance.Generation {
		t.Fatalf("observedGeneration=%d, want current generation %d",
			instance.Status.ObservedGeneration, instance.Generation)
	}
	if ready := instance.Status.Conditions.Get(condition.ReadyCondition); ready == nil || ready.Status != corev1.ConditionFalse {
		t.Fatalf("Ready condition=%+v, want False while TLS input is missing", ready)
	}
}
