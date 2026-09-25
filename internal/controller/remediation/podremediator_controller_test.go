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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	commonhelper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
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
	if err := appsv1.AddToScheme(scheme); err != nil {
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
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&remediationv1.PodRemediator{}).
		WithObjects(objects...).
		Build()
	nhc := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "remediation.medik8s.io/v1alpha1", "kind": "NodeHealthCheck", "metadata": map[string]interface{}{"name": "nhc"}}}
	template := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "self-node-remediation.medik8s.io/v1alpha1", "kind": "SelfNodeRemediationTemplate", "metadata": map[string]interface{}{"name": "template", "namespace": "test"}}}
	snr := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "self-node-remediation.medik8s.io/v1alpha1", "kind": "SelfNodeRemediation", "metadata": map[string]interface{}{"name": "worker-0-snr", "namespace": "test", "uid": "snr-uid", "annotations": map[string]interface{}{"remediation.medik8s.io/node-name": "worker-0"}}, "status": map[string]interface{}{"phase": "Fencing-Completed"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvrNodeHealthCheck: "NodeHealthCheckList", gvrSelfNodeRemediationTemplate: "SelfNodeRemediationTemplateList", gvrSelfNodeRemediation: "SelfNodeRemediationList"}, nhc, template, snr)
	return &PodRemediatorReconciler{Client: c, DynamicClient: dyn, Scheme: scheme}, pr
}

// remediationNode returns a test Node with the requested Ready status.
func remediationNode(ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0", UID: types.UID("node-uid"), Labels: map[string]string{corev1.LabelHostname: "worker-0"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}
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

func TestDeletedNodeHostnameAffinityDoesNotAuthorizeConsentedDeletion(t *testing.T) {
	ctx := context.Background()
	otherNode := remediationNode(corev1.ConditionFalse)
	otherNode.Name = "worker-1"
	otherNode.UID = types.UID("other-node-uid")
	otherNode.Labels[corev1.LabelHostname] = "worker-1"
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse), otherNode)
	if err := r.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0"}}); err != nil {
		t.Fatal(err)
	}

	// Another unhealthy Node keeps the scan active. The live SNR and consent
	// identify worker-0, but its hostname label cannot be resolved after the
	// worker-0 Node object has disappeared.
	pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, gotPVC); err != nil {
		t.Fatalf("PVC was deleted using hostname affinity after its Node disappeared: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("PVC received a deletion request using an unresolved hostname")
	}
	gotPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("Pod was deleted using hostname affinity after its Node disappeared: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("Pod received a deletion request using an unresolved hostname")
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
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{NodeName: "worker-0", Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}}}}
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

func TestCommittedPVCDeletionResumesAfterStateChanges(t *testing.T) {
	const podCleanupFinalizer = "test.example/pod-cleanup"
	const pvcProtectionFinalizer = "kubernetes.io/pvc-protection"

	for _, tc := range []struct {
		name        string
		changeState func(context.Context, *testing.T, *PodRemediatorReconciler, *remediationv1.PodRemediator)
	}{
		{
			name: "SNR disappears",
			changeState: func(ctx context.Context, t *testing.T, r *PodRemediatorReconciler, _ *remediationv1.PodRemediator) {
				t.Helper()
				if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "node recovers",
			changeState: func(ctx context.Context, t *testing.T, r *PodRemediatorReconciler, _ *remediationv1.PodRemediator) {
				t.Helper()
				node := &corev1.Node{}
				if err := r.Get(ctx, client.ObjectKey{Name: "worker-0"}, node); err != nil {
					t.Fatal(err)
				}
				node.Status.Conditions[0].Status = corev1.ConditionTrue
				if err := r.Status().Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "PodRemediator is disabled",
			changeState: func(_ context.Context, _ *testing.T, _ *PodRemediatorReconciler, pr *remediationv1.PodRemediator) {
				pr.Spec.Disabled = true
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
			pvc := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, pvcKey, pvc); err != nil {
				t.Fatal(err)
			}
			pvc.Finalizers = []string{pvcProtectionFinalizer}
			if err := r.Update(ctx, pvc); err != nil {
				t.Fatal(err)
			}

			podKey := client.ObjectKey{Namespace: "test", Name: "pod"}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace, UID: types.UID("original-pod-uid"), Finalizers: []string{podCleanupFinalizer}},
				Spec: corev1.PodSpec{
					NodeName: "worker-0",
					Volumes:  []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcKey.Name}}}},
				},
			}
			if err := r.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}

			result, err := r.reconcileNormal(ctx, pr)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != DefaultConsentPollInterval {
				t.Fatalf("newly committed deletion requeued after %s, want %s", result.RequeueAfter, DefaultConsentPollInterval)
			}
			committedPVC := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, pvcKey, committedPVC); err != nil {
				t.Fatal(err)
			}
			if got, want := committedPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation], committedPVCDeletionValue(string(committedPVC.UID), string(pr.UID), "worker-0"); got != want {
				t.Fatalf("committed deletion marker = %q, want %q", got, want)
			}

			tc.changeState(ctx, t, r, pr)
			result, err = r.reconcileNormal(ctx, pr)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != DefaultConsentPollInterval {
				t.Fatalf("resumed deletion requeued after %s, want %s", result.RequeueAfter, DefaultConsentPollInterval)
			}
			terminatingPod := &corev1.Pod{}
			if err := r.Get(ctx, podKey, terminatingPod); err != nil {
				t.Fatalf("referencing Pod disappeared before its cleanup finalizer was released: %v", err)
			}
			if terminatingPod.DeletionTimestamp.IsZero() {
				t.Fatal("committed deletion did not request cleanup of the referencing Pod")
			}
			terminatingPVC := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, pvcKey, terminatingPVC); err != nil {
				t.Fatalf("PVC disappeared while a Pod still referenced it: %v", err)
			}
			if !terminatingPVC.DeletionTimestamp.IsZero() {
				t.Fatal("PVC deletion started while a referencing Pod was still present")
			}
			if got := terminatingPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation]; got == "" {
				t.Fatal("committed deletion marker was lost while Pod cleanup was pending")
			}

			// Model the Pod controller finishing cleanup. The PVC-protection finalizer
			// keeps the claim terminating after the referencing Pod has gone away.
			terminatingPod.Finalizers = nil
			if err := r.Update(ctx, terminatingPod); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, podKey, terminatingPod); err == nil {
				if err := r.Delete(ctx, terminatingPod, observedDeleteOptions(terminatingPod)); err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
			} else if !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}

			result, err = r.reconcileNormal(ctx, pr)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != DefaultConsentPollInterval {
				t.Fatalf("PVC-protection wait requeued after %s, want %s", result.RequeueAfter, DefaultConsentPollInterval)
			}
			if err := r.Get(ctx, podKey, &corev1.Pod{}); !apierrors.IsNotFound(err) {
				t.Fatalf("referencing Pod was not cleaned up: %v", err)
			}
			terminatingPVC = &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, pvcKey, terminatingPVC); err != nil {
				t.Fatalf("PVC-protection finalizer did not retain the committed PVC: %v", err)
			}
			if terminatingPVC.DeletionTimestamp.IsZero() {
				t.Fatal("committed PVC deletion was abandoned after the Pod disappeared")
			}
			if got := terminatingPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation]; got == "" {
				t.Fatal("committed deletion marker was lost while PVC protection was pending")
			}
		})
	}
}

