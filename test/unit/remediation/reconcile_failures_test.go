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
	"fmt"
	"testing"
	"time"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediationctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestScanAndCleanupAPIFailuresRetry(t *testing.T) {
	for _, tc := range []struct {
		mode, op   string
		occurrence int
	}{
		{"normal", "list PVCs", 1}, {"normal", "list commits", 1},
		{"normal", "list Nodes", 1}, {"normal", "list PVCs", 2},
		{"normal", "get PV", 1}, {"normal", "list Pods", 1},
		{"request", "get PV", 1}, {"request", "patch PVC", 1},
		{"renew request", "patch PVC", 1},
		{"disabled", "list PVCs", 2}, {"disabled", "patch PVC", 1},
		{"deleting", "list PVCs", 1}, {"deleting", "list PVCs", 2}, {"deleting", "patch PVC", 1},
		{"recovered", "patch PVC", 1}, {"orphan consent", "patch PVC", 1},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.mode, tc.op, tc.occurrence), func(t *testing.T) {
			ctx := context.Background()
			node := remediationNode(corev1.ConditionFalse)
			if tc.mode == "recovered" || tc.mode == "orphan consent" {
				node.Status.Conditions[0].Status = corev1.ConditionTrue
			}
			r, pr := remediationFixture(t, node)
			base := r.Client
			pr.Spec.Disabled = tc.mode == "disabled"
			pvc := &corev1.PersistentVolumeClaim{}
			key := client.ObjectKey{Namespace: "test", Name: "claim"}
			if err := base.Get(ctx, key, pvc); err != nil {
				t.Fatal(err)
			}
			switch tc.mode {
			case "request":
				pvc.Annotations = nil
			case "renew request":
				pvc.Annotations[remediationv1.RequestIDAnnotation] = "previous-fault"
			case "orphan consent":
				delete(pvc.Annotations, remediationv1.PVCStuckOnNodeAnnotation)
			}
			if err := base.Update(ctx, pvc); err != nil {
				t.Fatal(err)
			}
			pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
			mustCreate(t, base, pod)
			count := 0
			fired := false
			faults := &apiFaultClient{Client: base, before: func(_ context.Context, op string, _ runtime.Object) error {
				if op == tc.op {
					count++
					if count == tc.occurrence {
						fired = true
						return apierrors.NewServiceUnavailable("injected API outage")
					}
				}
				return nil
			}}
			r.Client, r.APIReader = faults, faults
			var err error
			if tc.mode == "deleting" {
				_, err = reconcileDeletingRemediation(ctx, r, pr)
			} else {
				_, err = reconcileRemediation(ctx, r, pr)
			}
			if err == nil || !fired {
				t.Fatalf("failure not propagated: err=%v, fired=%t", err, fired)
			}
			assertObjectAbsent(t, base, pod, false)
			if err := base.Get(ctx, key, pvc); err != nil {
				t.Fatal(err)
			}
			if !pvc.DeletionTimestamp.IsZero() || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
				t.Fatal("failed scan authorized deletion")
			}
			r.Client, r.APIReader = base, base
			if tc.mode == "normal" {
				finishRetriedDeletion(t, r, pr)
				return
			}
			if tc.mode == "deleting" {
				_, err = reconcileDeletingRemediation(ctx, r, pr)
			} else {
				_, err = reconcileRemediation(ctx, r, pr)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertObjectAbsent(t, base, pod, false)
			if err := base.Get(ctx, key, pvc); err != nil {
				t.Fatal(err)
			}
			if remediationv1.HasRemediationConsent(pvc.Annotations) {
				t.Fatal("cleanup or a new request retained old consent")
			}
			if tc.mode == "request" || tc.mode == "renew request" {
				if pvc.Annotations[remediationv1.RequestIDAnnotation] != "pr-uid:pvc-uid:snr-uid" {
					t.Fatal("retry failed to issue the current request")
				}
			}
		})
	}
}

