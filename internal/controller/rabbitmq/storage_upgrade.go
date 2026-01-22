/*
Copyright 2023.

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

// Package rabbitmq implements RabbitMQ storage upgrade and migration logic.
//
// # Rollback Procedure
//
// Steps to roll back a failed upgrade:
//
// 1. Stop the upgrade process:
//    kubectl label rabbitmq <name> -n <namespace> rabbitmq-version-
//
// 2. Check the current upgrade phase:
//    kubectl get rabbitmq <name> -n <namespace> -o jsonpath='{.status.upgradePhase}'
//
// 3. For PVCs in deletion state (upgradePhase = "DeletingStorage"):
//    # List PVCs
//    kubectl get pvc -n <namespace> -l app.kubernetes.io/name=<name>
//
//    # Check finalizers
//    kubectl get pvc -n <namespace> <pvc-name> -o jsonpath='{.metadata.finalizers}'
//
//    # Remove finalizers (note: CSI driver cleanup will be bypassed)
//    kubectl patch pvc <pvc-name> -n <namespace> -p '{"metadata":{"finalizers":null}}' --type=merge
//
// 4. For PVs in deletion state:
//    # List PVs
//    kubectl get pv -o jsonpath='{.items[?(@.spec.claimRef.name=="persistence-<name>-0")].metadata.name}'
//
//    # Check PV status and finalizers
//    kubectl get pv <pv-name> -o yaml
//
//    # Check storage backend logs (CSI driver, storage system)
//
//    # Remove PV finalizers (note: storage backend cleanup will be bypassed)
//    kubectl patch pv <pv-name> -p '{"metadata":{"finalizers":null}}' --type=merge
//
// 5. Clean up the RabbitMQ CR:
//    kubectl delete rabbitmq <name> -n <namespace> --timeout=60s
//
//    # Force remove finalizers if deletion hangs
//    kubectl patch rabbitmq <name> -n <namespace> -p '{"metadata":{"finalizers":null}}' --type=merge
//
// 6. Clean remaining resources:
//    kubectl delete rabbitmqcluster <name> -n <namespace>
//    kubectl delete pvc -n <namespace> -l app.kubernetes.io/name=<name>
//    kubectl delete service -n <namespace> -l app.kubernetes.io/name=<name>
//
// 7. Recreate the RabbitMQ CR with the original version:
//    kubectl apply -f rabbitmq-original.yaml
//
// 8. Restore data from backups:
//    # Use backup/restore procedure for the deployment
//
// Notes:
// - Removing finalizers bypasses CSI driver and storage backend cleanup
// - Storage resources remain in the backend when PV finalizers are removed
// - RabbitMQ data is deleted during storage wipe for version compatibility

package rabbitmq

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ensureDeleteReclaimPolicy patches all PVs to use Delete reclaim policy to ensure
// storage is automatically wiped when PVCs are deleted during upgrade.
// Returns true if any PVs were patched (requiring a requeue to wait for changes to apply).
//
// This is necessary for RabbitMQ major version upgrades where Mnesia data formats
// are incompatible and old data must be completely removed before starting the new version.
//
// The original reclaim policy is stored in an annotation for reference.
func (r *Reconciler) ensureDeleteReclaimPolicy(ctx context.Context, pvcList *corev1.PersistentVolumeClaimList, log logr.Logger) (bool, error) {
	pvsPatched := false

	for _, pvc := range pvcList.Items {
		if pvc.Spec.VolumeName == "" {
			// PVC not yet bound to a PV
			continue
		}

		// Use retry logic to handle concurrent updates to PVs
		// This prevents failures from CSI drivers or other controllers updating PVs
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			pv := &corev1.PersistentVolume{}
			if err := r.Client.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, pv); err != nil {
				if k8s_errors.IsNotFound(err) {
					// PV already gone, that's fine
					return nil
				}
				return fmt.Errorf("failed to get PV %s: %w", pvc.Spec.VolumeName, err)
			}

			// Check if we need to patch the reclaim policy
			if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
				log.Info("Patching PV reclaim policy to Delete for upgrade",
					"pvName", pv.Name,
					"pvcName", pvc.Name,
					"oldPolicy", pv.Spec.PersistentVolumeReclaimPolicy,
					"newPolicy", corev1.PersistentVolumeReclaimDelete)

				// Store original policy in annotation for reference/debugging
				if pv.Annotations == nil {
					pv.Annotations = make(map[string]string)
				}
				pv.Annotations["rabbitmq.openstack.org/original-reclaim-policy"] = string(pv.Spec.PersistentVolumeReclaimPolicy)

				// Patch the reclaim policy to Delete
				// When the PVC is deleted, Kubernetes will automatically delete the PV
				// and the storage backend will wipe the underlying storage
				pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete

				if err := r.Client.Update(ctx, pv); err != nil {
					return err
				}

				pvsPatched = true
			}
			return nil
		})

		if err != nil {
			return false, fmt.Errorf("failed to update PV %s reclaim policy after retries: %w. "+
				"Check PV: kubectl get pv %s -o yaml", pvc.Spec.VolumeName, err, pvc.Spec.VolumeName)
		}
	}

	return pvsPatched, nil
}

// removeProtectionFinalizer removes the kubernetes.io/pvc-protection finalizer from a PVC.
// Other finalizers (CSI drivers, backup systems) are preserved.
//
// The kubernetes.io/pvc-protection finalizer prevents PVC deletion while pods are using it.
// Removed during controlled upgrades after all pods are terminated.
func removeProtectionFinalizer(pvc *corev1.PersistentVolumeClaim, log logr.Logger) bool {
	originalCount := len(pvc.Finalizers)
	if originalCount == 0 {
		return false
	}

	// Filter out only the kubernetes.io/pvc-protection finalizer
	var filtered []string
	for _, f := range pvc.Finalizers {
		if f != "kubernetes.io/pvc-protection" {
			filtered = append(filtered, f)
		}
	}

	if len(filtered) < originalCount {
		log.Info("Removing PVC protection finalizer to allow deletion during upgrade",
			"pvcName", pvc.Name,
			"removedFinalizer", "kubernetes.io/pvc-protection",
			"remainingFinalizers", filtered)
		pvc.Finalizers = filtered
		return true
	}

	return false
}

// verifyPVCleanupComplete checks if all PVs associated with the RabbitMQ instance
// have been deleted. Returns true if cleanup is complete, false if still in progress.
//
// Verifies clean storage before recreating the cluster.
//
// Implements timeout protection (30 minutes) for PV deletion.
func (r *Reconciler) verifyPVCleanupComplete(ctx context.Context, namespace, instanceName string, storageWipeStartedAt *time.Time, log logr.Logger) (bool, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := r.Client.List(ctx, pvList); err != nil {
		return false, fmt.Errorf("failed to list PVs during upgrade verification: %w. "+
			"List PVs: kubectl get pv -A", err)
	}

	// Check if any PVs are still referencing our PVCs
	pvcPrefix := fmt.Sprintf("persistence-%s-", instanceName)
	var stuckPVs []string
	for _, pv := range pvList.Items {
		if pv.Spec.ClaimRef != nil &&
			pv.Spec.ClaimRef.Namespace == namespace &&
			strings.HasPrefix(pv.Spec.ClaimRef.Name, pvcPrefix) {

			log.Info("Waiting for PV cleanup to complete",
				"pvName", pv.Name,
				"phase", pv.Status.Phase,
				"pvcName", pv.Spec.ClaimRef.Name)
			stuckPVs = append(stuckPVs, pv.Name)
		}
	}

	if len(stuckPVs) > 0 {
		// Check timeout
		const maxStorageWipeTimeout = 30 * time.Minute
		if storageWipeStartedAt != nil {
			elapsed := time.Since(*storageWipeStartedAt)
			if elapsed > maxStorageWipeTimeout {
				return false, fmt.Errorf("storage wipe timeout after %v - PVs in deletion: %v. "+
					"Check PV status: kubectl get pv %s -o yaml. "+
					"Check finalizers: kubectl get pv %s -o jsonpath='{.metadata.finalizers}'. "+
					"Check storage backend logs. "+
					"Remove finalizers: kubectl patch pv %s -p '{\"metadata\":{\"finalizers\":null}}' --type=merge",
					elapsed, stuckPVs, stuckPVs[0], stuckPVs[0], stuckPVs[0])
			}
			log.Info("Storage cleanup in progress",
				"elapsed", elapsed.Round(time.Second),
				"timeout", maxStorageWipeTimeout,
				"pvCount", len(stuckPVs))
		}
		return false, nil
	}

	// All PVs cleaned up
	log.Info("All PVs successfully cleaned up")
	return true, nil
}

// deletePVCsForUpgrade deletes all PVCs associated with the RabbitMQ instance.
// Removes the kubernetes.io/pvc-protection finalizer to allow deletion during upgrade.
//
// Prerequisites:
// - All pods terminated
// - All PVs have Delete reclaim policy (ensured by ensureDeleteReclaimPolicy)
//
// PVC deletion triggers:
// 1. PV deletion (due to Delete reclaim policy)
// 2. Storage backend cleanup
//
// Returns true if PVCs were deleted.
func (r *Reconciler) deletePVCsForUpgrade(ctx context.Context, pvcList *corev1.PersistentVolumeClaimList, log logr.Logger) (bool, error) {
	if len(pvcList.Items) == 0 {
		return false, nil
	}

	// Sort PVCs by name to ensure deterministic deletion order
	// This is important for StatefulSets where ordinal ordering matters
	sort.Slice(pvcList.Items, func(i, j int) bool {
		return pvcList.Items[i].Name < pvcList.Items[j].Name
	})

	log.Info("Deleting PVCs in deterministic order", "pvcCount", len(pvcList.Items))

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]

		// Remove kubernetes.io/pvc-protection finalizer to allow deletion
		// Other finalizers are preserved for CSI driver cleanup
		if removeProtectionFinalizer(pvc, log) {
			if err := r.Client.Update(ctx, pvc); err != nil {
				return false, fmt.Errorf("failed to remove finalizer from PVC %s: %w. "+
					"Check PVC: kubectl get pvc %s -n %s -o yaml",
					pvc.Name, err, pvc.Name, pvc.Namespace)
			}
		}

		// Delete the PVC
		// The Delete reclaim policy on the PV triggers automatic storage cleanup
		if err := r.Client.Delete(ctx, pvc); err != nil && !k8s_errors.IsNotFound(err) {
			return false, fmt.Errorf("failed to delete PVC %s: %w. "+
				"Check PVC: kubectl describe pvc %s -n %s",
				pvc.Name, err, pvc.Name, pvc.Namespace)
		}

		log.Info("Deleted PVC - storage will be automatically wiped by Delete reclaim policy",
			"pvcName", pvc.Name,
			"pvName", pvc.Spec.VolumeName)
	}

	return true, nil
}

// waitForPodsTermination checks if all pods associated with the RabbitMQ instance
// have been terminated. Returns a Result with requeue if pods are still terminating.
//
// This is a critical step before deleting PVCs to avoid data corruption from pods
// still writing to volumes.
func (r *Reconciler) waitForPodsTermination(ctx context.Context, namespace, instanceName string, log logr.Logger) (ctrl.Result, bool, error) {
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/name": instanceName},
	); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("failed to list pods during upgrade: %w", err)
	}

	if len(podList.Items) > 0 {
		log.Info("Waiting for pods to terminate before deleting PVCs",
			"podCount", len(podList.Items))
		return ctrl.Result{RequeueAfter: UpgradeCheckInterval}, true, nil
	}

	return ctrl.Result{}, false, nil
}

// listPVCsForInstance lists all PVCs associated with the RabbitMQ instance.
func (r *Reconciler) listPVCsForInstance(ctx context.Context, namespace, instanceName string) (*corev1.PersistentVolumeClaimList, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/name": instanceName},
	); err != nil {
		return nil, fmt.Errorf("failed to list PVCs during upgrade: %w", err)
	}
	return pvcList, nil
}

// StorageWipeParams contains all parameters needed for performing a storage wipe
type StorageWipeParams struct {
	// Instance namespace
	Namespace string
	// Instance name (used for labeling resources)
	InstanceName string
	// Current queue type from status (used to determine if ha-all policy should be deleted)
	CurrentQueueType string
	// Reason for the wipe ("version upgrade" or "queue type migration")
	Reason string
	// Timestamp when storage wipe started (for timeout tracking)
	StorageWipeStartedAt *time.Time
	// Function to delete the RabbitMQCluster
	DeleteCluster func(ctx context.Context) error
	// Function to delete the ha-all mirrored policy (optional)
	DeleteMirroredPolicy func(ctx context.Context) error
}

// performStorageWipe orchestrates the complete storage wipe process for RabbitMQ upgrades and migrations.
//
// This function handles both:
// 1. Version upgrades (e.g., 3.9 → 4.0) that require incompatible Mnesia data format changes
// 2. Queue type migrations (Mirrored → Quorum) that cannot be done in-place
//
// The wipe process follows these steps:
// 1. Delete ha-all policy if needed (for Mirrored queue migrations)
// 2. Delete RabbitMQCluster to stop all pods
// 3. Wait for all pods to terminate
// 4. Patch PV reclaim policies to Delete (for automatic storage cleanup)
// 5. Delete all PVCs (triggers Kubernetes to wipe storage)
// 6. Verify all PVs are deleted
//
// Returns:
// - ctrl.Result with Requeue if still in progress
// - error if an unrecoverable error occurs
// - ctrl.Result{} and nil error when wipe is complete
func (r *Reconciler) performStorageWipe(
	ctx context.Context,
	params StorageWipeParams,
	log logr.Logger,
) (ctrl.Result, error) {

	log.Info("Starting RabbitMQ storage wipe process", "reason", params.Reason)

	// Step 1: Delete ha-all policy if it exists (for queue type migrations from Mirrored)
	// This must happen before deleting the cluster since the policy is applied to the cluster
	needsPolicyDeletion := params.Reason == "queue type migration" ||
		(params.Reason == "version upgrade" && params.CurrentQueueType == "Mirrored")

	if needsPolicyDeletion && params.DeleteMirroredPolicy != nil {
		log.Info("Deleting ha-all policy before storage wipe")
		if err := params.DeleteMirroredPolicy(ctx); err != nil {
			log.Error(err, "Failed to delete ha-all policy during wipe")
			return ctrl.Result{}, fmt.Errorf("failed to delete ha-all policy during wipe: %w", err)
		}
	}

	// Step 2: Delete the RabbitMQCluster to trigger pod deletion
	if params.DeleteCluster != nil {
		if err := params.DeleteCluster(ctx); err != nil {
			log.Error(err, "Failed to delete RabbitMQCluster during storage wipe")
			return ctrl.Result{}, fmt.Errorf("failed to delete RabbitMQCluster during storage wipe: %w", err)
		}
	}

	// Step 3: Wait for all pods to terminate before deleting PVCs
	// This is critical to avoid data corruption from pods still writing to volumes
	result, shouldReturn, err := r.waitForPodsTermination(ctx, params.Namespace, params.InstanceName, log)
	if err != nil {
		log.Error(err, "Failed to wait for pods termination")
		return ctrl.Result{}, err
	}
	if shouldReturn {
		return result, nil
	}

	// Step 4: List PVCs for the instance
	pvcList, err := r.listPVCsForInstance(ctx, params.Namespace, params.InstanceName)
	if err != nil {
		log.Error(err, "Failed to list PVCs during storage wipe")
		return ctrl.Result{}, err
	}

	if len(pvcList.Items) > 0 {
		// Step 4a: Ensure all PVs have Delete reclaim policy
		// This is the key to automatic storage cleanup - when we delete the PVC,
		// Kubernetes will automatically delete the PV and the storage backend will wipe the data
		pvsPatched, err := r.ensureDeleteReclaimPolicy(ctx, pvcList, log)
		if err != nil {
			log.Error(err, "Failed to ensure Delete reclaim policy on PVs")
			return ctrl.Result{}, err
		}

		if pvsPatched {
			// Wait for the PV updates to apply before proceeding
			log.Info("Patched PV reclaim policies to Delete, waiting for changes to apply")
			return ctrl.Result{RequeueAfter: UpgradeCheckInterval}, nil
		}

		// Step 4b: Delete all PVCs
		// The Delete reclaim policy will trigger automatic storage cleanup
		pvcDeleted, err := r.deletePVCsForUpgrade(ctx, pvcList, log)
		if err != nil {
			log.Error(err, "Failed to delete PVCs during storage wipe")
			return ctrl.Result{}, err
		}

		if pvcDeleted {
			log.Info("Deleted all PVCs, waiting for PV cleanup to complete", "pvcCount", len(pvcList.Items))
			return ctrl.Result{RequeueAfter: UpgradeCheckInterval}, nil
		}
	}

	// Step 5: Verify all PVs are completely cleaned up
	cleanupComplete, err := r.verifyPVCleanupComplete(ctx, params.Namespace, params.InstanceName, params.StorageWipeStartedAt, log)
	if err != nil {
		log.Error(err, "Failed to verify PV cleanup")
		return ctrl.Result{}, fmt.Errorf("PV cleanup verification failed: %w. "+
			"Stop upgrade: kubectl label rabbitmq %s -n %s rabbitmq-version-. "+
			"Clean PVCs: kubectl delete pvc -n %s -l app.kubernetes.io/name=%s. "+
			"Check PV finalizers: kubectl get pv -o jsonpath='{.items[*].metadata.finalizers}'. "+
			"Remove finalizers: kubectl patch pv <pv-name> -p '{\"metadata\":{\"finalizers\":null}}' --type=merge. "+
			"Delete CR: kubectl delete rabbitmq %s -n %s. "+
			"Recreate with original version and restore from backups",
			err, params.InstanceName, params.Namespace, params.Namespace, params.InstanceName, params.InstanceName, params.Namespace)
	}

	if !cleanupComplete {
		// Still waiting for PVs to be deleted
		return ctrl.Result{RequeueAfter: UpgradeCheckInterval}, nil
	}

	log.Info("Storage completely cleaned", "reason", params.Reason)
	// Wipe is complete, return success
	return ctrl.Result{}, nil
}