func TestConsentRevocationAtPVCDeletionCommitBoundaryPreventsCleanup(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/pod-cleanup"})
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}

	baseClient := r.Client
	revokedResourceVersionChanged := false
	clientAtCommit := &commitBoundaryClient{
		Client: baseClient,
		beforeCommit: func(ctx context.Context) {
			current := &corev1.PersistentVolumeClaim{}
			if err := baseClient.Get(ctx, pvcKey, current); err != nil {
				t.Fatalf("get PVC at commit boundary: %v", err)
			}
			validatedResourceVersion := current.ResourceVersion
			delete(current.Annotations, remediationv1.SafeToDeleteAnnotation)
			delete(current.Annotations, remediationv1.ConsentIDAnnotation)
			if err := baseClient.Update(ctx, current); err != nil {
				t.Fatalf("revoke PVC consent at commit boundary: %v", err)
			}
			revoked := &corev1.PersistentVolumeClaim{}
			if err := baseClient.Get(ctx, pvcKey, revoked); err != nil {
				t.Fatalf("get PVC after revoking consent: %v", err)
			}
			revokedResourceVersionChanged = revoked.ResourceVersion != validatedResourceVersion
		},
	}
	r.Client = clientAtCommit

	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		// A resource-version conflict is a valid way for the controller to abort
		// after the competing consent update. The assertions below verify safety.
		t.Logf("reconcile stopped after consent revocation: %v", err)
	}
	if !clientAtCommit.intercepted {
		t.Fatal("test did not revoke consent at the PVC deletion commit boundary")
	}
	if !revokedResourceVersionChanged {
		t.Fatal("consent revocation did not advance the PVC resource version")
	}
	for key, active := range clientAtCommit.activeCommitRecords {
		if active {
			t.Fatalf("authoritative PVC deletion commit %s survived consent revocation", key)
		}
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared after consent was revoked: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("PVC received a deletion request after its consent was revoked")
	}
	if got := gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation]; got != "" {
		t.Fatalf("PVC retained a committed-deletion marker after consent revocation: %q", got)
	}
	if got := gotPVC.Annotations[deletionCommitTokenAnnotation]; got != "" {
		t.Fatalf("PVC retained a provisional commit token after consent revocation: %q", got)
	}
	if _, ok := gotPVC.Annotations[remediationv1.SafeToDeleteAnnotation]; ok {
		t.Fatal("revoked safe-to-delete consent was restored")
	}

	gotPod := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("Pod disappeared after its claim's consent was revoked: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("Pod received a deletion request after its claim's consent was revoked")
	}
	commit := &corev1.ConfigMap{}
	err := baseClient.Get(ctx, client.ObjectKey{Namespace: pvcKey.Namespace, Name: deletionCommitName(string(pvc.UID))}, commit)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("PVC deletion commit ConfigMap survived consent revocation: %v", err)
	}
}

func TestConsentRevocationAfterPVCMarkerWritePreventsCommitPromotion(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/pod-cleanup"})
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}

	baseClient := r.Client
	markerWasWritten := false
	consentWasRevoked := false
	markerClient := &pvcMarkerRevocationClient{
		Client: baseClient,
		afterMarkerWrite: func(ctx context.Context) {
			current := &corev1.PersistentVolumeClaim{}
			if err := baseClient.Get(ctx, pvcKey, current); err != nil {
				t.Fatalf("get PVC after conditional marker write: %v", err)
			}
			if current.Annotations[remediationv1.PVCDeletionCommittedAnnotation] == "" || current.Annotations[deletionCommitTokenAnnotation] == "" {
				t.Fatal("test hook ran before the PVC marker and token were written")
			}
			markerWasWritten = true
			previousResourceVersion := current.ResourceVersion
			delete(current.Annotations, remediationv1.SafeToDeleteAnnotation)
			delete(current.Annotations, remediationv1.ConsentIDAnnotation)
			if err := baseClient.Update(ctx, current); err != nil {
				t.Fatalf("revoke consent after PVC marker write: %v", err)
			}
			revoked := &corev1.PersistentVolumeClaim{}
			if err := baseClient.Get(ctx, pvcKey, revoked); err != nil {
				t.Fatalf("get PVC after consent revocation: %v", err)
			}
			if revoked.ResourceVersion == previousResourceVersion {
				t.Fatal("consent revocation did not advance the PVC resource version")
			}
			consentWasRevoked = true
		},
	}
	deleteRecorder := &podDeleteRecordingClient{Client: markerClient}
	r.Client = deleteRecorder
	r.APIReader = baseClient

	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		// A conflict after the competing consent update also safely aborts.
		t.Logf("reconcile stopped after consent revocation: %v", err)
	}
	if !markerClient.intercepted || !markerWasWritten || !consentWasRevoked {
		t.Fatal("test did not revoke consent after the conditional PVC marker write")
	}
	if len(deleteRecorder.podDeleteRequests) != 0 {
		t.Fatalf("Pod received %d delete requests after consent was revoked", len(deleteRecorder.podDeleteRequests))
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared after consent was revoked: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("PVC received a deletion request after consent was revoked")
	}
	if _, ok := gotPVC.Annotations[remediationv1.SafeToDeleteAnnotation]; ok {
		t.Fatal("revoked safe-to-delete consent was restored")
	}
	if gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" || gotPVC.Annotations[deletionCommitTokenAnnotation] != "" ||
		gotPVC.Annotations[deletionCommitFinalizedAnnotation] != "" {
		t.Fatal("PVC retained partial deletion authority after rejected commit")
	}
	gotPod := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("Pod disappeared after its claim's consent was revoked: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("Pod received a deletion request after its claim's consent was revoked")
	}
	commit := &corev1.ConfigMap{}
	commitKey := client.ObjectKey{Namespace: pvcKey.Namespace, Name: deletionCommitName(string(pvc.UID))}
	if err := baseClient.Get(ctx, commitKey, commit); !apierrors.IsNotFound(err) {
		t.Fatalf("PVC deletion commit survived consent revocation: %v", err)
	}
}

