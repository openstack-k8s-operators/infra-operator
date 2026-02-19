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

package rabbitmq

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqapi "github.com/openstack-k8s-operators/infra-operator/pkg/rabbitmq/api"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/object"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// userFinalizer is the controller-level finalizer for RabbitMQUser resources.
// Note: rabbitmqv1.UserFinalizer exists in the API types but is reserved for
// TransportURL-owned users. This separate constant is used by the controller
// for its own lifecycle management.
const userFinalizer = "rabbitmquser.openstack.org/finalizer"

// credentialSecretNameField is the field index for the credential secret
const credentialSecretNameField = ".spec.secret"

// generatePassword generates a random password
func generatePassword(length int) (string, error) {
	bytes := make([]byte, length)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

// RabbitMQUserReconciler reconciles a RabbitMQUser object
//
//nolint:revive
type RabbitMQUserReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles a RabbitMQUser object
func (r *RabbitMQUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	instance := &rabbitmqv1.RabbitMQUser{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	h, _ := helper.NewHelper(instance, r.Client, r.Kclient, r.Scheme, Log)

	// Save a copy of the conditions so that we can restore the LastTransitionTime
	// when a condition's state doesn't change
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Initialize status conditions
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.InitReason, rabbitmqv1.RabbitMQUserReadyInitMessage),
	)
	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	defer func() {
		// Restore condition timestamps if they haven't changed
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)

		if instance.Status.Conditions.IsUnknown(condition.ReadyCondition) {
			instance.Status.Conditions.Set(instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		if err := h.PatchInstance(ctx, instance); err != nil {
			Log.Error(err, "Failed to patch instance")
		}
	}()

	// Handle deletion
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, h)
	}

	// Two-phase finalizer addition:
	// 1. Add user finalizer first - prevents user deletion before vhost finalizer is added
	// 2. Add vhost finalizer second - ensures user is protected during vhost finalizer addition
	//
	// If deletion occurs between phases, reconcileDelete handles it with best-effort cleanup
	if controllerutil.AddFinalizer(instance, userFinalizer) {
		Log.Info("Added user finalizer, will reconcile again to add vhost finalizer")
		return ctrl.Result{}, nil
	}

	// Add vhost finalizer after user finalizer exists
	if instance.Spec.VhostRef != "" {
		username := instance.Spec.Username

		// Validate that username is set before creating finalizer
		// The webhook should have set this, but we validate defensively
		if username == "" {
			err := fmt.Errorf("spec.Username is empty, cannot create vhost finalizer")
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQUserReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQUserReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}

		vhostFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + username

		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

		// Add per-user finalizer to vhost to prevent deletion while this user exists
		if controllerutil.AddFinalizer(vhost, vhostFinalizer) {
			if err := r.Update(ctx, vhost); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to add finalizer %s to vhost %s: %w", vhostFinalizer, instance.Spec.VhostRef, err)
			}
			Log.Info("Added finalizer to vhost", "vhost", instance.Spec.VhostRef, "finalizer", vhostFinalizer)
		}
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQUserReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	var password string
	var secretName string

	// Username is now always stored in spec.Username by the webhook
	username := instance.Spec.Username

	// Handle VhostRef changes - remove finalizer from old vhost if changed
	// We track the previous vhost CR name in status.VhostRef to detect changes
	userFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + username
	if instance.Status.VhostRef != "" && instance.Status.VhostRef != instance.Spec.VhostRef {
		// VhostRef changed - remove finalizer from old vhost
		oldVhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Status.VhostRef, Namespace: instance.Namespace}, oldVhost); err == nil {
			if controllerutil.RemoveFinalizer(oldVhost, userFinalizer) {
				if err := r.Update(ctx, oldVhost); err != nil {
					// Requeue to retry - this is important for VhostRef changes
					return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, fmt.Errorf("failed to remove finalizer %s from old vhost %s: %w", userFinalizer, instance.Status.VhostRef, err)
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			// If we get an error other than NotFound, return it to requeue
			return ctrl.Result{}, fmt.Errorf("failed to get old vhost %s for finalizer removal: %w", instance.Status.VhostRef, err)
		}
	}

	// Get vhost - default to "/" if VhostRef is empty
	vhostName := "/"
	var vhost *rabbitmqv1.RabbitMQVhost
	if instance.Spec.VhostRef != "" {
		vhost = &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

		vhostName = vhost.Spec.Name

		// Add per-user finalizer to vhost to prevent deletion while this user exists
		// Design note: Using per-user finalizers (rabbitmquser.rabbitmq.openstack.org/user-<name>)
		// instead of a shared finalizer avoids the need for reference counting.
		if controllerutil.AddFinalizer(vhost, userFinalizer) {
			if err := r.Update(ctx, vhost); err != nil {
				// Requeue to retry - this ensures the finalizer is eventually added
				return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, fmt.Errorf("failed to add finalizer %s to vhost %s: %w", userFinalizer, instance.Spec.VhostRef, err)
			}
		}
	}

	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Check if cluster is being deleted
	if !rabbit.DeletionTimestamp.IsZero() {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQUserReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQUserReadyErrorMessage,
			fmt.Sprintf("RabbitMQ cluster %s is being deleted", instance.Spec.RabbitmqClusterName)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Check if cluster is ready - need DefaultUser secret to proceed
	if rabbit.Status.DefaultUser == nil || rabbit.Status.DefaultUser.SecretReference == nil || rabbit.Status.DefaultUser.SecretReference.Name == "" {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQUserReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			rabbitmqv1.RabbitMQUserReadyWaitingMessage,
			fmt.Sprintf("RabbitMQ cluster %s", instance.Spec.RabbitmqClusterName)))
		Log.Info("Waiting for RabbitMQ cluster to be ready", "cluster", instance.Spec.RabbitmqClusterName, "hasDefaultUser", rabbit.Status.DefaultUser != nil)
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Determine credentials source
	if instance.Spec.Secret != nil && *instance.Spec.Secret != "" {
		// Use user-provided secret
		secretName = *instance.Spec.Secret
		userSecret, _, err := oko_secret.GetSecret(ctx, h, secretName, instance.Namespace)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQUserReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQUserReadyErrorMessage,
				fmt.Sprintf("failed to get credential secret %s: %v", secretName, err)))
			return ctrl.Result{}, err
		}

		// Default credential selectors if not set (defensive coding)
		if instance.Spec.CredentialSelectors == nil {
			instance.Spec.CredentialSelectors = &rabbitmqv1.CredentialSelectors{
				Username: "username",
				Password: "password",
			}
		}

		// Extract password using credential selector
		passwordBytes, ok := userSecret.Data[instance.Spec.CredentialSelectors.Password]
		if !ok {
			err := fmt.Errorf("password key %q not found in secret %s",
				instance.Spec.CredentialSelectors.Password, secretName)
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQUserReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQUserReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		password = string(passwordBytes)
	} else {
		// Existing auto-generation logic for backward compatibility
		secretName = fmt.Sprintf("rabbitmq-user-%s", instance.Name)
		userSecret, _, err := oko_secret.GetSecret(ctx, h, secretName, instance.Namespace)

		if err != nil {
			if k8s_errors.IsNotFound(err) {
				password, err = generatePassword(32)
				if err != nil {
					instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
					return ctrl.Result{}, err
				}
			} else {
				instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
		} else {
			password = string(userSecret.Data["password"])
		}

		// Create or update user secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: instance.Namespace,
			},
			Data: map[string][]byte{},
		}

		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
			secret.Data["username"] = []byte(username)
			secret.Data["password"] = []byte(password)
			return controllerutil.SetControllerReference(instance, secret, r.Scheme)
		})
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
	}

	// Update status with credentials info immediately, before attempting RabbitMQ operations
	// This ensures status is recorded even if RabbitMQ operations fail
	instance.Status.SecretName = secretName
	instance.Status.Username = username
	instance.Status.VhostRef = instance.Spec.VhostRef // Track the vhost CR name for finalizer management
	// Note: status.Vhost will be updated after successful permission operations

	// Always reconcile the user in RabbitMQ to ensure it exists
	// Following the Kubernetes reconciliation pattern: always ensure actual state matches desired state

	// Get admin credentials
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Create API client
	baseURL := getManagementURL(rabbit, rabbitSecret)
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	caCert, err := getTLSCACert(ctx, h, rabbit, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// If vhost changed and there was a previous vhost, delete permissions from old vhost first
	vhostChanged := instance.Status.Vhost != vhostName
	oldPermissionsDeleted := true
	if vhostChanged && instance.Status.Vhost != "" {
		Log.Info("Vhost changed, deleting permissions from old vhost", "old_vhost", instance.Status.Vhost, "new_vhost", vhostName, "username", username)
		if err := apiClient.DeletePermissions(instance.Status.Vhost, username); err != nil {
			// Track that old permissions weren't deleted - we'll retry on next reconciliation
			// We continue to set new permissions so the user works in the new vhost,
			// but we won't update status.Vhost until old permissions are cleaned up
			oldPermissionsDeleted = false
			Log.Error(err, "Failed to delete permissions from old vhost, will retry", "old_vhost", instance.Status.Vhost, "username", username)
		}
	}

	// Always create/update user - CreateOrUpdateUser is idempotent
	tags := instance.Spec.Tags
	if tags == nil {
		tags = []string{}
	}
	err = apiClient.CreateOrUpdateUser(username, password, tags)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Always set permissions to ensure they're correct
	// Note: instance.Spec.Permissions is never nil because the field doesn't use omitempty.
	// Individual permission fields (Configure/Write/Read) are guaranteed to have values
	// either from user input or from kubebuilder defaults (".*" for full permissions).
	err = apiClient.SetPermissions(vhostName, username,
		instance.Spec.Permissions.Configure,
		instance.Spec.Permissions.Write,
		instance.Spec.Permissions.Read)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Only update status.Vhost if old permissions were successfully deleted
	// This ensures we keep track of the old vhost and retry cleanup on next reconciliation
	if oldPermissionsDeleted {
		instance.Status.Vhost = vhostName
	}
	instance.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQUserReadyCondition, rabbitmqv1.RabbitMQUserReadyMessage)
	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)

	return ctrl.Result{}, nil
}

