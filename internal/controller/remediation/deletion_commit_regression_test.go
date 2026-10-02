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

package remediation

import (
	"context"
	"fmt"
	"testing"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDeletionCommitRevalidatesFencing(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         string
		directAffinity bool
		allowed        bool
		readError      bool
	}{
		{name: "unchanged fencing", allowed: true},
		{name: "node recovers", change: "node recovers"},
		{name: "node is replaced", change: "node is replaced"},
		{name: "SNR disappears", change: "SNR disappears"},
		{name: "SNR is deleting", change: "SNR is deleting"},
		{name: "SNR phase regresses", change: "SNR phase regresses"},
		{name: "SNR is replaced", change: "SNR is replaced"},
		{name: "multiple SNRs", change: "multiple SNRs"},
		{name: "missing node with hostname affinity", change: "node disappears"},
		{name: "missing node with direct affinity", change: "node disappears", directAffinity: true, allowed: true},
		{name: "node read fails", change: "node read fails", readError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			if tc.directAffinity {
				pv := &corev1.PersistentVolume{}
				if err := r.Get(ctx, client.ObjectKey{Name: "pv"}, pv); err != nil {
					t.Fatal(err)
				}
				pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Key = "topology.topolvm.io/node"
				if err := r.Update(ctx, pv); err != nil {
					t.Fatal(err)
				}
			}
			pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/hold"})
			if err := r.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			base := r.Client
			r.APIReader = base
			markerClient := &pvcMarkerRevocationClient{Client: base, afterMarkerWrite: func(ctx context.Context) {
				// Change fencing after the provisional PVC write, before the
				// irreversible finalization. The PVC version itself does not change.
				changeFencingBeforeCommit(t, ctx, r, tc.change)
			}}
			recorder := &podDeleteRecordingClient{Client: markerClient}
			r.Client = recorder
			_, err := r.reconcileNormal(ctx, pr)
			if (err != nil) != tc.readError {
				t.Fatalf("reconcile error = %v, want read error: %t", err, tc.readError)
			}
			if !markerClient.intercepted {
				t.Fatal("test did not reach the provisional PVC write")
			}
			if got := len(recorder.podDeleteRequests) != 0; got != tc.allowed {
				t.Fatalf("Pod deletion requested = %t, want %t", got, tc.allowed)
			}
			current := &corev1.PersistentVolumeClaim{}
			key := client.ObjectKey{Namespace: "test", Name: "claim"}
			if err := base.Get(ctx, key, current); err != nil {
				t.Fatal(err)
			}
			if !current.DeletionTimestamp.IsZero() {
				t.Fatal("PVC deletion started while the Pod's finalizer still held it")
			}
			if !tc.allowed && current.Annotations[deletionCommitFinalizedAnnotation] != "" {
				t.Fatal("invalid fencing crossed the finalized-token boundary")
			}
			if !tc.allowed && !tc.readError {
				if current.Annotations[remediationv1.SafeToDeleteAnnotation] != "" ||
					current.Annotations[deletionCommitTokenAnnotation] != "" {
					t.Fatal("rejected fencing retained consent or a provisional token")
				}
				commitKey := client.ObjectKey{Namespace: key.Namespace, Name: deletionCommitName(string(current.UID))}
				if err := base.Get(ctx, commitKey, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
					t.Fatalf("rejected fencing retained the prepared record: %v", err)
				}
			}
		})
	}
}