func TestPreparedDeletionCommitDoesNotPromoteAfterConsentRevocation(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	validatedPVCResourceVersion := pvc.ResourceVersion
	requestID := pvc.Annotations[remediationv1.RequestIDAnnotation]
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/pod-cleanup"})
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	state := deletionCommit{
		PVCName: pvc.Name, PVCUID: string(pvc.UID), PVCResourceVersion: validatedPVCResourceVersion,
		RequestID: requestID, RemediatorUID: string(pr.UID), Node: "worker-0", Token: token,
		Phase: deletionCommitPhasePrepared, Pods: []committedPod{{Name: pod.Name, UID: string(pod.UID)}},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: deletionCommitName(string(pvc.UID)), Namespace: pvc.Namespace,
		Annotations: map[string]string{deletionCommitAnnotation: string(encoded)},
	}}
	if err := r.Create(ctx, prepared); err != nil {
		t.Fatal(err)
	}

	oldPVC := pvc.DeepCopy()
	pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = committedPVCDeletionValue(string(pvc.UID), string(pr.UID), "worker-0")
	pvc.Annotations[deletionCommitTokenAnnotation] = token
	if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil {
		t.Fatalf("write conditional PVC marker from prepared-commit fixture: %v", err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
	delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatalf("remove consent before prepared-commit resume: %v", err)
	}

	baseClient := r.Client
	deleteRecorder := &podDeleteRecordingClient{Client: baseClient}
	r.Client = deleteRecorder
	r.APIReader = baseClient
	pr.Spec.Disabled = true // resume runs before the disabled scan/cleanup path
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatalf("reconcile prepared commit after consent revocation: %v", err)
	}
	if len(deleteRecorder.podDeleteRequests) != 0 {
		t.Fatalf("prepared commit force-deleted a Pod %d time(s) after consent was revoked", len(deleteRecorder.podDeleteRequests))
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared after prepared commit lost consent: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("prepared commit requested PVC deletion after consent was revoked")
	}
	if gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" || gotPVC.Annotations[deletionCommitTokenAnnotation] != "" ||
		gotPVC.Annotations[deletionCommitFinalizedAnnotation] != "" {
		t.Fatal("PVC retained its provisional marker/token after prepared commit lost consent")
	}
	if _, ok := gotPVC.Annotations[remediationv1.SafeToDeleteAnnotation]; ok {
		t.Fatal("resume restored revoked safe-to-delete consent")
	}
	gotPod := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("Pod disappeared after prepared commit lost consent: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("prepared commit requested Pod deletion after consent was revoked")
	}
	commit := &corev1.ConfigMap{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(prepared), commit); !apierrors.IsNotFound(err) {
		t.Fatalf("prepared deletion commit survived rejection after consent revocation: %v", err)
	}
}

func TestFinalizedPreparedDeletionCommitResumesAfterConsentRevocation(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{"test.example/pod-cleanup"})
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	const token = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	state := deletionCommit{
		PVCName: pvc.Name, PVCUID: string(pvc.UID), PVCResourceVersion: pvc.ResourceVersion,
		RequestID:     pvc.Annotations[remediationv1.RequestIDAnnotation],
		RemediatorUID: string(pr.UID), Node: "worker-0", Token: token,
		Phase: deletionCommitPhasePrepared, Pods: []committedPod{{Name: pod.Name, UID: string(pod.UID)}},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: deletionCommitName(string(pvc.UID)), Namespace: pvc.Namespace,
		Annotations: map[string]string{deletionCommitAnnotation: string(encoded)},
	}}
	if err := r.Create(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	oldPVC := pvc.DeepCopy()
	pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] = committedPVCDeletionValue(string(pvc.UID), string(pr.UID), "worker-0")
	pvc.Annotations[deletionCommitTokenAnnotation] = token
	if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(oldPVC, client.MergeFromWithOptimisticLock{})); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	markedPVC := pvc.DeepCopy()
	pvc.Annotations[deletionCommitFinalizedAnnotation] = token
	if err := r.Patch(ctx, pvc, client.MergeFromWithOptions(markedPVC, client.MergeFromWithOptimisticLock{})); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	delete(pvc.Annotations, remediationv1.SafeToDeleteAnnotation)
	delete(pvc.Annotations, remediationv1.ConsentIDAnnotation)
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	baseClient := r.Client
	deleteRecorder := &podDeleteRecordingClient{Client: baseClient}
	r.Client = deleteRecorder
	r.APIReader = baseClient
	pr.Spec.Disabled = true
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatalf("resume finalized deletion after consent revocation: %v", err)
	}
	if len(deleteRecorder.podDeleteRequests) != 1 {
		t.Fatalf("finalized deletion issued %d Pod deletes, want one", len(deleteRecorder.podDeleteRequests))
	}
	gotPod := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatal(err)
	}
	if gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("finalized deletion did not request Pod cleanup")
	}
	gotCommit := &corev1.ConfigMap{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(prepared), gotCommit); err != nil {
		t.Fatal(err)
	}
	committedState, err := decodeDeletionCommit(gotCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !committedDeletion(committedState) || committedState.Token != token {
		t.Fatal("finalized deletion did not promote matching prepared authority")
	}
}

func TestStatefulSetReplacementBeforePVCEmptyListCheckAbortsCleanup(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	baseClient := r.Client
	statefulSet, originalPod, replacement := createStatefulSetReplacementWorkload(t, baseClient)
	if err := baseClient.Create(ctx, originalPod); err != nil {
		t.Fatal(err)
	}
	replacementClient := &statefulSetReplacementClient{
		Client: baseClient,
		afterPodDelete: func(ctx context.Context, deleted *corev1.Pod) {
			if deleted.Name != originalPod.Name {
				t.Fatalf("replacement hook observed Pod %q, want StatefulSet ordinal %q", deleted.Name, originalPod.Name)
			}
			if err := baseClient.Create(ctx, replacement); err != nil {
				t.Fatalf("StatefulSet with replicas=%d recreated ordinal before the empty-list check: %v", *statefulSet.Spec.Replicas, err)
			}
		},
	}
	deleteRecorder := &podDeleteRecordingClient{Client: replacementClient}
	r.Client = deleteRecorder
	r.APIReader = baseClient

	result, err := r.reconcileNormal(ctx, pr)
	if err != nil {
		t.Fatalf("reconcile with replacement Pod before empty-list check: %v", err)
	}
	if result.RequeueAfter != DefaultConsentPollInterval && result.RequeueAfter != DefaultPeriodicPollInterval {
		t.Fatalf("cleanup requeued after unexpected interval %s with a replacement Pod", result.RequeueAfter)
	}
	if !replacementClient.replacementCreated {
		t.Fatal("StatefulSet replacement was not created after force-deleting the original Pod")
	}
	if replacementClient.pvcDeleteIntercepted {
		t.Fatal("PVC deletion started despite a replacement Pod being present before the empty-list check")
	}
	if len(deleteRecorder.podDeleteRequests) != 1 {
		t.Fatalf("controller issued %d Pod deletes before observing the replacement, want only the original Pod delete", len(deleteRecorder.podDeleteRequests))
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared while replacement Pod used it: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("PVC received a deletion request while the replacement Pod used it")
	}
	gotReplacement := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(replacement), gotReplacement); err != nil {
		t.Fatalf("StatefulSet replacement Pod was deleted: %v", err)
	}
	if gotReplacement.UID != replacement.UID || !gotReplacement.DeletionTimestamp.IsZero() {
		t.Fatal("controller changed or deleted the StatefulSet replacement Pod")
	}

	// The next reconcile must revalidate the new Pod UID and abort before the
	// PVC delete becomes irreversible. Disable the scanner so the fixture cannot
	// start a fresh consent request after that abort.
	pr.Spec.Disabled = true
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatalf("reconcile after replacement invalidated the prepared Pod set: %v", err)
	}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared after safely aborting replacement-Pod cleanup: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() || gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" || gotPVC.Annotations[deletionCommitTokenAnnotation] != "" {
		t.Fatal("replacement Pod did not safely abort PVC cleanup before deletion")
	}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(replacement), gotReplacement); err != nil {
		t.Fatalf("replacement Pod disappeared after commit abort: %v", err)
	}
	if !gotReplacement.DeletionTimestamp.IsZero() {
		t.Fatal("replacement Pod received a delete request while the PVC was still live")
	}
	commit := &corev1.ConfigMap{}
	if err := baseClient.Get(ctx, client.ObjectKey{Namespace: pvcKey.Namespace, Name: deletionCommitName(string(pvc.UID))}, commit); !apierrors.IsNotFound(err) {
		t.Fatalf("aborted replacement-Pod cleanup retained its deletion commit: %v", err)
	}
}

