/*
Copyright 2025.

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
	"testing"
	"time"

	"github.com/go-logr/logr"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// remediationFixture builds a reconciler and CR for controller unit tests.
func remediationFixture(t *testing.T, nodes ...client.Object) (*PodRemediatorReconciler, *remediationv1.PodRemediator) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := remediationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv"}, Spec: corev1.PersistentVolumeSpec{
		PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: "/data"}},
		NodeAffinity:           &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{"worker-0"}}}}}}},
	}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "test", UID: types.UID("pvc-uid")}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv"}}
	pr := &remediationv1.PodRemediator{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "test", UID: types.UID("pr-uid")}}
	pvc.Annotations = map[string]string{
		remediationv1.PVCStuckOnNodeAnnotation: "worker-0",
		remediationv1.SafeToDeleteAnnotation:   "true",
		remediationv1.RequestIDAnnotation:      "pr-uid:pvc-uid:snr-uid",
		remediationv1.ConsentIDAnnotation:      "pr-uid:pvc-uid:snr-uid",
		remediationv1.RemediatorUIDAnnotation:  "pr-uid",
		remediationv1.FencingNodeUIDAnnotation: "node-uid",
	}
	objects := append([]client.Object{pv, pvc}, nodes...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	nhc := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "remediation.medik8s.io/v1alpha1", "kind": "NodeHealthCheck", "metadata": map[string]interface{}{"name": "nhc"}}}
	template := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "self-node-remediation.medik8s.io/v1alpha1", "kind": "SelfNodeRemediationTemplate", "metadata": map[string]interface{}{"name": "template", "namespace": "test"}}}
	snr := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "self-node-remediation.medik8s.io/v1alpha1", "kind": "SelfNodeRemediation", "metadata": map[string]interface{}{"name": "worker-0-snr", "namespace": "test", "uid": "snr-uid", "annotations": map[string]interface{}{"remediation.medik8s.io/node-name": "worker-0"}}, "status": map[string]interface{}{"phase": "Fencing-Completed"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvrNodeHealthCheck: "NodeHealthCheckList", gvrSelfNodeRemediationTemplate: "SelfNodeRemediationTemplateList", gvrSelfNodeRemediation: "SelfNodeRemediationList"}, nhc, template, snr)
	return &PodRemediatorReconciler{Client: c, DynamicClient: dyn}, pr
}

// remediationNode returns a test Node with the requested Ready status.
func remediationNode(ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0", UID: types.UID("node-uid")}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}
}

func TestRejectNonExclusiveAffinity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		op     corev1.NodeSelectorOperator
		values []string
	}{
		{"NotIn", corev1.NodeSelectorOpNotIn, []string{"worker-0"}},
		{"MultipleNodes", corev1.NodeSelectorOpIn, []string{"worker-0", "worker-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pv := &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "network.csi"}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: tc.op, Values: tc.values}}}}}}}}
			if isLocalPV(pv) && getLocalPVNodeName(pv, logr.Discard()) == "worker-0" {
				t.Fatal("PV accepted as exclusively local to worker-0 despite non-exclusive/excluding affinity")
			}
		})
	}
}

func TestDeletedNodeWaitsForLiveFencing(t *testing.T) {
	r, pr := remediationFixture(t)
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(context.Background(), "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	err := r.Get(context.Background(), client.ObjectKey{Namespace: "test", Name: "claim"}, &corev1.PersistentVolumeClaim{})
	if err != nil {
		t.Fatalf("PVC was deleted without live SNR fencing: %v", err)
	}
}

func TestForeignRemediatorOwnsPVC(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations[remediationv1.RemediatorUIDAnnotation] = "other-pr-uid"
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
		t.Fatal(err)
	}
	if got := pvc.Annotations[remediationv1.RemediatorUIDAnnotation]; got != "other-pr-uid" {
		t.Fatalf("foreign PVC owner changed to %q", got)
	}
}

func TestDisabledRecoveryInvalidatesConsent(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionTrue))
	pr.Spec.Disabled = true
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: "worker-0"}, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	if err := r.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Disabled = false
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("PVC deleted using consent from fault before disabled recovery: %v", err)
	}
}

func TestPodRemediatorOnlyScansOwnNamespace(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Namespace = "unrelated"
	pvc.ResourceVersion = ""
	if err := r.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "unrelated", Name: "claim"}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("PodRemediator scanned unrelated namespace: %v", err)
	}
}

func TestFencingRequiredForDeletion(t *testing.T) {
	for _, tc := range []struct {
		name, phase                     string
		deleteSNR, terminating, allowed bool
	}{
		{name: "missing SNR", deleteSNR: true},
		{name: "missing status"},
		{name: "unknown phase", phase: "Unknown"},
		{name: "fencing started", phase: "Fencing-Started"},
		{name: "pre reboot", phase: "Pre-Reboot-Completed"},
		{name: "reboot complete", phase: "Reboot-Completed", allowed: true},
		{name: "fencing complete", phase: "Fencing-Completed", allowed: true},
		{name: "deleting SNR", phase: "Fencing-Completed", terminating: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test"}, Spec: corev1.PodSpec{NodeName: "worker-0", Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}}}}
			if err := r.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			resource := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test")
			snr, err := resource.Get(ctx, "worker-0-snr", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Even an elapsed reboot deadline does not replace a confirmed phase.
			if err := unstructured.SetNestedField(snr.Object, time.Now().Add(-time.Hour).Format(time.RFC3339), "status", "timeAssumedRebooted"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedField(snr.Object, tc.phase, "status", "phase"); err != nil {
				t.Fatal(err)
			}
			if tc.terminating {
				now := metav1.Now()
				snr.SetDeletionTimestamp(&now)
			}
			if _, err := resource.Update(ctx, snr, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if tc.deleteSNR {
				if err := resource.Delete(ctx, snr.GetName(), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.reconcileNormal(ctx, pr); err != nil {
				t.Fatal(err)
			}
			for _, obj := range []client.Object{&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "test"}}, pod} {
				err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj)
				if tc.allowed && !apierrors.IsNotFound(err) {
					t.Fatalf("fenced resource not deleted: %T: %v", obj, err)
				}
				if !tc.allowed && err != nil {
					t.Fatalf("unfenced resource was deleted: %T: %v", obj, err)
				}
			}
		})
	}
}

func TestConsentWaitsForLiveFencedSNR(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	resource := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test")
	snr, err := resource.Get(ctx, "worker-0-snr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(snr.Object, "Fencing-Started", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if _, err := resource.Update(ctx, snr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("consent was used while SNR fencing was incomplete: %v", err)
	}
}

func TestSNRNodeNameFallback(t *testing.T) {
	snr := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "worker-0"},
	}}
	if got := snrNodeName(snr); got != "worker-0" {
		t.Fatalf("SNR node name = %q, want metadata.name fallback", got)
	}
	if !fencingMatchesNode(snr, remediationNode(corev1.ConditionTrue)) {
		t.Fatal("metadata.name fallback was not accepted for the matching node")
	}
}

func TestUnannotatedPVCWaitingForFencingUsesConsentPoll(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pr.Spec.ConsentPollInterval = &metav1.Duration{Duration: 17 * time.Second}
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: "test", Name: "claim"}
	if err := r.Get(ctx, key, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations = nil
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := r.reconcileNormal(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != 17*time.Second {
		t.Fatalf("waiting for fencing requeued after %s, want consent poll", result.RequeueAfter)
	}
	if err := r.Get(ctx, key, pvc); err != nil {
		t.Fatal(err)
	}
	if len(pvc.Annotations) != 0 {
		t.Fatalf("PVC annotated before fencing: %v", pvc.Annotations)
	}
}

func TestAffinityAlternatives(t *testing.T) {
	term := func(key, node string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}}
	}
	for _, tc := range []struct {
		name  string
		terms []corev1.NodeSelectorTerm
		want  string
	}{
		{name: "no terms"},
		{name: "one node", terms: []corev1.NodeSelectorTerm{term(corev1.LabelHostname, "worker-0")}, want: "worker-0"},
		{name: "topolvm", terms: []corev1.NodeSelectorTerm{term("topology.topolvm.io/node", "worker-0")}, want: "worker-0"},
		{name: "same node alternatives", terms: []corev1.NodeSelectorTerm{term(corev1.LabelHostname, "worker-0"), term(corev1.LabelHostname, "worker-0")}, want: "worker-0"},
		{name: "other node alternative", terms: []corev1.NodeSelectorTerm{term(corev1.LabelHostname, "worker-0"), term(corev1.LabelHostname, "worker-1")}},
		{name: "zone alternative", terms: []corev1.NodeSelectorTerm{term(corev1.LabelHostname, "worker-0"), term("topology.kubernetes.io/zone", "zone-a")}},
		{name: "empty alternative", terms: []corev1.NodeSelectorTerm{term(corev1.LabelHostname, "worker-0"), {}}},
		{name: "conflicting keys", terms: []corev1.NodeSelectorTerm{{MatchExpressions: append(term(corev1.LabelHostname, "worker-0").MatchExpressions, term("topology.topolvm.io/node", "worker-1").MatchExpressions...)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pv := &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: "/data"}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: tc.terms}}}}
			if got := getLocalPVNodeName(pv, logr.Discard()); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if isLocalPV(pv) != (tc.want != "") {
				t.Fatal("locality classification disagrees with exclusive node pinning")
			}
		})
	}
}

func TestDisabledCleanupWithoutDependencies(t *testing.T) {
	ctx := context.Background()
	for _, invalidInterval := range []bool{false, true} {
		r, pr := remediationFixture(t, remediationNode(corev1.ConditionTrue))
		pr.Spec.Disabled = true
		if invalidInterval {
			pr.Spec.ConsentPollInterval = &metav1.Duration{Duration: time.Millisecond}
		}
		if err := r.DynamicClient.Resource(gvrNodeHealthCheck).Delete(ctx, "nhc", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.reconcileNormal(ctx, pr); err != nil {
			t.Fatal(err)
		}
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, pvc); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{remediationv1.PVCStuckOnNodeAnnotation, remediationv1.SafeToDeleteAnnotation} {
			if _, ok := pvc.Annotations[key]; ok {
				t.Fatalf("disabled CR retained %s without dependencies (invalid interval=%v)", key, invalidInterval)
			}
		}
	}
}
