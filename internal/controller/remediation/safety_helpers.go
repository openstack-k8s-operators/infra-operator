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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
)

// safetyReader returns the uncached reader used for destructive decisions.
func (r *PodRemediatorReconciler) safetyReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// observedDeleteOptions binds deletion to the object's observed UID and version.
func observedDeleteOptions(obj client.Object) *client.DeleteOptions {
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	return &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
}

// clearRemediationAnnotations removes every annotation owned by the handshake.
func clearRemediationAnnotations(obj client.Object) {
	if obj.GetAnnotations() == nil {
		return
	}
	annotations := obj.GetAnnotations()
	for _, key := range []string{
		remediationv1.PVCStuckOnNodeAnnotation,
		remediationv1.SafeToDeleteAnnotation,
		remediationv1.RequestIDAnnotation,
		remediationv1.ConsentIDAnnotation,
		remediationv1.RemediatorUIDAnnotation,
		remediationv1.FencingNodeUIDAnnotation,
	} {
		delete(annotations, key)
	}
	obj.SetAnnotations(annotations)
}

// remediationRequestID binds one consent request to the CR, PVC, and SNR objects.
func remediationRequestID(instance *remediationv1.PodRemediator, pvc *corev1.PersistentVolumeClaim, snr *unstructured.Unstructured) string {
	if instance.UID == "" || pvc.UID == "" || snr == nil || snr.GetUID() == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s", instance.UID, pvc.UID, snr.GetUID())
}

// snrNodeName resolves the node identity used by SNR implementations. NHC-owned
// SNRs may use the node name as their object name instead of setting metadata.
func snrNodeName(snr *unstructured.Unstructured) string {
	if snr == nil {
		return ""
	}
	if name := snr.GetAnnotations()["remediation.medik8s.io/node-name"]; name != "" {
		return name
	}
	if name := snr.GetLabels()["remediation.medik8s.io/node-name"]; name != "" {
		return name
	}
	return snr.GetName()
}

// startRequest records a new fault-scoped consent handshake on a PVC.
func (r *PodRemediatorReconciler) startRequest(ctx context.Context, instance *remediationv1.PodRemediator, pvc *corev1.PersistentVolumeClaim, nodeName, nodeUID, requestID string) error {
	if requestID == "" || nodeUID == "" {
		return fmt.Errorf("cannot start remediation request without fencing and node identity")
	}
	old := pvc.DeepCopy()
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[remediationv1.PVCStuckOnNodeAnnotation] = nodeName
	annotations[remediationv1.RequestIDAnnotation] = requestID
	annotations[remediationv1.RemediatorUIDAnnotation] = string(instance.UID)
	annotations[remediationv1.FencingNodeUIDAnnotation] = nodeUID
	delete(annotations, remediationv1.SafeToDeleteAnnotation)
	delete(annotations, remediationv1.ConsentIDAnnotation)
	pvc.SetAnnotations(annotations)
	return r.Patch(ctx, pvc, client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{}))
}

// fencingMatchesNode rejects SNR evidence belonging to an older node incarnation.
func fencingMatchesNode(snr *unstructured.Unstructured, node *corev1.Node) bool {
	if snr == nil || node == nil || snr.GetDeletionTimestamp() != nil {
		return false
	}
	name := snrNodeName(snr)
	if name != node.Name || node.UID == "" {
		return false
	}
	return snr.GetCreationTimestamp().Time.IsZero() || !snr.GetCreationTimestamp().Time.Before(node.CreationTimestamp.Time)
}