func TestStatefulSetReplacementAfterPVCDeleteKeepsCommitPending(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	baseClient := r.Client
	statefulSet, originalPod, replacement := createStatefulSetReplacementWorkload(t, baseClient)
	if err := baseClient.Create(ctx, originalPod); err != nil {
		t.Fatal(err)
	}
	replacementClient := &statefulSetReplacementClient{
		Client: baseClient,
		beforePVCDelete: func(ctx context.Context, _ *corev1.PersistentVolumeClaim) {
			if err := baseClient.Create(ctx, replacement); err != nil {
				t.Fatalf("StatefulSet with replicas=%d recreated ordinal after the empty-list check: %v", *statefulSet.Spec.Replicas, err)
			}
		},
	}
	deleteRecorder := &podDeleteRecordingClient{Client: replacementClient}
	r.Client = deleteRecorder
	r.APIReader = baseClient

	result, err := r.reconcileNormal(ctx, pr)
	if err != nil {
		t.Fatalf("reconcile with replacement racing PVC deletion: %v", err)
	}
	if result.RequeueAfter != DefaultConsentPollInterval {
		t.Fatalf("cleanup requeued after %s while PVC deletion was protected, want %s", result.RequeueAfter, DefaultConsentPollInterval)
	}
	initialReady := pr.Status.Conditions.Get(condition.ReadyCondition)
	if initialReady == nil || initialReady.Status != corev1.ConditionFalse || string(initialReady.Reason) != ReplacementPodBlocksPVCDeletionReason ||
		!strings.Contains(initialReady.Message, pvcKey.String()) || !strings.Contains(initialReady.Message, replacement.Name) {
		t.Fatalf("replacement racing PVC deletion needs an actionable Ready condition, got %v", initialReady)
	}
	if !replacementClient.pvcDeleteIntercepted || !replacementClient.replacementCreated {
		t.Fatal("test did not create the replacement after the empty-list check and before the PVC delete request")
	}
	if len(deleteRecorder.podDeleteRequests) != 1 {
		t.Fatalf("controller issued %d Pod deletes, want only the original Pod force-delete", len(deleteRecorder.podDeleteRequests))
	}

	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("PVC disappeared despite its protection finalizer: %v", err)
	}
	if gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("test did not leave the PVC terminating while the replacement Pod uses it")
	}
	gotReplacement := &corev1.Pod{}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(replacement), gotReplacement); err != nil {
		t.Fatalf("replacement Pod disappeared while PVC protection held: %v", err)
	}
	if !gotReplacement.DeletionTimestamp.IsZero() {
		t.Fatal("controller force-deleted a StatefulSet replacement Pod after the PVC delete request")
	}

	// Once the deletionTimestamp exists it cannot be rolled back. The chosen
	// safety policy is to keep the committed record and report pending cleanup
	// until the workload owner releases its replacement, rather than deleting a
	// new Pod or silently forgetting the terminating PVC.
	crHelper, err := commonhelper.NewHelper(pr, r.Client, nil, r.Scheme, logr.Discard())
	if err != nil {
		t.Fatal(err)
	}
	finalizer := crHelper.GetFinalizer()
	pr.Finalizers = append(pr.Finalizers, finalizer)
	now := metav1.Now()
	pr.DeletionTimestamp = &now
	result, err = r.reconcileDelete(ctx, pr, crHelper)
	if err != nil {
		t.Fatalf("resume protected PVC with a post-delete replacement Pod: %v", err)
	}
	if result.RequeueAfter != DefaultConsentPollInterval {
		t.Fatalf("protected PVC was treated as complete after %s with its replacement still present, want pending interval %s", result.RequeueAfter, DefaultConsentPollInterval)
	}
	if !hasFinalizer(pr, finalizer) {
		t.Fatal("PodRemediator finalizer was removed while PVC protection still held the terminating claim")
	}
	ready := pr.Status.Conditions.Get(condition.ReadyCondition)
	if ready == nil || ready.Status != corev1.ConditionFalse || string(ready.Reason) != ReplacementPodBlocksPVCDeletionReason ||
		!strings.Contains(ready.Message, pvcKey.String()) || !strings.Contains(ready.Message, replacement.Name) ||
		!strings.Contains(ready.Message, "pause recreation") {
		t.Fatalf("blocked replacement needs an actionable Ready condition, got %v", ready)
	}
	commitKey := client.ObjectKey{Namespace: pvcKey.Namespace, Name: deletionCommitName(string(pvc.UID))}
	commit := &corev1.ConfigMap{}
	if err := baseClient.Get(ctx, commitKey, commit); err != nil {
		t.Fatalf("committed deletion authority was dropped while PVC protection held: %v", err)
	}
	state, err := decodeDeletionCommit(commit)
	if err != nil {
		t.Fatalf("decode retained PVC deletion commit: %v", err)
	}
	if !committedDeletion(state) {
		t.Fatalf("retained PVC deletion commit phase = %q, want committed", state.Phase)
	}
	if len(deleteRecorder.podDeleteRequests) != 1 {
		t.Fatalf("controller issued %d Pod deletes after replacement appeared, want no delete of the replacement", len(deleteRecorder.podDeleteRequests))
	}
	result, err = r.reconcileDelete(ctx, pr, crHelper)
	if err != nil || result.RequeueAfter != DefaultConsentPollInterval || !hasFinalizer(pr, finalizer) {
		t.Fatalf("committed cleanup did not remain pending on retry: result=%v error=%v", result, err)
	}
	if err := baseClient.Get(ctx, commitKey, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("committed deletion authority was dropped on retry: %v", err)
	}
	if len(deleteRecorder.podDeleteRequests) != 1 {
		t.Fatal("replacement Pod was deleted on committed cleanup retry")
	}

	// Model the StatefulSet owner later scaling down and the PVC protection
	// controller releasing the old claim. The still-persisted commit must then
	// be able to finish instead of leaving an orphaned terminating PVC.
	*statefulSet.Spec.Replicas = 0
	if err := baseClient.Update(ctx, statefulSet); err != nil {
		t.Fatalf("scale StatefulSet down to release its replacement Pod: %v", err)
	}
	if err := baseClient.Delete(ctx, gotReplacement, observedDeleteOptions(gotReplacement)); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("release replacement Pod after owner scale-down: %v", err)
	}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatalf("get terminating PVC after replacement Pod removal: %v", err)
	}
	gotPVC.Finalizers = nil
	if err := baseClient.Update(ctx, gotPVC); err != nil {
		t.Fatalf("model PVC-protection finalizer removal after last Pod reference: %v", err)
	}
	if err := baseClient.Get(ctx, pvcKey, gotPVC); err == nil {
		if err := baseClient.Delete(ctx, gotPVC, observedDeleteOptions(gotPVC)); err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("complete PVC deletion after its protection finalizer was released: %v", err)
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get PVC after protection release: %v", err)
	}
	result, err = r.reconcileDelete(ctx, pr, crHelper)
	if err != nil {
		t.Fatalf("finish committed cleanup after the StatefulSet released its replacement: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("completed cleanup requeued after %s after the workload released its replacement", result.RequeueAfter)
	}
	if hasFinalizer(pr, finalizer) {
		t.Fatal("PodRemediator finalizer remained after committed cleanup completed")
	}
	if err := baseClient.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("PVC remained after its replacement Pod was released: %v", err)
	}
	if err := baseClient.Get(ctx, client.ObjectKeyFromObject(replacement), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("StatefulSet replacement Pod remained after owner scale-down: %v", err)
	}
	if err := baseClient.Get(ctx, commitKey, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("completed PVC deletion retained its commit ConfigMap: %v", err)
	}
}