func (r *RabbitMQUserReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// If TransportURL finalizer exists, wait for TransportURL to remove it
	// The TransportURL controller manages this finalizer and removes it when switching users
	if controllerutil.ContainsFinalizer(instance, rabbitmqv1.TransportURLFinalizer) {
		instance.Status.Conditions.MarkFalse(
			rabbitmqv1.RabbitMQUserReadyCondition,
			condition.DeletingReason,
			condition.SeverityInfo,
			"Waiting for TransportURL to release user (finalizer: %s)",
			rabbitmqv1.TransportURLFinalizer,
		)
		return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
	}

	// Check for external finalizers (not managed by this controller)
	// Wait for all external finalizers to be removed before proceeding with cleanup
	// This ensures other controllers (e.g., dataplane) finish using this user before deletion
	externalFinalizers := []string{}
	for _, finalizer := range instance.GetFinalizers() {
		if !rabbitmqv1.IsInternalFinalizer(finalizer) {
			externalFinalizers = append(externalFinalizers, finalizer)
		}
	}

	if len(externalFinalizers) > 0 {
		Log.Info("Waiting for external finalizers to be removed before deleting user",
			"user", instance.Name,
			"finalizers", strings.Join(externalFinalizers, ", "))

		instance.Status.Conditions.MarkFalse(
			rabbitmqv1.RabbitMQUserReadyCondition,
			condition.DeletingReason,
			condition.SeverityInfo,
			"Waiting for external finalizers to be removed: %s",
			strings.Join(externalFinalizers, ", "),
		)
		return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
	}

	// All external finalizers removed, mark as ready for deletion
	instance.Status.Conditions.MarkTrue(
		rabbitmqv1.RabbitMQUserReadyCondition,
		"RabbitMQ user ready for deletion",
	)

	// Remove per-user finalizer from vhost if it exists
	// Use VhostRef from status (current) or spec (fallback) to find the vhost
	vhostRef := instance.Status.VhostRef
	if vhostRef == "" {
		vhostRef = instance.Spec.VhostRef
	}

	// Remove per-user finalizer from vhost if it exists
	// We retry on transient errors, but skip when vhost is already being deleted
	if vhostRef != "" {
		userFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + instance.Spec.Username
		vhost := &rabbitmqv1.RabbitMQVhost{}
		err := r.Get(ctx, types.NamespacedName{Name: vhostRef, Namespace: instance.Namespace}, vhost)

		// Check for context cancellation (e.g., pod shutdown)
		if ctx.Err() != nil {
			return ctrl.Result{}, ctx.Err()
		}

		if err == nil {
			// Vhost exists - try to remove our finalizer (even if vhost is being deleted)
			if controllerutil.RemoveFinalizer(vhost, userFinalizer) {
				if err := r.Update(ctx, vhost); err != nil {
					if k8s_errors.IsNotFound(err) {
						// Vhost was deleted between Get and Update - that's fine
						Log.Info("Vhost was deleted before finalizer could be removed", "vhost", vhostRef)
					} else {
						// Failed to update vhost - retry with exponential backoff
						return ctrl.Result{}, fmt.Errorf("failed to remove finalizer %s from vhost %s: %w", userFinalizer, vhostRef, err)
					}
				} else {
					Log.Info("Successfully removed finalizer from vhost", "vhost", vhostRef, "finalizer", userFinalizer)
				}
			}
		} else if k8s_errors.IsNotFound(err) {
			// Vhost doesn't exist - nothing to clean up
			Log.Info("Vhost not found during user deletion", "vhost", vhostRef)
		} else {
			// Failed to get vhost (other error) - retry
			return ctrl.Result{}, fmt.Errorf("failed to get vhost %s for finalizer removal: %w", vhostRef, err)
		}
	}

	username := instance.Status.Username
	if username == "" {
		// Username is defaulted by webhook
		username = instance.Spec.Username
	}

	// Get vhost name - priority order:
	// 1. From status.Vhost (the actual vhost name, stored during normal reconciliation)
	// 2. From the vhost CR if it still exists
	// 3. Default to "/" if VhostRef is empty
	vhostName := "/"
	if instance.Status.Vhost != "" {
		// Use the vhost name from status - this is the most reliable source during deletion
		vhostName = instance.Status.Vhost
	} else if instance.Spec.VhostRef != "" {
		// Try to get vhost CR to determine the vhost name
		vhost := &rabbitmqv1.RabbitMQVhost{}
		err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost)
		if err != nil && !k8s_errors.IsNotFound(err) {
			// Log non-NotFound errors but continue with deletion
			Log.Error(err, "Failed to get vhost", "vhost", instance.Spec.VhostRef)
		} else if err == nil && vhost.Spec.Name != "" {
			vhostName = vhost.Spec.Name
		}
	}

	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)

	// If cluster is being deleted or not found, skip cleanup and just remove finalizer
	if err != nil && !k8s_errors.IsNotFound(err) {
		// Error getting cluster - return error to retry
		return ctrl.Result{}, err
	}

	if k8s_errors.IsNotFound(err) || !rabbit.DeletionTimestamp.IsZero() {
		// Cluster doesn't exist or is being deleted - skip RabbitMQ cleanup
		Log.Info("RabbitMQ cluster not found or being deleted, skipping RabbitMQ cleanup",
			"cluster", instance.Spec.RabbitmqClusterName,
			"notFound", k8s_errors.IsNotFound(err),
			"beingDeleted", !rabbit.DeletionTimestamp.IsZero(),
			"vhostRef", vhostRef)

		instance.Status.Conditions.MarkTrue(
			rabbitmqv1.RabbitMQUserReadyCondition,
			"RabbitMQ cluster deleted, skipping RabbitMQ cleanup",
		)

		// Vhost finalizer removal was already attempted above (best effort)
		// We don't block user deletion even if it failed - the vhost controller
		// will clean up any orphaned finalizers after 10 minutes.

		controllerutil.RemoveFinalizer(instance, userFinalizer)
		return ctrl.Result{}, nil
	}

	// Cluster exists and is not being deleted - perform cleanup
	// Get admin credentials
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		// If cluster exists and is healthy, secret should be available
		// Return error to retry
		return ctrl.Result{}, err
	}

	// Create API client
	baseURL := getManagementURL(rabbit, rabbitSecret)
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	caCert, err := getTLSCACert(ctx, h, rabbit, instance.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// Delete permissions and user from RabbitMQ
	// The Delete methods already treat 404 as success
	if err := apiClient.DeletePermissions(vhostName, username); err != nil {
		// Return error to trigger retry - see rabbitmqpolicy_controller.go for detailed rationale
		return ctrl.Result{}, fmt.Errorf("failed to delete permissions for user %s from vhost %s in RabbitMQ: %w", username, vhostName, err)
	}

	if err := apiClient.DeleteUser(username); err != nil {
		// Return error to trigger retry - see rabbitmqpolicy_controller.go for detailed rationale
		return ctrl.Result{}, fmt.Errorf("failed to delete user %s from RabbitMQ: %w", username, err)
	}

	// Only delete auto-generated secret (when spec.secret is not set)
	// User-provided secrets are NOT deleted
	if instance.Spec.Secret == nil || *instance.Spec.Secret == "" {
		secretName := fmt.Sprintf("rabbitmq-user-%s", instance.Name)
		secret := &corev1.Secret{}

		// Get the secret first to check ownership
		if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: instance.Namespace}, secret); err == nil {
			// Check if we are the owner before deleting
			if object.CheckOwnerRefExist(instance.GetUID(), secret.GetOwnerReferences()) {
				if err := r.Delete(ctx, secret); err != nil && !k8s_errors.IsNotFound(err) {
					log.FromContext(ctx).Error(err, "Failed to delete user secret", "secret", secretName)
				}
			} else {
				log.FromContext(ctx).Info("Skipping secret deletion - not owned by this RabbitMQUser", "secret", secretName)
			}
		} else if !k8s_errors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "Failed to get secret for ownership check", "secret", secretName)
		}
	}

	controllerutil.RemoveFinalizer(instance, userFinalizer)
	return ctrl.Result{}, nil
}

