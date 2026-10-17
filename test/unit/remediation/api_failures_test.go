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
	"encoding/json"
	"fmt"
	"testing"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediationctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// apiFaultClient injects transport failures before or after the underlying API
// operation. After-write failures model a server accepting a write whose reply
// never reaches the controller. Reads used for test assertions bypass this client.
type apiFaultClient struct {
	client.Client
	before func(context.Context, string, runtime.Object) error
	after  func(context.Context, string, runtime.Object) error
}

func apiOperation(verb string, obj runtime.Object) string {
	kind := fmt.Sprintf("%T", obj)
	switch o := obj.(type) {
	case *corev1.PersistentVolumeClaim:
		kind = "PVC"
		if verb == "patch" {
			if o.Annotations[deletionCommitFinalizedAnnotation] != "" {
				return "finalize PVC"
			}
			if o.Annotations[deletionCommitTokenAnnotation] != "" {
				return "prepare PVC"
			}
		}
	case *corev1.Pod:
		kind = "Pod"
	case *corev1.ConfigMap:
		kind = "commit"
	case *corev1.PersistentVolume:
		kind = "PV"
	case *corev1.Node:
		kind = "Node"
	case *corev1.PersistentVolumeClaimList:
		kind = "PVCs"
	case *corev1.PodList:
		kind = "Pods"
	case *corev1.ConfigMapList:
		kind = "commits"
	case *corev1.NodeList:
		kind = "Nodes"
	}
	return verb + " " + kind
}

func (c *apiFaultClient) call(ctx context.Context, verb string, obj runtime.Object, perform func() error) error {
	op := apiOperation(verb, obj)
	if c.before != nil {
		if err := c.before(ctx, op, obj); err != nil {
			return err
		}
	}
	if err := perform(); err != nil {
		return err
	}
	if c.after != nil {
		return c.after(ctx, op, obj)
	}
	return nil
}
func (c *apiFaultClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.call(ctx, "get", obj, func() error { return c.Client.Get(ctx, key, obj, opts...) })
}
func (c *apiFaultClient) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	return c.call(ctx, "list", obj, func() error { return c.Client.List(ctx, obj, opts...) })
}
func (c *apiFaultClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return c.call(ctx, "create", obj, func() error { return c.Client.Create(ctx, obj, opts...) })
}
func (c *apiFaultClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.call(ctx, "patch", obj, func() error { return c.Client.Patch(ctx, obj, patch, opts...) })
}
func (c *apiFaultClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return c.call(ctx, "update", obj, func() error { return c.Client.Update(ctx, obj, opts...) })
}
func (c *apiFaultClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return c.call(ctx, "delete", obj, func() error { return c.Client.Delete(ctx, obj, opts...) })
}

func TestDeletionAPIFailuresRecover(t *testing.T) {
	for _, op := range []string{"create commit", "prepare PVC", "finalize PVC", "update commit", "delete Pod", "delete PVC", "delete commit"} {
		for _, persisted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persisted=%t", op, persisted), func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				base := r.Client
				pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
				mustCreate(t, base, pod)
				faults := &apiFaultClient{Client: base}
				fired := false
				fail := func(_ context.Context, current string, _ runtime.Object) error {
					if current != op || fired {
						return nil
					}
					fired = true
					return apierrors.NewTimeoutError("injected lost API response", 1)
				}
				if persisted {
					faults.after = fail
				} else {
					faults.before = fail
				}
				r.Client, r.APIReader = faults, faults
				_, err := reconcileRemediation(ctx, r, pr)
				recoveredByReadback := persisted && (op == "prepare PVC" || op == "finalize PVC" || op == "update commit")
				if (err == nil) != recoveredByReadback {
					t.Fatalf("reconcile error = %v; immediate recovery expected = %t", err, recoveredByReadback)
				}
				if !fired {
					t.Fatal("fault was never injected")
				}
				podDeleted := recoveredByReadback || op == "delete PVC" || op == "delete commit" || (op == "delete Pod" && persisted)
				pvcDeleted := recoveredByReadback || op == "delete commit" || (op == "delete PVC" && persisted)
				assertObjectAbsent(t, base, pod, podDeleted)
				pvc := &corev1.PersistentVolumeClaim{}
				pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
				getErr := base.Get(ctx, pvcKey, pvc)
				if pvcDeleted {
					if !apierrors.IsNotFound(getErr) {
						t.Fatalf("PVC should be gone: %v", getErr)
					}
				} else {
					if getErr != nil {
						t.Fatal(getErr)
					}
					if !pvc.DeletionTimestamp.IsZero() {
						t.Fatal("PVC entered deletion prematurely")
					}
					committed := op == "update commit" || op == "delete Pod" || op == "delete PVC"
					if (pvc.Annotations[deletionCommitFinalizedAnnotation] != "") != committed {
						t.Fatalf("finalized token does not reflect the persisted decision: %v", pvc.Annotations)
					}
					// Once finalized, cleanup must recover even after participants
					// remove consent. Before finalization, retries need valid consent.
					if committed {
						delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
						delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
						if err := base.Update(ctx, pvc); err != nil {
							t.Fatal(err)
						}
					}
				}
				r.Client, r.APIReader = base, base
				if pvcDeleted || op == "update commit" || op == "delete Pod" || op == "delete PVC" {
					finishCommittedDeletion(t, r, pr)
				} else {
					finishRetriedDeletion(t, r, pr)
				}
				assertObjectAbsent(t, base, pod, true)
			})
		}
	}
}

