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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediationctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
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

// These keys describe the persisted PVC state observed by the tests. The tests
// drive the public reconciler and never call the controller's commit helpers.
const (
	deletionCommitTokenAnnotation     = "remediation.openstack.org/pvc-deletion-commit-token"
	deletionCommitFinalizedAnnotation = "remediation.openstack.org/pvc-deletion-commit-finalized"
)

var gvrSelfNodeRemediation = schema.GroupVersionResource{
	Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Resource: "selfnoderemediations",
}

func remediationFixture(t *testing.T, nodes ...client.Object) (*remediationctrl.PodRemediatorReconciler, *remediationv1.PodRemediator) {
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
	return &remediationctrl.PodRemediatorReconciler{Client: c, DynamicClient: dyn, Scheme: scheme}, pr
}

func remediationNode(ready corev1.ConditionStatus) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-0", UID: types.UID("node-uid"), Labels: map[string]string{corev1.LabelHostname: "worker-0"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}
}

// reconcileRemediation persists the requested spec and drives the public entry
// point. The first call initializes the CR before running the scenario under test.
func reconcileRemediation(ctx context.Context, r *remediationctrl.PodRemediatorReconciler, pr *remediationv1.PodRemediator) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(pr)
	deleting := !pr.DeletionTimestamp.IsZero()
	current := &remediationv1.PodRemediator{}
	if err := r.Get(ctx, key, current); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		current = pr.DeepCopy()
		current.DeletionTimestamp = nil
		if err := r.Create(ctx, current); err != nil {
			return ctrl.Result{}, err
		}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, key, current); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !reflect.DeepEqual(current.Spec, pr.Spec) {
		current.Spec = *pr.Spec.DeepCopy()
		if err := r.Update(ctx, current); err != nil {
			return ctrl.Result{}, err
		}
	}
	if deleting && current.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, current); err != nil {
			return ctrl.Result{}, err
		}
	}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	if err != nil {
		return result, err
	}
	if err := r.Get(ctx, key, pr); err != nil {
		if deleting && apierrors.IsNotFound(err) {
			pr.Finalizers = nil
			return result, nil
		}
		return result, fmt.Errorf("read PodRemediator after reconcile: %w", err)
	}
	return result, nil
}

func reconcileDeletingRemediation(ctx context.Context, r *remediationctrl.PodRemediatorReconciler, pr *remediationv1.PodRemediator) (ctrl.Result, error) {
	now := metav1.Now()
	pr.DeletionTimestamp = &now
	return reconcileRemediation(ctx, r, pr)
}

// pvcMarkerRevocationClient injects a state change immediately after the
// provisional PVC marker has been persisted and before deletion is finalized.
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

// Persisted deletion records used to model restarts, corruption, and legacy data.
const (
	deletionCommitPrefix         = "podremediator-deletion-"
	deletionCommitAnnotation     = "remediation.openstack.org/deletion-commit-state"
	deletionCommitPhasePrepared  = "prepared"
	deletionCommitPhaseCommitted = "committed"
)

var gvrNodeHealthCheck = schema.GroupVersionResource{Group: "remediation.medik8s.io", Version: "v1alpha1", Resource: "nodehealthchecks"}
var gvrSelfNodeRemediationTemplate = schema.GroupVersionResource{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Resource: "selfnoderemediationtemplates"}

type committedPod struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}
type deletionCommit struct {
	PVCName            string         `json:"pvcName"`
	PVCUID             string         `json:"pvcUID"`
	PVCResourceVersion string         `json:"pvcResourceVersion,omitempty"`
	RequestID          string         `json:"requestID,omitempty"`
	RemediatorUID      string         `json:"remediatorUID"`
	Node               string         `json:"node"`
	Token              string         `json:"token,omitempty"`
	Phase              string         `json:"phase,omitempty"`
	Pods               []committedPod `json:"pods"`
}

func deletionCommitName(pvcUID string) string {
	sum := sha256.Sum256([]byte(pvcUID))
	return deletionCommitPrefix + hex.EncodeToString(sum[:16])
}
func committedPVCDeletionValue(pvcUID, remediatorUID, nodeName string) string {
	return fmt.Sprintf("v1|%s|%s|%s", nodeName, pvcUID, remediatorUID)
}
func decodeDeletionCommit(commit *corev1.ConfigMap) (deletionCommit, error) {
	var state deletionCommit
	err := json.Unmarshal([]byte(commit.Annotations[deletionCommitAnnotation]), &state)
	return state, err
}
func observedDeleteOptions(obj client.Object) *client.DeleteOptions {
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	return &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
}