func TestCommittedDeletionForceDeletesTerminatingPodOnlyOnce(t *testing.T) {
	const podCleanupFinalizer = "test.example/pod-cleanup"
	const pvcProtectionFinalizer = "kubernetes.io/pvc-protection"

	for _, tc := range []struct {
		name               string
		initialGracePeriod int64
		wantForceProgress  bool
	}{
		{name: "shortens a graceful deletion", initialGracePeriod: 30, wantForceProgress: true},
		{name: "does not repeat a completed force delete", initialGracePeriod: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			markPVCDeletionCommittedForTest(t, r, pr, "worker-0", pvcProtectionFinalizer)

			pod := podUsingClaim("pod", "worker-0", "pod-uid", []string{podCleanupFinalizer})
			gracePeriod := tc.initialGracePeriod
			if err := r.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			// The fake client ignores deletionTimestamp on Create. Delete the
			// created Pod with its cleanup finalizer, then record the grace period
			// the API server would persist on the terminating object.
			if err := r.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &gracePeriod}); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			if pod.DeletionTimestamp.IsZero() {
				t.Fatal("test setup did not leave the Pod terminating")
			}
			pod.DeletionGracePeriodSeconds = &gracePeriod
			if err := r.Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			commitPVCDeletionForTest(t, r, pr, "worker-0", committedPod{Name: pod.Name, UID: string(pod.UID)})

			deleteRecorder := &podDeleteRecordingClient{Client: r.Client}
			r.Client = deleteRecorder
			crHelper, err := commonhelper.NewHelper(pr, r.Client, nil, r.Scheme, logr.Discard())
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Create(ctx, pr); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(pr)
			req := ctrl.Request{NamespacedName: key}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			currentPR := &remediationv1.PodRemediator{}
			if err := r.Get(ctx, key, currentPR); err != nil {
				t.Fatal(err)
			}
			if !hasFinalizer(currentPR, crHelper.GetFinalizer()) {
				t.Fatalf("initial reconcile did not persist PodRemediator finalizer %q", crHelper.GetFinalizer())
			}
			if err := r.Delete(ctx, currentPR); err != nil {
				t.Fatal(err)
			}

			result, err := r.Reconcile(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != DefaultConsentPollInterval {
				t.Fatalf("terminating Pod kept committed cleanup pending for %s, want %s", result.RequeueAfter, DefaultConsentPollInterval)
			}
			if !hasFinalizer(getPodRemediator(t, r, key), crHelper.GetFinalizer()) {
				t.Fatal("PodRemediator finalizer was removed while the Pod cleanup finalizer remained")
			}
			gotPod := &corev1.Pod{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
				t.Fatalf("Pod disappeared before its cleanup finalizer ran: %v", err)
			}
			if gotPod.DeletionTimestamp.IsZero() || len(gotPod.Finalizers) == 0 {
				t.Fatal("test Pod did not remain observable while terminating")
			}

			requestsAfterFirstReconcile := len(deleteRecorder.podDeleteRequests)
			if tc.wantForceProgress {
				forceDeleteRequested := false
				if len(deleteRecorder.podDeleteRequests) == 1 {
					grace := deleteRecorder.podDeleteRequests[0].GracePeriodSeconds
					forceDeleteRequested = grace != nil && *grace == 0
				}
				forceDeleteApplied := gotPod.DeletionGracePeriodSeconds != nil && *gotPod.DeletionGracePeriodSeconds == 0
				if !forceDeleteRequested && !forceDeleteApplied {
					t.Fatalf("terminating Pod made no zero-grace progress: requests=%v grace=%v", deleteRecorder.podDeleteRequests, gotPod.DeletionGracePeriodSeconds)
				}
				if len(deleteRecorder.podDeleteRequests) > 1 {
					t.Fatalf("Pod received %d force-delete requests in one reconcile, want at most one", len(deleteRecorder.podDeleteRequests))
				}

				// The fake client's Delete does not emulate the API server's stored
				// deletionGracePeriodSeconds update. Reflect an accepted force delete
				// before the next reconcile so it can prove it does not repeat it.
				if forceDeleteRequested {
					zeroGracePeriod := int64(0)
					gotPod.DeletionGracePeriodSeconds = &zeroGracePeriod
					if err := r.Update(ctx, gotPod); err != nil {
						t.Fatal(err)
					}
				}
			} else if len(deleteRecorder.podDeleteRequests) != 0 {
				t.Fatalf("already force-deleted Pod received %d additional delete requests", len(deleteRecorder.podDeleteRequests))
			}

			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if got := len(deleteRecorder.podDeleteRequests); got != requestsAfterFirstReconcile {
				t.Fatalf("Pod force-delete requests after a second reconcile = %d, want %d", got, requestsAfterFirstReconcile)
			}
			if !hasFinalizer(getPodRemediator(t, r, key), crHelper.GetFinalizer()) {
				t.Fatal("PodRemediator finalizer was removed while the Pod cleanup finalizer remained")
			}
			gotPVC := &corev1.PersistentVolumeClaim{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, gotPVC); err != nil {
				t.Fatalf("PVC disappeared while the terminating Pod remained: %v", err)
			}
			if !gotPVC.DeletionTimestamp.IsZero() {
				t.Fatal("PVC deletion started while its referencing Pod was still present")
			}
		})
	}
}