func TestDependencyAPIFailuresDoNotAuthorizeDeletion(t *testing.T) {
	for _, gvr := range []schema.GroupVersionResource{gvrNodeHealthCheck, gvrSelfNodeRemediationTemplate, gvrSelfNodeRemediation} {
		for _, problem := range []string{"not installed", "no match", "forbidden"} {
			t.Run(gvr.Resource+"/"+problem, func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
				mustCreate(t, r.Client, pod)
				enabled, fired := true, false
				r.DynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("list", gvr.Resource, func(ktesting.Action) (bool, runtime.Object, error) {
					if !enabled {
						return false, nil, nil
					}
					fired = true
					var err error
					switch problem {
					case "not installed":
						err = apierrors.NewNotFound(gvr.GroupResource(), "")
					case "no match":
						err = &meta.NoResourceMatchError{PartialResource: gvr}
					default:
						err = apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("denied"))
					}
					return true, nil, err
				})
				result, err := reconcileRemediation(ctx, r, pr)
				if (err != nil) != (problem == "forbidden") || !fired {
					t.Fatalf("dependency failure: err=%v fired=%t", err, fired)
				}
				if err == nil && result.RequeueAfter <= 0 {
					t.Fatal("missing dependency did not schedule a retry")
				}
				assertObjectAbsent(t, r.Client, pod, false)
				pvc := &corev1.PersistentVolumeClaim{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				if pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
					t.Fatal("missing dependency authorized deletion")
				}
				enabled = false
				finishRetriedDeletion(t, r, pr)
			})
		}
	}
}

func TestPollingIntervalPrecedence(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		for _, source := range []string{"default", "environment", "spec", "invalid"} {
			t.Run(fmt.Sprintf("%s/waiting=%t", source, waiting), func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				pr.Spec.Disabled = !waiting
				pvc := &corev1.PersistentVolumeClaim{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
				if err := r.Update(ctx, pvc); err != nil {
					t.Fatal(err)
				}
				want := remediationctrl.DefaultPeriodicPollInterval
				if waiting {
					want = remediationctrl.DefaultConsentPollInterval
				}
				if source != "default" {
					r.PeriodicPollInterval, r.ConsentPollInterval = 17*time.Second, 7*time.Second
					want = 17 * time.Second
					if waiting {
						want = 7 * time.Second
					}
				}
				if source == "spec" || source == "invalid" {
					pr.Spec.PeriodicPollInterval = &metav1.Duration{Duration: 11 * time.Second}
					pr.Spec.ConsentPollInterval = &metav1.Duration{Duration: 3 * time.Second}
					want = 11 * time.Second
					if waiting {
						want = 3 * time.Second
					}
					if source == "invalid" {
						pr.Spec.ConsentPollInterval.Duration = 0
						want = 0
					}
				}
				result, err := reconcileRemediation(ctx, r, pr)
				if err != nil {
					t.Fatal(err)
				}
				if result.RequeueAfter != want {
					t.Fatalf("poll = %s, want %s", result.RequeueAfter, want)
				}
				if source == "invalid" && pr.Status.Conditions.Get(condition.ReadyCondition).Status != corev1.ConditionFalse {
					t.Fatal("invalid interval not reported")
				}
			})
		}
	}
}

func TestPendingDeletionKeepsPollingWithoutUsableConfiguration(t *testing.T) {
	for _, problem := range []string{"invalid interval", "missing NHC", "missing template"} {
		t.Run(problem, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/hold"})
			mustCreate(t, r.Client, pod)
			seedDeletionRecord(t, r.Client, deletionCommitPhaseCommitted, true)
			switch problem {
			case "invalid interval":
				pr.Spec.PeriodicPollInterval = &metav1.Duration{Duration: 0}
			case "missing NHC":
				if err := r.DynamicClient.Resource(gvrNodeHealthCheck).Delete(ctx, "nhc", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "missing template":
				if err := r.DynamicClient.Resource(gvrSelfNodeRemediationTemplate).Namespace("test").Delete(ctx, "template", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := reconcileRemediation(ctx, r, pr)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != remediationctrl.DefaultConsentPollInterval {
				t.Fatalf("committed cleanup lost its retry: %v", result)
			}
			current := &corev1.Pod{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
				t.Fatal(err)
			}
			if current.DeletionTimestamp.IsZero() {
				t.Fatal("configuration change prevented committed Pod cleanup")
			}
		})
	}
}