func changeFencingBeforeCommit(t *testing.T, ctx context.Context, r *PodRemediatorReconciler, change string) {
	t.Helper()
	if change == "" {
		return
	}
	if change == "node read fails" {
		r.APIReader = nodeReadFailureReader{Reader: r.APIReader}
		return
	}
	if change == "node recovers" || change == "node is replaced" || change == "node disappears" {
		node := &corev1.Node{}
		if err := r.Get(ctx, client.ObjectKey{Name: "worker-0"}, node); err != nil {
			t.Fatal(err)
		}
		if change == "node recovers" {
			node.Status.Conditions[0].Status = corev1.ConditionTrue
			if err := r.Status().Update(ctx, node); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := r.Delete(ctx, node); err != nil {
				t.Fatal(err)
			}
			if change == "node is replaced" {
				node.UID = types.UID("replacement-node-uid")
				node.ResourceVersion = ""
				if err := r.Create(ctx, node); err != nil {
					t.Fatal(err)
				}
			}
		}
		return
	}
	resource := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test")
	snr, err := resource.Get(ctx, "worker-0-snr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	switch change {
	case "SNR disappears", "SNR is replaced":
		if err := resource.Delete(ctx, snr.GetName(), metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if change == "SNR disappears" {
			return
		}
		fallthrough
	case "multiple SNRs":
		if change == "multiple SNRs" {
			snr.SetName("another-snr")
		}
		snr.SetUID(types.UID("another-snr-uid"))
		snr.SetResourceVersion("")
		if _, err := resource.Create(ctx, snr, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		return
	case "SNR is deleting":
		now := metav1.Now()
		snr.SetDeletionTimestamp(&now)
	case "SNR phase regresses":
		if err := unstructured.SetNestedField(snr.Object, "Pre-Reboot-Completed", "status", "phase"); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown fencing change %q", change)
	}
	if _, err := resource.Update(ctx, snr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

type nodeReadFailureReader struct {
	client.Reader
}

func (r nodeReadFailureReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Node); ok {
		return fmt.Errorf("injected Node read failure")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestPendingDeletionAllowsOtherPVCProgress(t *testing.T) {
	for _, tc := range []struct {
		pending string
		action  string
	}{
		{pending: "Pod finalizer", action: "request"},
		{pending: "PVC finalizer", action: "request"},
		{pending: "replacement Pod", action: "request"},
		{pending: "Pod finalizer", action: "consented deletion"},
		{pending: "replacement Pod", action: "consented deletion"},
		{pending: "Pod finalizer", action: "recovery"},
	} {
		for _, otherNamespace := range []string{"test", "workload"} {
			t.Run(tc.pending+"/"+tc.action+"/"+otherNamespace, func(t *testing.T) {
				ctx := context.Background()
				r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
				pr.Spec.Namespaces = []string{"test", "workload"}
				pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
				original := &corev1.PersistentVolumeClaim{}
				if err := r.Get(ctx, pvcKey, original); err != nil {
					t.Fatal(err)
				}
				original.Finalizers = []string{"kubernetes.io/pvc-protection"}
				if err := r.Update(ctx, original); err != nil {
					t.Fatal(err)
				}
				if tc.pending == "Pod finalizer" {
					pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/hold"})
					if err := r.Create(ctx, pod); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := r.reconcileNormal(ctx, pr); err != nil {
					t.Fatal(err)
				}
				if tc.pending == "replacement Pod" {
					if err := r.Create(ctx, podUsingClaim("replacement", "worker-0", "replacement-uid", nil)); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.Get(ctx, pvcKey, original); err != nil {
					t.Fatal(err)
				}
				committedToken := original.Annotations[deletionCommitFinalizedAnnotation]
				if committedToken == "" {
					t.Fatal("first PVC was not committed")
				}
				pv := &corev1.PersistentVolume{}
				if err := r.Get(ctx, client.ObjectKey{Name: "pv"}, pv); err != nil {
					t.Fatal(err)
				}
				pv.Name, pv.ResourceVersion, pv.UID = "other-pv", "", ""
				if err := r.Create(ctx, pv); err != nil {
					t.Fatal(err)
				}
				other := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "other-claim", Namespace: otherNamespace, UID: types.UID("other-pvc-uid")},
					Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pv.Name},
				}
				if tc.action != "request" {
					other.Annotations = map[string]string{
						remediationv1.PVCStuckOnNodeAnnotation: "worker-0",
						remediationv1.RemediatorUIDAnnotation:  string(pr.UID),
						remediationv1.FencingNodeUIDAnnotation: "node-uid",
						remediationv1.RequestIDAnnotation:      "pr-uid:other-pvc-uid:snr-uid",
						remediationv1.ConsentIDAnnotation:      "pr-uid:other-pvc-uid:snr-uid",
						remediationv1.SafeToDeleteAnnotation:   "true",
					}
				}
				if err := r.Create(ctx, other); err != nil {
					t.Fatal(err)
				}
				if tc.action == "recovery" {
					changeFencingBeforeCommit(t, ctx, r, "node recovers")
				}
				for attempt := 0; attempt < 3; attempt++ {
					result, err := r.reconcileNormal(ctx, pr)
					if err != nil {
						t.Fatal(err)
					}
					if result.RequeueAfter != DefaultConsentPollInterval {
						t.Fatalf("pending cleanup requeues after %s", result.RequeueAfter)
					}
				}
				err := r.Get(ctx, client.ObjectKeyFromObject(other), other)
				if tc.action == "consented deletion" {
					if !apierrors.IsNotFound(err) {
						t.Fatalf("independent consented PVC was not deleted: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if tc.action == "request" && other.Annotations[remediationv1.RequestIDAnnotation] == "" {
						t.Fatal("independent PVC never received a request")
					}
					if tc.action == "recovery" && len(other.Annotations) != 0 {
						t.Fatalf("recovered PVC retained its handshake: %v", other.Annotations)
					}
				}
				if err := r.Get(ctx, pvcKey, original); err != nil {
					t.Fatal(err)
				}
				if original.Annotations[deletionCommitFinalizedAnnotation] != committedToken ||
					original.Annotations[remediationv1.RequestIDAnnotation] == "" {
					t.Fatal("normal scanning changed the pending committed PVC's handshake")
				}
				if tc.pending == "replacement Pod" {
					ready := pr.Status.Conditions.Get(condition.ReadyCondition)
					if ready == nil || string(ready.Reason) != ReplacementPodBlocksPVCDeletionReason {
						t.Fatalf("lost replacement Pod status: %v", ready)
					}
				}
			})
		}
	}
}