func TestUnmarkedTerminatingPVCDoesNotAuthorizePodDeletion(t *testing.T) {
	const pvcProtectionFinalizer = "kubernetes.io/pvc-protection"
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionTrue))
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations = nil
	pvc.Finalizers = []string{pvcProtectionFinalizer}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	podKey := client.ObjectKey{Namespace: "test", Name: "pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace},
		Spec: corev1.PodSpec{
			NodeName: "worker-0",
			Volumes:  []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcKey.Name}}}},
		},
	}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	terminatingPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, terminatingPVC); err != nil {
		t.Fatal(err)
	}
	if terminatingPVC.DeletionTimestamp.IsZero() {
		t.Fatal("test setup did not leave the unmarked PVC terminating")
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	gotPod := &corev1.Pod{}
	if err := r.Get(ctx, podKey, gotPod); err != nil {
		t.Fatalf("unmarked terminating PVC caused its Pod to be deleted: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("unmarked terminating PVC caused a Pod deletion request")
	}
	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, gotPVC); err != nil {
		t.Fatal(err)
	}
	if _, marked := gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation]; marked {
		t.Fatal("unrelated terminating PVC acquired a committed-deletion marker")
	}
}

func TestForgedCommittedDeletionMarkerDoesNotAuthorizeDeletion(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionTrue))
	markPVCDeletionCommittedForTest(t, r, pr, "worker-0", "kubernetes.io/pvc-protection")

	// The claim has no request, consent, or live fencing evidence. A syntactically
	// plausible marker alone must not authorize the resume path to delete it.
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Disabled = true
	pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}

	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}

	gotPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("unconsented PVC marker caused Pod deletion: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("unconsented PVC marker caused a Pod deletion request")
	}
	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "test", Name: "claim"}, gotPVC); err != nil {
		t.Fatalf("unconsented PVC marker caused PVC deletion: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("unconsented PVC marker caused a PVC deletion request")
	}
}

func TestCommittedDeletionAbortsWhenClaimHasCrossNodePod(t *testing.T) {
	t.Run("normal reconcile", func(t *testing.T) {
		ctx := context.Background()
		r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
		markPVCDeletionCommittedForTest(t, r, pr, "worker-0", "kubernetes.io/pvc-protection")
		commitPVCDeletionForTest(t, r, pr, "worker-0")
		pr.Spec.Disabled = true
		pod := podUsingClaim("healthy-user", "worker-1", "pod-uid", nil)
		if err := r.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}

		result, err := r.reconcileNormal(ctx, pr)
		if err != nil {
			t.Fatal(err)
		}
		if result.RequeueAfter != DefaultPeriodicPollInterval {
			t.Fatalf("aborted cross-node remediation requeued after %s, want normal idle interval %s", result.RequeueAfter, DefaultPeriodicPollInterval)
		}
		assertCrossNodePodAndPVCRemain(t, r, pod)
	})

	t.Run("PodRemediator deletion completes its finalizer", func(t *testing.T) {
		ctx := context.Background()
		r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
		markPVCDeletionCommittedForTest(t, r, pr, "worker-0", "kubernetes.io/pvc-protection")
		commitPVCDeletionForTest(t, r, pr, "worker-0")
		pod := podUsingClaim("healthy-user", "worker-1", "pod-uid", nil)
		if err := r.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}

		crHelper, err := commonhelper.NewHelper(pr, r.Client, nil, r.Scheme, logr.Discard())
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Create(ctx, pr); err != nil {
			t.Fatal(err)
		}
		key := client.ObjectKeyFromObject(pr)
		req := ctrl.Request{NamespacedName: key}
		// The first reconcile of a new CR persists its initial conditions and
		// adds the finalizer. The fake client needs the declared status
		// subresource above so this status write is retained like it is by the API.
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		currentPR := &remediationv1.PodRemediator{}
		if err := r.Get(ctx, key, currentPR); err != nil {
			t.Fatal(err)
		}
		hasFinalizer := false
		for _, finalizer := range currentPR.Finalizers {
			if finalizer == crHelper.GetFinalizer() {
				hasFinalizer = true
				break
			}
		}
		if !hasFinalizer {
			t.Fatalf("initial reconcile did not persist PodRemediator finalizer %q", crHelper.GetFinalizer())
		}
		if err := r.Delete(ctx, currentPR); err != nil {
			t.Fatal(err)
		}

		result, err := r.Reconcile(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if result.RequeueAfter != 0 {
			t.Fatalf("cross-node Pod kept PodRemediator deletion pending for %s", result.RequeueAfter)
		}
		deletedCR := &remediationv1.PodRemediator{}
		err = r.Get(ctx, key, deletedCR)
		if err == nil {
			for _, finalizer := range deletedCR.Finalizers {
				if finalizer == crHelper.GetFinalizer() {
					t.Fatal("PodRemediator finalizer remained after safely aborting cross-node PVC deletion")
				}
			}
		} else if !apierrors.IsNotFound(err) {
			t.Fatalf("get PodRemediator after finalizer reconcile: %v", err)
		}
		assertCrossNodePodAndPVCRemain(t, r, pod)
	})
}

func TestCommittedDeletionDoesNotDeletePodsCreatedAfterCommit(t *testing.T) {
	ctx := context.Background()
	const podCleanupFinalizer = "test.example/pod-cleanup"
	r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))

	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Finalizers = []string{"kubernetes.io/pvc-protection"}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}

	committedPod := podUsingClaim("original", "worker-0", "original-pod-uid", []string{podCleanupFinalizer})
	if err := r.Create(ctx, committedPod); err != nil {
		t.Fatal(err)
	}
	committedPodUID := committedPod.UID
	result, err := r.reconcileNormal(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter != DefaultConsentPollInterval {
		t.Fatalf("committed cleanup requeued after %s, want %s", result.RequeueAfter, DefaultConsentPollInterval)
	}
	gotCommittedPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(committedPod), gotCommittedPod); err != nil {
		t.Fatalf("Pod present at commitment was unexpectedly gone before its cleanup finalizer ran: %v", err)
	}
	if gotCommittedPod.UID != committedPodUID || gotCommittedPod.DeletionTimestamp.IsZero() {
		t.Fatal("Pod present at commitment was not the identity selected for cleanup")
	}
	committedPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, committedPVC); err != nil {
		t.Fatal(err)
	}
	if committedPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation] == "" {
		t.Fatal("PVC deletion was not committed before the replacement Pod was created")
	}

	newPod := podUsingClaim("replacement", "worker-0", "replacement-pod-uid", nil)
	if err := r.Create(ctx, newPod); err != nil {
		t.Fatal(err)
	}
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: "worker-0"}, node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions[0].Status = corev1.ConditionTrue
	if err := r.Status().Update(ctx, node); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Disabled = true

	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	gotNewPod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(newPod), gotNewPod); err != nil {
		t.Fatalf("Pod created after the deletion decision was deleted during resume: %v", err)
	}
	if !gotNewPod.DeletionTimestamp.IsZero() {
		t.Fatal("Pod created after the deletion decision received a deletion request during resume")
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pvcKey.Namespace, Name: deletionCommitName(string(committedPVC.UID))}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("aborted replacement-Pod cleanup retained its deletion commit: %v", err)
	}
}