func mustCreate(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}
func assertObjectAbsent(t *testing.T, c client.Client, obj client.Object, absent bool) {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
	if absent {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("%T should be absent, got %v", obj, err)
		}
	} else if err != nil {
		t.Fatalf("%T should remain: %v", obj, err)
	}
}

// finishRetriedDeletion acts as the participant only when the controller has
// issued a current request. It never manufactures commit tokens or bypasses the
// public reconcile entry point. Tests separately assert safety before recovery.
func finishRetriedDeletion(t *testing.T, r *remediationctrl.PodRemediatorReconciler, pr *remediationv1.PodRemediator) {
	t.Helper()
	finishDeletion(t, r, pr, true)
}

// A finalized decision must recover without asking the participant again.
// Withdraw consent explicitly so a retry cannot accidentally pass by starting
// another deletion transaction with the original consent annotations.
func finishCommittedDeletion(t *testing.T, r *remediationctrl.PodRemediatorReconciler, pr *remediationv1.PodRemediator) {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(context.Background(), client.ObjectKey{Namespace: "test", Name: "claim"}, pvc)
	if err == nil {
		delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
		delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
		if err := r.Update(context.Background(), pvc); err != nil {
			t.Fatal(err)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	finishDeletion(t, r, pr, false)
}

func finishDeletion(t *testing.T, r *remediationctrl.PodRemediatorReconciler, pr *remediationv1.PodRemediator, grantConsent bool) {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := reconcileRemediation(ctx, r, pr); err != nil {
			t.Fatal(err)
		}
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc)
		if apierrors.IsNotFound(err) {
			commits := &corev1.ConfigMapList{}
			if err := r.List(ctx, commits, client.InNamespace("test")); err != nil {
				t.Fatal(err)
			}
			if len(commits.Items) == 0 {
				return
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if request := pvc.Annotations[remediationv1.RequestIDAnnotation]; grantConsent && request != "" && pvc.Annotations[deletionCommitFinalizedAnnotation] == "" {
			pvc.Annotations[remediationv1.SafeToDeleteAnnotation] = "true"
			pvc.Annotations[remediationv1.ConsentIDAnnotation] = request
			if err := r.Update(ctx, pvc); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("retry did not finish PVC deletion and remove the commit")
}

func encodeTestCommit(t *testing.T, cm *corev1.ConfigMap, state deletionCommit) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	cm.Annotations[deletionCommitAnnotation] = string(encoded)
}

func TestDeletionReadbackFailurePreservesResources(t *testing.T) {
	for _, op := range []string{"prepare PVC", "finalize PVC", "update commit"} {
		for _, persisted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persisted=%t", op, persisted), func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				base := r.Client
				pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
				mustCreate(t, base, pod)
				writeFailed, readFailed := false, false
				failWrite := func(_ context.Context, current string, _ runtime.Object) error {
					if current == op && !writeFailed {
						writeFailed = true
						return apierrors.NewTimeoutError("lost write response", 1)
					}
					return nil
				}
				faults := &apiFaultClient{Client: base}
				faults.before = func(ctx context.Context, current string, obj runtime.Object) error {
					readOp := "get PVC"
					if op == "update commit" {
						readOp = "get commit"
					}
					if writeFailed && !readFailed && current == readOp {
						readFailed = true
						return apierrors.NewTimeoutError("readback unavailable", 1)
					}
					if !persisted {
						return failWrite(ctx, current, obj)
					}
					return nil
				}
				if persisted {
					faults.after = failWrite
				}
				r.Client, r.APIReader = faults, faults
				if _, err := reconcileRemediation(ctx, r, pr); err == nil {
					t.Fatal("unverifiable write must return an error")
				}
				if !writeFailed || !readFailed {
					t.Fatal("did not reach both injected failures")
				}
				assertObjectAbsent(t, base, pod, false)
				pvc := &corev1.PersistentVolumeClaim{}
				if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				if !pvc.DeletionTimestamp.IsZero() {
					t.Fatal("unverifiable write started PVC deletion")
				}
				committed := op == "update commit" || (op == "finalize PVC" && persisted)
				if (pvc.Annotations[deletionCommitFinalizedAnnotation] != "") != committed {
					t.Fatal("incorrect persisted commit boundary")
				}
				r.Client, r.APIReader = base, base
				if committed {
					finishCommittedDeletion(t, r, pr)
				} else {
					finishRetriedDeletion(t, r, pr)
				}
			})
		}
	}
}

