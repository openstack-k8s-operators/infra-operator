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

package remediation_test

import (
	"context"
	"testing"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediationctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func seedDeletionRecord(t *testing.T, c client.Client, phase string, finalized bool) *corev1.ConfigMap {
	t.Helper()
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
		t.Fatal(err)
	}
	state := deletionCommit{
		PVCName: pvc.Name, PVCUID: string(pvc.UID), PVCResourceVersion: pvc.ResourceVersion,
		RequestID: pvc.Annotations[remediationv1.RequestIDAnnotation], RemediatorUID: "pr-uid",
		Node: "worker-0", Token: "persisted-token", Phase: phase,
		Pods: []committedPod{{Name: "pod", UID: "pod-uid"}},
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: deletionCommitName(state.PVCUID), Annotations: map[string]string{}}}
	encodeTestCommit(t, cm, state)
	mustCreate(t, c, cm)
	pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = committedPVCDeletionValue(state.PVCUID, state.RemediatorUID, state.Node)
	pvc.Annotations[deletionCommitTokenAnnotation] = state.Token
	if finalized {
		pvc.Annotations[deletionCommitFinalizedAnnotation] = state.Token
	}
	if err := c.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestMalformedDeletionRecordsFailClosed(t *testing.T) {
	for _, corruption := range []string{"invalid phase", "missing request", "missing version", "missing token", "missing Pod name", "missing Pod UID"} {
		for _, mode := range []string{"provisional", "finalized", "disabled", "deleting", "foreign owner"} {
			t.Run(corruption+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
				mustCreate(t, r.Client, pod)
				finalized := mode != "provisional" && mode != "foreign owner"
				cm := seedDeletionRecord(t, r.Client, deletionCommitPhaseCommitted, finalized)
				state, err := decodeDeletionCommit(cm)
				if err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "invalid phase":
					state.Phase = "unknown"
				case "missing request":
					state.RequestID = ""
				case "missing version":
					state.PVCResourceVersion = ""
				case "missing token":
					state.Token = ""
				case "missing Pod name":
					state.Pods[0].Name = ""
				case "missing Pod UID":
					state.Pods[0].UID = ""
				}
				encodeTestCommit(t, cm, state)
				if err := r.Update(ctx, cm); err != nil {
					t.Fatal(err)
				}
				if mode == "foreign owner" {
					pvc := &corev1.PersistentVolumeClaim{}
					if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
						t.Fatal(err)
					}
					pvc.Annotations[remediationv1.RemediatorUIDAnnotation] = "foreign-owner"
					if err := r.Update(ctx, pvc); err != nil {
						t.Fatal(err)
					}
				}
				pr.Spec.Disabled = mode == "disabled"
				if mode == "deleting" {
					_, err = reconcileDeletingRemediation(ctx, r, pr)
				} else {
					_, err = reconcileRemediation(ctx, r, pr)
				}
				if err != nil {
					t.Fatal(err)
				}
				assertObjectAbsent(t, r.Client, pod, false)
				pvc := &corev1.PersistentVolumeClaim{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				if !pvc.DeletionTimestamp.IsZero() {
					t.Fatal("malformed record authorized deletion")
				}
				assertObjectAbsent(t, r.Client, cm, mode == "provisional")
				if finalized {
					ready := pr.Status.Conditions.Get(condition.ReadyCondition)
					if ready == nil || string(ready.Reason) != remediationctrl.InvalidPVCDeletionCommitReason {
						t.Fatalf("invalid persisted decision not reported: %v", ready)
					}
					if mode == "deleting" && len(pr.Finalizers) == 0 {
						t.Fatal("CR finalizer released while invalid commit remains")
					}
				} else if mode == "provisional" && remediationv1.HasRemediationConsent(pvc.Annotations) {
					t.Fatal("rejected record retained consent")
				}
			})
		}
	}
}

func TestPersistedDeletionAPIFailuresRetry(t *testing.T) {
	for _, scenario := range []struct {
		phase, op string
		finalized bool
	}{
		{deletionCommitPhasePrepared, "delete commit", false},
		{deletionCommitPhasePrepared, "patch PVC", false},
		{deletionCommitPhasePrepared, "update commit", true},
		{deletionCommitPhaseCommitted, "delete commit", false},
		{deletionCommitPhaseCommitted, "patch PVC", false},
		{deletionCommitPhaseCommitted, "get PVC", true},
		{deletionCommitPhaseCommitted, "list Pods", true},
		{deletionCommitPhaseCommitted, "delete Pod", true},
		{deletionCommitPhaseCommitted, "delete PVC", true},
	} {
		t.Run(scenario.phase+"/"+scenario.op, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			base := r.Client
			pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
			mustCreate(t, base, pod)
			seedDeletionRecord(t, base, scenario.phase, scenario.finalized)
			fired := false
			faults := &apiFaultClient{Client: base, before: func(_ context.Context, op string, _ runtime.Object) error {
				if op == scenario.op && !fired {
					fired = true
					return apierrors.NewServiceUnavailable("injected resume failure")
				}
				return nil
			}}
			r.Client, r.APIReader = faults, faults
			if _, err := reconcileRemediation(ctx, r, pr); err == nil || !fired {
				t.Fatalf("resume failure not propagated: %v, fired=%t", err, fired)
			}
			pvc := &corev1.PersistentVolumeClaim{}
			if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
				t.Fatal(err)
			}
			if !pvc.DeletionTimestamp.IsZero() {
				t.Fatal("failed resume started PVC deletion")
			}
			assertObjectAbsent(t, base, pod, scenario.op == "delete PVC")
			r.Client, r.APIReader = base, base
			if scenario.finalized {
				finishCommittedDeletion(t, r, pr)
			} else {
				finishRetriedDeletion(t, r, pr)
			}
		})
	}
}