func markPVCDeletionCommittedForTest(t *testing.T, r *PodRemediatorReconciler, pr *remediationv1.PodRemediator, node string, finalizers ...string) {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKey{Namespace: pr.Namespace, Name: "claim"}
	if err := r.Get(context.Background(), key, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations = map[string]string{
		remediationv1.PVCDeletionCommittedAnnotation: committedPVCDeletionValue(string(pvc.UID), string(pr.UID), node),
	}
	pvc.Finalizers = finalizers
	if err := r.Update(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
}

func commitPVCDeletionForTest(t *testing.T, r *PodRemediatorReconciler, pr *remediationv1.PodRemediator, node string, pods ...committedPod) {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: pr.Namespace, Name: "claim"}, pvc); err != nil {
		t.Fatal(err)
	}
	state := deletionCommit{PVCName: pvc.Name, PVCUID: string(pvc.UID), RemediatorUID: string(pr.UID), Node: node, Pods: pods}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	commit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: deletionCommitName(state.PVCUID), Namespace: pvc.Namespace,
		Annotations: map[string]string{deletionCommitAnnotation: string(encoded)},
	}}
	if err := r.Create(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
}

func hasFinalizer(obj metav1.Object, finalizer string) bool {
	for _, current := range obj.GetFinalizers() {
		if current == finalizer {
			return true
		}
	}
	return false
}

func getPodRemediator(t *testing.T, r *PodRemediatorReconciler, key client.ObjectKey) *remediationv1.PodRemediator {
	t.Helper()
	pr := &remediationv1.PodRemediator{}
	if err := r.Get(context.Background(), key, pr); err != nil {
		t.Fatal(err)
	}
	return pr
}

func createStatefulSetReplacementWorkload(t *testing.T, c client.Client) (*appsv1.StatefulSet, *corev1.Pod, *corev1.Pod) {
	t.Helper()
	replicas := int32(1)
	controller := true
	labels := map[string]string{"app": "pod-remediator-statefulset-race"}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", UID: types.UID("statefulset-uid")},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: "pod-remediator-statefulset-race",
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					NodeName:   "worker-0",
					Containers: []corev1.Container{{Name: "workload", Image: "busybox:1.36", Command: []string{"sh", "-c", "sleep 3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}},
					Volumes:    []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}},
				},
			},
		},
	}
	if err := c.Create(context.Background(), statefulSet); err != nil {
		t.Fatalf("create StatefulSet with intended replicas=%d: %v", replicas, err)
	}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID, Controller: &controller}
	original := podUsingClaim("pod-0", "worker-0", "original-pod-uid", nil)
	original.Labels = labels
	original.OwnerReferences = []metav1.OwnerReference{owner}
	replacement := original.DeepCopy()
	replacement.UID = types.UID("replacement-pod-uid")
	replacement.ResourceVersion = ""
	replacement.DeletionTimestamp = nil
	replacement.DeletionGracePeriodSeconds = nil
	replacement.Finalizers = nil
	return statefulSet, original, replacement
}

type pvcMarkerRevocationClient struct {
	client.Client
	afterMarkerWrite func(context.Context)
	intercepted      bool
}

func (c *pvcMarkerRevocationClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	err := c.Client.Patch(ctx, obj, patch, opts...)
	if err != nil || c.intercepted || c.afterMarkerWrite == nil {
		return err
	}
	pvc, ok := obj.(*corev1.PersistentVolumeClaim)
	if !ok || pvc.Annotations[remediationv1.PVCDeletionCommittedAnnotation] == "" || pvc.Annotations[deletionCommitTokenAnnotation] == "" {
		return err
	}
	c.intercepted = true
	c.afterMarkerWrite(ctx)
	return nil
}

type statefulSetReplacementClient struct {
	client.Client
	afterPodDelete       func(context.Context, *corev1.Pod)
	beforePVCDelete      func(context.Context, *corev1.PersistentVolumeClaim)
	replacementCreated   bool
	pvcDeleteIntercepted bool
}

func (c *statefulSetReplacementClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if pvc, ok := obj.(*corev1.PersistentVolumeClaim); ok && !c.pvcDeleteIntercepted && c.beforePVCDelete != nil {
		c.pvcDeleteIntercepted = true
		c.beforePVCDelete(ctx, pvc)
		c.replacementCreated = true
	}
	if pod, ok := obj.(*corev1.Pod); ok && c.afterPodDelete != nil && !c.replacementCreated {
		err := c.Client.Delete(ctx, obj, opts...)
		if err == nil {
			c.afterPodDelete(ctx, pod)
			c.replacementCreated = true
		}
		return err
	}
	return c.Client.Delete(ctx, obj, opts...)
}

type commitBoundaryClient struct {
	client.Client
	beforeCommit        func(context.Context)
	intercepted         bool
	activeCommitRecords map[client.ObjectKey]bool
}

func (c *commitBoundaryClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.interceptBoundary(ctx, obj)
	err := c.Client.Create(ctx, obj, opts...)
	if err == nil && isDeletionCommitRecord(obj) {
		c.setCommitRecord(obj, true)
	}
	return err
}

func (c *commitBoundaryClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.interceptBoundary(ctx, obj)
	err := c.Client.Update(ctx, obj, opts...)
	if err == nil && isDeletionCommitRecord(obj) {
		c.setCommitRecord(obj, true)
	}
	return err
}

func (c *commitBoundaryClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.interceptBoundary(ctx, obj)
	err := c.Client.Patch(ctx, obj, patch, opts...)
	if err == nil && isDeletionCommitRecord(obj) {
		c.setCommitRecord(obj, true)
	}
	return err
}

func (c *commitBoundaryClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	err := c.Client.Delete(ctx, obj, opts...)
	if (err == nil || apierrors.IsNotFound(err)) && isDeletionCommitRecord(obj) {
		c.setCommitRecord(obj, false)
	}
	return err
}

func (c *commitBoundaryClient) interceptBoundary(ctx context.Context, obj client.Object) {
	if c.intercepted || !isPVCDeletionCommitBoundary(obj) || c.beforeCommit == nil {
		return
	}
	c.intercepted = true
	c.beforeCommit(ctx)
}

func (c *commitBoundaryClient) setCommitRecord(obj client.Object, active bool) {
	if c.activeCommitRecords == nil {
		c.activeCommitRecords = make(map[client.ObjectKey]bool)
	}
	key := client.ObjectKeyFromObject(obj)
	if active {
		c.activeCommitRecords[key] = true
	} else {
		delete(c.activeCommitRecords, key)
	}
}

func isPVCDeletionCommitBoundary(obj client.Object) bool {
	if pvc, ok := obj.(*corev1.PersistentVolumeClaim); ok && pvc.Namespace == "test" && pvc.Name == "claim" {
		return true
	}
	return isDeletionCommitRecord(obj)
}

func isDeletionCommitRecord(obj client.Object) bool {
	return strings.HasPrefix(obj.GetName(), deletionCommitPrefix) || obj.GetAnnotations()[deletionCommitAnnotation] != ""
}

type recordedPodDelete struct {
	GracePeriodSeconds *int64
}