func TestDeletionConflictingCreate(t *testing.T) {
	for _, change := range []string{"matching prepared", "matching committed", "invalid JSON", "different owner", "different PVC", "different node", "different version", "different request"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			base := r.Client
			pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
			mustCreate(t, base, pod)
			fired := false
			var stored *corev1.ConfigMap
			faults := &apiFaultClient{Client: base, before: func(ctx context.Context, op string, obj runtime.Object) error {
				if op != "create commit" || fired {
					return nil
				}
				fired = true
				stored = obj.(*corev1.ConfigMap).DeepCopy()
				state, err := decodeDeletionCommit(stored)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "different owner":
					state.RemediatorUID = "another-remediator"
				case "different PVC":
					state.PVCName = "another-claim"
				case "different node":
					state.Node = "another-node"
				case "different version":
					state.PVCResourceVersion = "outdated"
				case "different request":
					state.RequestID = "outdated"
				case "matching committed":
					state.Phase = deletionCommitPhaseCommitted
					pvc := &corev1.PersistentVolumeClaim{}
					if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
						t.Fatal(err)
					}
					pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = committedPVCDeletionValue(state.PVCUID, state.RemediatorUID, state.Node)
					pvc.Annotations[deletionCommitTokenAnnotation] = state.Token
					pvc.Annotations[deletionCommitFinalizedAnnotation] = state.Token
					if err := base.Update(ctx, pvc); err != nil {
						t.Fatal(err)
					}
				}
				encodeTestCommit(t, stored, state)
				if change == "invalid JSON" {
					stored.Annotations[deletionCommitAnnotation] = "{"
				}
				mustCreate(t, base, stored)
				return apierrors.NewAlreadyExists(corev1.Resource("configmaps"), stored.Name)
			}}
			r.Client, r.APIReader = faults, faults
			_, err := reconcileRemediation(ctx, r, pr)
			matching := change == "matching prepared" || change == "matching committed"
			if (err == nil) != matching {
				t.Fatalf("reconcile error = %v, matching record = %t", err, matching)
			}
			if !fired {
				t.Fatal("did not inject concurrent create")
			}
			assertObjectAbsent(t, base, pod, matching)
			if matching {
				assertObjectAbsent(t, base, stored, true)
			} else {
				current := &corev1.ConfigMap{}
				if err := base.Get(ctx, client.ObjectKeyFromObject(stored), current); err != nil {
					t.Fatal(err)
				}
				if current.Annotations[deletionCommitAnnotation] != stored.Annotations[deletionCommitAnnotation] {
					t.Fatal("conflicting record was overwritten")
				}
				pvc := &corev1.PersistentVolumeClaim{}
				if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				if pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
					t.Fatal("conflict authorized deletion")
				}
			}
		})
	}
}