// vhostToUserMapFunc maps vhost changes to user reconciliation requests
// This allows the user controller to react when a vhost changes, eliminating
// the need to poll when waiting for old vhost permissions to be deleted
func (r *RabbitMQUserReconciler) vhostToUserMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	vhost := obj.(*rabbitmqv1.RabbitMQVhost)
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(vhost.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list users for vhost watch", "vhost", vhost.Name)
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, user := range userList.Items {
		// Reconcile users that reference this vhost (either current or old)
		if user.Spec.VhostRef == vhost.Name || user.Status.Vhost == vhost.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      user.Name,
					Namespace: user.Namespace,
				},
			})
		}
	}
	return requests
}

// findRabbitMQUsersForSecret finds all RabbitMQUsers that reference the given secret
// This function uses the field index for efficient lookups
func (r *RabbitMQUserReconciler) findRabbitMQUsersForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	// Use the field index to find RabbitMQUsers that reference this secret
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList,
		client.InNamespace(secret.GetNamespace()),
		client.MatchingFields{credentialSecretNameField: secret.GetName()}); err != nil {
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, user := range userList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      user.Name,
				Namespace: user.Namespace,
			},
		})
	}
	return requests
}

// clusterToUserMapFunc maps RabbitMQ cluster changes to user reconciliation requests
// This allows the user controller to react when a cluster is created, deleted,
// or becomes ready, ensuring users are created/updated when the cluster is available
// Works with both RabbitmqCluster (cluster-operator) and RabbitMq CRs
func (r *RabbitMQUserReconciler) clusterToUserMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName := obj.GetName()
	clusterNamespace := obj.GetNamespace()

	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(clusterNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list users for cluster watch", "cluster", clusterName)
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, user := range userList.Items {
		// Reconcile users that reference this cluster
		if user.Spec.RabbitmqClusterName == clusterName {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      user.Name,
					Namespace: user.Namespace,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register field index for efficient secret watching
	if err := mgr.GetFieldIndexer().IndexField(context.Background(),
		&rabbitmqv1.RabbitMQUser{}, credentialSecretNameField,
		func(rawObj client.Object) []string {
			user := rawObj.(*rabbitmqv1.RabbitMQUser)
			if user.Spec.Secret == nil || *user.Spec.Secret == "" {
				return nil
			}
			return []string{*user.Spec.Secret}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQUser{}).
		Owns(&corev1.Secret{}).
		Watches(&rabbitmqv1.RabbitMQVhost{},
			handler.EnqueueRequestsFromMapFunc(r.vhostToUserMapFunc)).
		Watches(&rabbitmqclusterv2.RabbitmqCluster{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToUserMapFunc)).
		Watches(&rabbitmqv1.RabbitMq{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToUserMapFunc)).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findRabbitMQUsersForSecret),
		).
		Complete(r)
}