type podDeleteRecordingClient struct {
	client.Client
	podDeleteRequests []recordedPodDelete
}

func (c *podDeleteRecordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		options := (&client.DeleteOptions{}).ApplyOptions(opts)
		var gracePeriodSeconds *int64
		if options.GracePeriodSeconds != nil {
			gracePeriod := *options.GracePeriodSeconds
			gracePeriodSeconds = &gracePeriod
		}
		c.podDeleteRequests = append(c.podDeleteRequests, recordedPodDelete{GracePeriodSeconds: gracePeriodSeconds})
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func podUsingClaim(name, node, uid string, finalizers []string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(uid), Finalizers: finalizers},
		Spec: corev1.PodSpec{
			NodeName: node,
			Volumes:  []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func assertCrossNodePodAndPVCRemain(t *testing.T, r *PodRemediatorReconciler, pod *corev1.Pod) {
	t.Helper()
	gotPod := &corev1.Pod{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), gotPod); err != nil {
		t.Fatalf("healthy cross-node Pod was deleted: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("healthy cross-node Pod received a deletion request")
	}
	gotPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "test", Name: "claim"}, gotPVC); err != nil {
		t.Fatalf("PVC was deleted while a healthy cross-node Pod used it: %v", err)
	}
	if !gotPVC.DeletionTimestamp.IsZero() {
		t.Fatal("PVC deletion was not aborted while a healthy cross-node Pod used it")
	}
	if gotPVC.Annotations[remediationv1.PVCDeletionCommittedAnnotation] != "" {
		t.Fatal("aborted cross-node remediation retained its committed-deletion marker")
	}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: gotPVC.Namespace, Name: deletionCommitName(string(gotPVC.UID))}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("aborted cross-node remediation retained its deletion commit: %v", err)
	}
}

func TestHostnameAffinityUsesActualNodeIdentityForHandshake(t *testing.T) {
	ctx := context.Background()
	const nodeName = "compute-node-a"
	const hostname = "compute-host-a"
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName, UID: types.UID("actual-node-uid"), Labels: map[string]string{corev1.LabelHostname: hostname}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}},
	}
	r, pr := remediationFixture(t, node)
	setHostnameAffinity(t, r, hostname)
	setFixtureSNRNodeName(t, r, nodeName)

	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations = nil
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	if got := pvc.Annotations[remediationv1.PVCStuckOnNodeAnnotation]; got != nodeName {
		t.Fatalf("handshake recorded Node %q, want actual Node.Name %q", got, nodeName)
	}
	if got := pvc.Annotations[remediationv1.FencingNodeUIDAnnotation]; got != string(node.UID) {
		t.Fatalf("handshake recorded Node UID %q, want %q", got, node.UID)
	}
	if got := pvc.Annotations[remediationv1.RequestIDAnnotation]; got != "pr-uid:pvc-uid:snr-uid" {
		t.Fatalf("handshake request ID = %q, want the live SNR-bound ID", got)
	}
}

func TestDuplicateHostnameLabelsDoNotStartHandshake(t *testing.T) {
	ctx := context.Background()
	const hostname = "shared-compute-host"
	nodes := []client.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "compute-a", UID: types.UID("node-a-uid"), Labels: map[string]string{corev1.LabelHostname: hostname}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "compute-b", UID: types.UID("node-b-uid"), Labels: map[string]string{corev1.LabelHostname: hostname}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
	}
	r, _ := remediationFixture(t, nodes...)
	setHostnameAffinity(t, r, hostname)
	setFixtureSNRNodeName(t, r, "compute-a")

	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations = nil
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileNormal(ctx, &remediationv1.PodRemediator{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "test", UID: types.UID("pr-uid")}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		remediationv1.PVCStuckOnNodeAnnotation,
		remediationv1.RequestIDAnnotation,
		remediationv1.RemediatorUIDAnnotation,
		remediationv1.FencingNodeUIDAnnotation,
	} {
		if got := pvc.Annotations[key]; got != "" {
			t.Fatalf("ambiguous hostname started a handshake: %s=%q", key, got)
		}
	}
}

func TestDuplicateHostnameLabelsDoNotAuthorizeConsentedDeletion(t *testing.T) {
	ctx := context.Background()
	const hostname = "shared-compute-host"
	nodes := []client.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "compute-a", UID: types.UID("node-a-uid"), Labels: map[string]string{corev1.LabelHostname: hostname}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "compute-b", UID: types.UID("node-b-uid"), Labels: map[string]string{corev1.LabelHostname: hostname}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
	}
	r, _ := remediationFixture(t, nodes...)
	setHostnameAffinity(t, r, hostname)
	setFixtureSNRNodeName(t, r, "compute-a")

	pvcKey := client.ObjectKey{Namespace: "test", Name: "claim"}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, pvcKey, pvc); err != nil {
		t.Fatal(err)
	}
	consentID := "pr-uid:pvc-uid:snr-uid"
	pvc.Annotations = map[string]string{
		remediationv1.PVCStuckOnNodeAnnotation: "compute-a",
		remediationv1.SafeToDeleteAnnotation:   "true",
		remediationv1.RequestIDAnnotation:      consentID,
		remediationv1.ConsentIDAnnotation:      consentID,
		remediationv1.RemediatorUIDAnnotation:  "pr-uid",
		remediationv1.FencingNodeUIDAnnotation: "node-a-uid",
	}
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	podKey := client.ObjectKey{Namespace: "test", Name: "pod"}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace},
		Spec: corev1.PodSpec{
			NodeName: "compute-a",
			Volumes:  []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcKey.Name}}}},
		},
	}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pr := &remediationv1.PodRemediator{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "test", UID: types.UID("pr-uid")}}
	if _, err := r.reconcileNormal(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, pvcKey, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("consented PVC was deleted despite ambiguous hostname ownership: %v", err)
	}
	gotPod := &corev1.Pod{}
	if err := r.Get(ctx, podKey, gotPod); err != nil {
		t.Fatalf("Pod was deleted using ambiguous hostname affinity: %v", err)
	}
	if !gotPod.DeletionTimestamp.IsZero() {
		t.Fatal("consented deletion was authorized by a hostname shared by multiple Nodes")
	}
}

func setHostnameAffinity(t *testing.T, r *PodRemediatorReconciler, hostname string) {
	t.Helper()
	pv := &corev1.PersistentVolume{}
	if err := r.Get(context.Background(), client.ObjectKey{Name: "pv"}, pv); err != nil {
		t.Fatal(err)
	}
	pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
		MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{hostname}}},
	}}}}
	if err := r.Update(context.Background(), pv); err != nil {
		t.Fatal(err)
	}
}

func setFixtureSNRNodeName(t *testing.T, r *PodRemediatorReconciler, nodeName string) {
	t.Helper()
	ctx := context.Background()
	resource := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test")
	snr, err := resource.Get(ctx, "worker-0-snr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snr.SetAnnotations(map[string]string{"remediation.medik8s.io/node-name": nodeName})
	if _, err := resource.Update(ctx, snr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