func TestFinalizationConflictRequiresNewConsent(t *testing.T) {
	for _, change := range []string{"withdraw consent", "replace consent ID", "unrelated PVC update"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			base := r.Client
			pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
			mustCreate(t, base, pod)
			fired := false
			faults := &apiFaultClient{Client: base, before: func(ctx context.Context, op string, _ runtime.Object) error {
				if op != "finalize PVC" || fired {
					return nil
				}
				fired = true
				pvc := &corev1.PersistentVolumeClaim{}
				if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "withdraw consent":
					delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
				case "replace consent ID":
					pvc.Annotations[remediationv1.ConsentIDAnnotation] = "another-request"
				case "unrelated PVC update":
					pvc.Annotations["test.example/changed"] = "true"
				}
				return base.Update(ctx, pvc)
			}}
			r.Client, r.APIReader = faults, faults
			if _, err := reconcileRemediation(ctx, r, pr); err != nil {
				t.Fatal(err)
			}
			if !fired {
				t.Fatal("did not reach finalization")
			}
			assertObjectAbsent(t, base, pod, false)
			pvc := &corev1.PersistentVolumeClaim{}
			if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
				t.Fatal(err)
			}
			if pvc.Annotations[deletionCommitFinalizedAnnotation] != "" || remediationv1.HasRemediationConsent(pvc.Annotations) {
				t.Fatal("conflicting update retained deletion authority")
			}
			r.Client, r.APIReader = base, base
			if _, err := reconcileRemediation(ctx, r, pr); err != nil {
				t.Fatal(err)
			}
			assertObjectAbsent(t, base, pod, false)
			finishRetriedDeletion(t, r, pr)
		})
	}
}

func TestPVCChangesBetweenCommitWrites(t *testing.T) {
	for _, op := range []string{"create commit", "prepare PVC", "finalize PVC", "update commit"} {
		for _, change := range []string{"read unavailable", "PVC disappears", "PVC replaced", "marker removed"} {
			if change == "marker removed" && op == "create commit" {
				continue
			}
			t.Run(op+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				base := r.Client
				pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
				mustCreate(t, base, pod)
				fired, readFailed := false, false
				faults := &apiFaultClient{Client: base}
				faults.after = func(ctx context.Context, current string, _ runtime.Object) error {
					if current != op || fired {
						return nil
					}
					fired = true
					if change == "read unavailable" {
						return nil
					}
					pvc := &corev1.PersistentVolumeClaim{}
					if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
						t.Fatal(err)
					}
					if change == "marker removed" {
						if op == "prepare PVC" {
							delete(pvc.Annotations, deletionCommitTokenAnnotation)
						} else {
							delete(pvc.Annotations, deletionCommitFinalizedAnnotation)
						}
						return base.Update(ctx, pvc)
					}
					if err := base.Delete(ctx, pvc); err != nil {
						t.Fatal(err)
					}
					if change == "PVC replaced" {
						pvc.UID = "replacement-pvc-uid"
						pvc.ResourceVersion = ""
						pvc.Annotations = nil
						mustCreate(t, base, pvc)
					}
					return nil
				}
				faults.before = func(_ context.Context, current string, _ runtime.Object) error {
					if fired && change == "read unavailable" && current == "get PVC" && !readFailed {
						readFailed = true
						return apierrors.NewServiceUnavailable("PVC readback unavailable")
					}
					return nil
				}
				r.Client, r.APIReader = faults, faults
				_, err := reconcileRemediation(ctx, r, pr)
				wantError := op != "update commit" || change != "PVC disappears"
				if (err != nil) != wantError || !fired {
					t.Fatalf("error=%v wantError=%t fired=%t", err, wantError, fired)
				}
				assertObjectAbsent(t, base, pod, false)
				r.Client, r.APIReader = base, base
				if change == "PVC replaced" || change == "PVC disappears" {
					if _, err := reconcileRemediation(ctx, r, pr); err != nil {
						t.Fatal(err)
					}
					assertObjectAbsent(t, base, pod, false)
					if change == "PVC replaced" {
						current := &corev1.PersistentVolumeClaim{}
						if err := base.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, current); err != nil {
							t.Fatal(err)
						}
						if current.UID != "replacement-pvc-uid" || !current.DeletionTimestamp.IsZero() || remediationv1.HasRemediationConsent(current.Annotations) {
							t.Fatal("replacement PVC inherited deletion authority")
						}
					}
				} else {
					finishRetriedDeletion(t, r, pr)
				}
			})
		}
	}
}
