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

package v1beta1

const (
	// PVCStuckOnNodeAnnotation is set by PodRemediator on a PVC when the PVC
	// is bound to a local PV on an unhealthy node after SNR confirms fencing.
	// Value is the node name. This signals the application operator to evaluate
	// whether it is safe to delete the PVC.
	//
	// Contract: consent also requires ConsentIDAnnotation to match RequestIDAnnotation.
	// Accepted consent authorizes force-deletion only of the observed pods on the
	// fenced node (or still Pending), guarded by UID/resourceVersion preconditions.
	PVCStuckOnNodeAnnotation = "remediation.openstack.org/pvc-stuck-on-node"

	// SafeToDeleteAnnotation is set by the application operator (e.g. mariadb-operator)
	// on a PVC to authorize PodRemediator to delete it. Consent is per fault-event:
	// PodRemediator removes this annotation on node recovery or disable so the app operator must
	// re-evaluate for each new fault. Deletion also requires current SNR fencing confirmation.
	SafeToDeleteAnnotation = "remediation.openstack.org/safe-to-delete"

	// RequestIDAnnotation binds a handshake to the PodRemediator, PVC and SNR UIDs.
	// Participants must never grant consent to an empty request ID.
	RequestIDAnnotation = "remediation.openstack.org/request-id"
	// ConsentIDAnnotation is the exact request ID evaluated by the participant.
	// It must be patched together with safe-to-delete using optimistic locking.
	ConsentIDAnnotation = "remediation.openstack.org/consent-id"
	// RemediatorUIDAnnotation identifies the CR that owns a PVC handshake.
	RemediatorUIDAnnotation = "remediation.openstack.org/remediator-uid"
	// FencingNodeUIDAnnotation binds the handshake to the fenced node incarnation.
	FencingNodeUIDAnnotation = "remediation.openstack.org/fencing-node-uid"
	// PVCDeletionCommittedAnnotation is a visible deletion marker. It is first
	// written provisionally and is not authority by itself: a matching
	// controller-owned ConfigMap and finalized PVC token are required.
	PVCDeletionCommittedAnnotation = "remediation.openstack.org/pvc-deletion-committed"
)

// HasRemediationConsent accepts only an acknowledgment of the current request.
func HasRemediationConsent(annotations map[string]string) bool {
	return annotations[RequestIDAnnotation] != "" &&
		annotations[SafeToDeleteAnnotation] == "true" &&
		annotations[ConsentIDAnnotation] == annotations[RequestIDAnnotation]
}
