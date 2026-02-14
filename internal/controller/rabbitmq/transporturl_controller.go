/*
Copyright 2022.

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
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	object "github.com/openstack-k8s-operators/lib-common/modules/common/object"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// GetClient -
func (r *TransportURLReconciler) GetClient() client.Client {
	return r.Client
}

// GetKClient -
func (r *TransportURLReconciler) GetKClient() kubernetes.Interface {
	return r.Kclient
}

// GetScheme -
func (r *TransportURLReconciler) GetScheme() *runtime.Scheme {
	return r.Scheme
}

// TransportURLReconciler reconciles a TransportURL object
type TransportURLReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *TransportURLReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("TransportURL")
}

// Constants for old user cleanup
const (
	// Grace period after removing TransportURL finalizer before deleting user.
	// This ensures the owner service has time to reconcile with new credentials
	// before the old user is removed from RabbitMQ.
	userCleanupGracePeriod = 30 * time.Second

	// Requeue interval when waiting for owner service to be ready
	ownerReadinessCheckInterval = 10 * time.Second

	// Annotation key for tracking when the TransportURL finalizer was removed
	finalizerRemovedAtAnnotation = "rabbitmq.openstack.org/finalizer-removed-at"
)

// isOwnerServiceReady checks if the owner service (Cinder, Nova, etc.) that owns this TransportURL is ready.
// Returns:
//   - ready: true if the owner is ready, false if not ready
//   - observedGen: the owner's observedGeneration (0 if no owner or not available)
//   - error: only for unexpected failures
//
// If there's no owner with controller=true, it returns (true, 0, nil).
func (r *TransportURLReconciler) isOwnerServiceReady(ctx context.Context, instance *rabbitmqv1.TransportURL) (ready bool, observedGen int64, err error) {
	Log := log.FromContext(ctx)

	// Find the controller owner reference (e.g., Cinder, Nova, etc.)
	var ownerRef *metav1.OwnerReference
	for _, owner := range instance.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			ownerRef = &owner
			break
		}
	}

	// If no controlling owner, return ready
	if ownerRef == nil {
		Log.Info("No controller owner found")
		return true, 0, nil
	}

	// Parse the APIVersion to extract group and version
	gv, err := schema.ParseGroupVersion(ownerRef.APIVersion)
	if err != nil {
		return false, 0, fmt.Errorf("failed to parse owner APIVersion %s: %w", ownerRef.APIVersion, err)
	}

	// Fetch the owner resource using unstructured client
	owner := &unstructured.Unstructured{}
	owner.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    ownerRef.Kind,
	})

	err = r.Get(ctx, types.NamespacedName{
		Name:      ownerRef.Name,
		Namespace: instance.Namespace,
	}, owner)

	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Owner deleted, return ready
			Log.Info("Owner resource not found", "kind", ownerRef.Kind, "name", ownerRef.Name)
			return true, 0, nil
		}
		// Unexpected error, return error
		return false, 0, fmt.Errorf("failed to fetch owner resource %s/%s: %w", ownerRef.Kind, ownerRef.Name, err)
	}

	// Check status.conditions for Ready condition
	conditions, found, err := unstructured.NestedSlice(owner.Object, "status", "conditions")
	if err != nil || !found {
		Log.Info("No conditions found in owner status, waiting", "kind", ownerRef.Kind, "name", ownerRef.Name)
		return false, 0, nil
	}

	// Look for Ready condition with status=True
	isReady := false
	for _, c := range conditions {
		condition, ok := c.(map[string]any)
		if !ok {
			continue
		}

		condType, _, _ := unstructured.NestedString(condition, "type")
		status, _, _ := unstructured.NestedString(condition, "status")

		if condType == "Ready" && status == "True" {
			isReady = true
			break
		}
	}

	if !isReady {
		Log.Info("Owner service not ready, waiting before deleting old user", "kind", ownerRef.Kind, "name", ownerRef.Name)
		return false, 0, nil
	}

	// Check if owner has reconciled (observedGeneration matches generation)
	generation, foundGen, err := unstructured.NestedInt64(owner.Object, "metadata", "generation")
	if err != nil || !foundGen {
		Log.Info("Could not get owner generation, waiting", "kind", ownerRef.Kind, "name", ownerRef.Name)
		return false, 0, nil
	}

	observedGeneration, foundObsGen, err := unstructured.NestedInt64(owner.Object, "status", "observedGeneration")
	if err != nil || !foundObsGen {
		Log.Info("Could not get owner observedGeneration, waiting", "kind", ownerRef.Kind, "name", ownerRef.Name)
		return false, 0, nil
	}

	if observedGeneration != generation {
		Log.Info("Owner service has not reconciled yet (observedGeneration != generation), waiting",
			"kind", ownerRef.Kind,
			"name", ownerRef.Name,
			"generation", generation,
			"observedGeneration", observedGeneration)
		return false, 0, nil
	}

	Log.Info("Owner service is ready and has reconciled",
		"kind", ownerRef.Kind,
		"name", ownerRef.Name,
		"observedGeneration", observedGeneration)
	return true, observedGeneration, nil
}

// cleanupOldUser handles the cleanup of an old RabbitMQUser when credentials are rotated.
// It removes the TransportURL finalizer and deletes the user after the owner service has reconciled.
//
// State machine:
//  1. Owner service not ready → wait and requeue
//  2. Has TransportURL finalizer → remove finalizer, set timestamp, requeue
//  3. Grace period not elapsed → wait for remaining time
//  4. External finalizers present → skip deletion (let external controller handle it)
//  5. Grace period elapsed → delete user
//
// Returns the requeue duration (0 if no requeue needed) and any error encountered.
func (r *TransportURLReconciler) cleanupOldUser(
	ctx context.Context,
	instance *rabbitmqv1.TransportURL,
	oldUserName types.NamespacedName,
) (requeueAfter time.Duration, err error) {
	Log := r.GetLogger(ctx)

	// Always fetch fresh user data from API server
	// The oldUserName comes from a List operation which may be stale
	user := &rabbitmqv1.RabbitMQUser{}
	if err := r.Get(ctx, oldUserName, user); err != nil {
		if k8s_errors.IsNotFound(err) {
			// User was already deleted - success
			Log.Info("Old user already deleted", "user", oldUserName.Name)
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get user %s for cleanup: %w", oldUserName.Name, err)
	}

	// Check if owner service is ready with new credentials
	// We only proceed with cleanup after the owner has switched to the new user
	ownerReady, _, err := r.isOwnerServiceReady(ctx, instance)
	if err != nil {
		return 0, fmt.Errorf("failed to check owner service readiness for user %s: %w", user.Name, err)
	}
	if !ownerReady {
		Log.Info("Waiting for owner service to be ready before cleanup", "user", user.Name)
		return ownerReadinessCheckInterval, nil
	}

	// State machine based on whether TransportURL finalizer is present
	hasTransportURLFinalizer := controllerutil.ContainsFinalizer(user, rabbitmqv1.TransportURLFinalizer)

	if hasTransportURLFinalizer {
		// Phase 1: Remove finalizer and start grace period
		return r.startUserCleanupGracePeriod(ctx, user)
	}

	// Phase 2: Wait for grace period, then delete
	return r.deleteUserAfterGracePeriod(ctx, instance, user)
}

// startUserCleanupGracePeriod removes the TransportURL finalizer and marks the timestamp
// when cleanup started. Returns the requeue duration (grace period) and any error.
func (r *TransportURLReconciler) startUserCleanupGracePeriod(
	ctx context.Context,
	user *rabbitmqv1.RabbitMQUser,
) (time.Duration, error) {
	Log := r.GetLogger(ctx)

	// Remove TransportURL finalizer
	controllerutil.RemoveFinalizer(user, rabbitmqv1.TransportURLFinalizer)

	// Set timestamp annotation to track when grace period started
	if user.Annotations == nil {
		user.Annotations = make(map[string]string)
	}
	user.Annotations[finalizerRemovedAtAnnotation] = time.Now().UTC().Format(time.RFC3339)

	// Update the user
	if err := r.Update(ctx, user); err != nil {
		if k8s_errors.IsNotFound(err) {
			// User was deleted before we could update - that's fine
			Log.Info("User deleted before finalizer could be removed", "user", user.Name)
			return 0, nil
		}
		return 0, fmt.Errorf("failed to remove TransportURL finalizer from user %s: %w", user.Name, err)
	}

	Log.Info("Removed TransportURL finalizer, starting grace period",
		"user", user.Name,
		"gracePeriod", userCleanupGracePeriod)

	// Requeue after the grace period
	return userCleanupGracePeriod, nil
}

// deleteUserAfterGracePeriod checks if the grace period has elapsed and deletes the user.
// Returns the requeue duration (remaining grace period) or 0 if deletion succeeded.
func (r *TransportURLReconciler) deleteUserAfterGracePeriod(
	ctx context.Context,
	instance *rabbitmqv1.TransportURL,
	user *rabbitmqv1.RabbitMQUser,
) (time.Duration, error) {
	Log := r.GetLogger(ctx)

	// Check for external finalizers - if present, we can't delete
	if hasExternalFinalizers(user) {
		Log.Info("External finalizers present, skipping deletion",
			"user", user.Name,
			"finalizers", user.GetFinalizers())
		return 0, nil
	}

	// Check if grace period has elapsed
	remaining, hasTimestamp := r.checkGracePeriod(user)

	if !hasTimestamp {
		// Finalizer was removed but no timestamp set (shouldn't happen, but handle it)
		// Set timestamp now and requeue
		Log.Info("Finalizer removed but no timestamp found, setting it now", "user", user.Name)
		return r.startUserCleanupGracePeriod(ctx, user)
	}

	if remaining > 0 {
		// Still in grace period - wait
		Log.Info("Waiting for grace period before deletion",
			"user", user.Name,
			"remaining", remaining.Round(time.Second))
		return remaining, nil
	}

	// Grace period elapsed - re-verify owner is still ready before deletion
	// This ensures we don't delete the user if owner becomes not-ready during grace period
	ownerReady, _, err := r.isOwnerServiceReady(ctx, instance)
	if err != nil {
		return 0, fmt.Errorf("failed to check owner service readiness before deletion for user %s: %w", user.Name, err)
	}
	if !ownerReady {
		Log.Info("Owner service no longer ready, waiting before deletion", "user", user.Name)
		return ownerReadinessCheckInterval, nil
	}

	// Grace period elapsed and owner is ready - delete the user
	Log.Info("Grace period elapsed, deleting old user",
		"user", user.Name,
		"gracePeriod", userCleanupGracePeriod)

	if err := r.Delete(ctx, user); err != nil {
		if k8s_errors.IsNotFound(err) {
			// Already deleted - success
			Log.Info("User already deleted", "user", user.Name)
			return 0, nil
		}
		return 0, fmt.Errorf("failed to delete user %s: %w", user.Name, err)
	}

	Log.Info("Successfully deleted old user", "user", user.Name)
	return 0, nil
}

// checkGracePeriod checks if the grace period has elapsed based on the annotation timestamp.
// Returns:
//   - remaining: time remaining in grace period (0 if elapsed or parse error)
//   - hasTimestamp: whether a valid timestamp annotation exists
func (r *TransportURLReconciler) checkGracePeriod(user *rabbitmqv1.RabbitMQUser) (remaining time.Duration, hasTimestamp bool) {
	Log := r.GetLogger(context.Background())

	timestampStr, ok := user.Annotations[finalizerRemovedAtAnnotation]
	if !ok {
		return 0, false
	}

	// Parse the timestamp
	removedAt, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		// Can't parse timestamp - fail open (allow deletion)
		Log.Error(err, "Failed to parse grace period timestamp, allowing deletion",
			"user", user.Name,
			"timestamp", timestampStr)
		return 0, true
	}

	// Calculate remaining time
	elapsed := time.Since(removedAt)
	if elapsed >= userCleanupGracePeriod {
		return 0, true // Grace period elapsed
	}

	return userCleanupGracePeriod - elapsed, true
}

// hasExternalFinalizers returns true if the user has any non-internal finalizers.
// External finalizers indicate that another controller needs to perform cleanup.
func hasExternalFinalizers(user *rabbitmqv1.RabbitMQUser) bool {
	for _, finalizer := range user.GetFinalizers() {
		if !rabbitmqv1.IsInternalFinalizer(finalizer) {
			return true
		}
	}
	return false
}

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=cinder.openstack.org;glance.openstack.org;heat.openstack.org;horizon.openstack.org;ironic.openstack.org;keystone.openstack.org;manila.openstack.org;neutron.openstack.org;nova.openstack.org;octavia.openstack.org;ovn.openstack.org;placement.openstack.org;swift.openstack.org;telemetry.openstack.org;designate.openstack.org;barbican.openstack.org;watcher.openstack.org,resources=*,verbs=get;list
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete;

// Reconcile - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *TransportURLReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)
	// Fetch the TransportURL instance
	instance := &rabbitmqv1.TransportURL{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// initialize status if Conditions is nil, but do not reset if it already
	// exists
	isNewInstance := instance.Status.Conditions == nil
	if isNewInstance {
		instance.Status.Conditions = condition.Conditions{}
	}

	// Save a copy of the condtions so that we can restore the LastTransitionTime
	// when a condition's state doesn't change.
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Always patch the instance status when exiting this function so we can
	// persist any changes.
	defer func() {
		// Don't update the status, if reconciler Panics
		if r := recover(); r != nil {
			Log.Info(fmt.Sprintf("panic during reconcile %v\n", r))
			panic(r)
		}
		condition.RestoreLastTransitionTimes(
			&instance.Status.Conditions, savedConditions)
		if instance.Status.Conditions.IsUnknown(condition.ReadyCondition) {
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	//
	// initialize status
	//
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(rabbitmqv1.TransportURLReadyCondition, condition.InitReason, rabbitmqv1.TransportURLReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	if isNewInstance {
		// Return to register overall status immediately to have an early feedback e.g. in the cli
		return ctrl.Result{}, nil
	}

	// Handle deletion by removing finalizers from owned resources
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Add finalizer if not present
	if controllerutil.AddFinalizer(instance, transportURLFinalizer) {
		// Finalizer was added, update will trigger reconcile
		return ctrl.Result{}, nil
	}

	return r.reconcileNormal(ctx, instance, helper)
}

func (r *TransportURLReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.TransportURL, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// Track if we need to requeue for old user cleanup
	var requeueAfter time.Duration

	// Get RabbitMQ cluster
	rabbit, err := getRabbitmqCluster(ctx, helper, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Wait for RabbitMQ cluster to be ready
	rabbitReady := false
	for _, condition := range rabbit.Status.Conditions {
		if condition.Reason == "AllPodsAreReady" && condition.Status == "True" {
			rabbitReady = true
			break
		}
	}
	if !rabbitReady {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			rabbitmqv1.TransportURLInProgressMessage))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Get cluster admin secret for connection details
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, helper, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.TransportURLReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.TransportURLInProgressMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	// Extract connection details from secret
	port, ok := rabbitSecret.Data["port"]
	if !ok {
		err := fmt.Errorf("port does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	adminUsername, ok := rabbitSecret.Data["username"]
	if !ok {
		err := fmt.Errorf("username does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	adminPassword, ok := rabbitSecret.Data["password"]
	if !ok {
		err := fmt.Errorf("password does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	// Get RabbitMq CR early to determine queue type for vhost selection
	rabbitmqCR := &rabbitmqv1.RabbitMq{}
	rabbitmqCRErr := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitmqCR)
	if rabbitmqCRErr != nil {
		Log.Info(fmt.Sprintf("Could not fetch RabbitMQ CR: %v", rabbitmqCRErr))
	}

	// Determine credentials and vhost
	var finalUsername, finalPassword, vhostName string
	var userRef, vhostRef string

	if instance.Spec.UserRef != "" {
		userRef = instance.Spec.UserRef
	} else if instance.Spec.Username != "" {
		// Determine vhost
		vhostName = instance.Spec.Vhost
		if vhostName == "" {
			vhostName = "/"
		}
		if vhostName != "/" {
			vhostRef = fmt.Sprintf("%s-%s-vhost", instance.Name, vhostName)
			vhost := &rabbitmqv1.RabbitMQVhost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vhostRef,
					Namespace: instance.Namespace,
				},
			}
			// Note: During normal reconciliation (not deletion), we return errors rather than
			// just logging them, as we need these operations to succeed for correct functionality.
			if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, vhost, func() error {
				if err := controllerutil.SetControllerReference(instance, vhost, r.Scheme); err != nil {
					return err
				}
				// AddFinalizer is idempotent - safe to call even if finalizer already exists
				controllerutil.AddFinalizer(vhost, rabbitmqv1.TransportURLFinalizer)
				vhost.Spec.RabbitmqClusterName = instance.Spec.RabbitmqClusterName
				vhost.Spec.Name = vhostName
				return nil
			}); err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
		}

		// Create RabbitMQUser - use username in resource name for blue/green rotation
		userRef = fmt.Sprintf("%s-%s-user", instance.Name, instance.Spec.Username)
		user := &rabbitmqv1.RabbitMQUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRef,
				Namespace: instance.Namespace,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, user, func() error {
			if err := controllerutil.SetControllerReference(instance, user, r.Scheme); err != nil {
				return err
			}
			// Add TransportURL finalizer to protect user while in use
			controllerutil.AddFinalizer(user, rabbitmqv1.TransportURLFinalizer)
			// Add temporary blocking finalizer to prevent automatic cleanup
			// This ensures users are not deleted automatically during credential rotation
			// until proper safe-to-delete logic is implemented
			controllerutil.AddFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer)
			user.Spec.RabbitmqClusterName = instance.Spec.RabbitmqClusterName
			user.Spec.Username = instance.Spec.Username
			user.Spec.VhostRef = vhostRef
			user.Spec.Permissions = rabbitmqv1.RabbitMQUserPermissions{
				Configure: ".*",
				Read:      ".*",
				Write:     ".*",
			}
			return nil
		}); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

	}

	// Remove TransportURL finalizer from previously used users that are no longer referenced
	// This handles credential rotation, rollback scenarios, and removal of custom users
	// This cleanup runs unconditionally to handle the case where a custom user is removed
	// from the spec and the TransportURL switches to default admin credentials
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err == nil {
		for i := range userList.Items {
			oldUser := &userList.Items[i]
			isOwned := object.CheckOwnerRefExist(instance.GetUID(), oldUser.GetOwnerReferences())

			// If owned by this TransportURL but not the current user, handle cleanup
			// When userRef is empty (using default credentials), all owned users should be cleaned up
			if isOwned && oldUser.Name != userRef {
				// Clean up old user - pass NamespacedName instead of full object
				// This ensures we always fetch fresh data inside cleanupOldUser
				oldUserName := types.NamespacedName{Name: oldUser.Name, Namespace: oldUser.Namespace}
				userRequeueAfter, err := r.cleanupOldUser(ctx, instance, oldUserName)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to cleanup old user %s: %w", oldUser.Name, err)
				}
				if userRequeueAfter > 0 && (requeueAfter == 0 || userRequeueAfter < requeueAfter) {
					requeueAfter = userRequeueAfter
				}
			}
		}
	}

	// Remove TransportURL finalizer from owned vhosts that are being deleted
	// but only if they're no longer referenced in the TransportURL spec
	// This handles vhost removal when the vhost definition is removed from the spec
	vhostList := &rabbitmqv1.RabbitMQVhostList{}
	if err := r.List(ctx, vhostList, client.InNamespace(instance.Namespace)); err == nil {
		for i := range vhostList.Items {
			oldVhost := &vhostList.Items[i]

			// Skip vhosts not owned by this TransportURL
			isOwned := object.CheckOwnerRefExist(instance.GetUID(), oldVhost.GetOwnerReferences())
			if !isOwned {
				continue
			}

			// Skip vhosts that are not being deleted
			if oldVhost.DeletionTimestamp.IsZero() {
				continue
			}

			// Skip the current vhost - only clean up orphaned vhosts from previous specs
			if oldVhost.Name == vhostRef {
				continue
			}

			// Remove finalizer if present
			if !controllerutil.RemoveFinalizer(oldVhost, rabbitmqv1.TransportURLFinalizer) {
				continue
			}

			// Update the vhost
			if err := r.Update(ctx, oldVhost); err != nil && !k8s_errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed to remove TransportURL finalizer from old vhost %s: %w", oldVhost.Name, err)
			}
		}
	}

	if userRef != "" {
		// Wait for RabbitMQUser to be ready
		rabbitUser := &rabbitmqv1.RabbitMQUser{}
		if err = r.Get(ctx, types.NamespacedName{Name: userRef, Namespace: instance.Namespace}, rabbitUser); err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.RequestedReason, condition.SeverityInfo, rabbitmqv1.TransportURLInProgressMessage))
				Log.Info(fmt.Sprintf("RabbitMQUser %s not found, waiting for it to be created", userRef))
				return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
			}
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		if rabbitUser.Status.SecretName == "" {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.RequestedReason, condition.SeverityInfo, rabbitmqv1.TransportURLInProgressMessage))
			Log.Info(fmt.Sprintf("RabbitMQUser %s not ready yet (no secret created)", userRef))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		// Wait for vhost status to match spec when a custom vhost is specified
		// This handles both initial creation (status is empty) and updates (status has old value)
		if instance.Spec.Vhost != "" && instance.Spec.Vhost != rabbitUser.Status.Vhost {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.RequestedReason, condition.SeverityInfo, rabbitmqv1.TransportURLInProgressMessage))
			Log.Info(fmt.Sprintf("RabbitMQUser %s vhost status (%s) doesn't match spec (%s), waiting for update", userRef, rabbitUser.Status.Vhost, instance.Spec.Vhost))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}

		// Get credentials from user secret
		userSecret, _, err := oko_secret.GetSecret(ctx, helper, rabbitUser.Status.SecretName, instance.Namespace)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		finalUsername = string(userSecret.Data["username"])
		finalPassword = string(userSecret.Data["password"])
		vhostName = rabbitUser.Status.Vhost
	} else {
		// Use default cluster admin credentials
		finalUsername = string(adminUsername)
		finalPassword = string(adminPassword)
		vhostName = "/"
	}

	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	Log.Info(fmt.Sprintf("rabbitmq cluster %s has TLS enabled: %t", rabbit.Name, tlsEnabled))

	// Build list of hosts - use ServiceHostnames from status if available, otherwise use host from secret
	var hosts []string
	if rabbitmqCRErr == nil && len(rabbitmqCR.Status.ServiceHostnames) > 0 {
		hosts = rabbitmqCR.Status.ServiceHostnames
		Log.Info(fmt.Sprintf("Using per-pod service hostnames: %v", hosts))
	} else {
		h, ok := rabbitSecret.Data["host"]
		if !ok {
			err := fmt.Errorf("host does not exist in rabbitmq secret %s", rabbitSecret.Name)
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.TransportURLReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.TransportURLReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		hosts = []string{string(h)}
		Log.Info(fmt.Sprintf("Using single host from secret: %s", string(h)))
	}

	// Determine quorum setting for secret generation
	quorum := false
	if rabbitmqCRErr != nil {
		Log.Info(fmt.Sprintf("Could not fetch RabbitMQ CR: %v", rabbitmqCRErr))
		// Default to false for quorum if we can't fetch the CR
	} else {
		Log.Info(fmt.Sprintf("Found RabbitMQ CR: %s", rabbitmqCR.Name))

		// Determine quorum setting - prefer Spec over Status
		// Spec represents the configured queue type and is set immediately when the CR is created,
		// while Status.QueueType is updated asynchronously after cluster initialization.
		// This prevents a race condition where TransportURL reconciles before Status.QueueType is set
		// after a RabbitMQ upgrade/recreation (e.g., during 3.9 -> 4.2 upgrade with storage wipe).
		if rabbitmqCR.Spec.QueueType != nil {
			quorum = *rabbitmqCR.Spec.QueueType == rabbitmqv1.QueueTypeQuorum
			Log.Info(fmt.Sprintf("Setting quorum to: %t based on spec QueueType", quorum))
		} else if rabbitmqCR.Status.QueueType != "" {
			quorum = rabbitmqCR.Status.QueueType == rabbitmqv1.QueueTypeQuorum
			Log.Info(fmt.Sprintf("Setting quorum to: %t based on status QueueType (spec not set)", quorum))
		} else {
			// Default to false if neither is set
			Log.Info("Setting quorum to: false (neither spec nor status QueueType set)")
		}

		// Update QueueType and add annotation to signal change
		if rabbitmqCR.Status.QueueType != instance.Status.QueueType {
			Log.Info(fmt.Sprintf("Updating transportURL Status.QueueType from %s to %s", instance.Status.QueueType, rabbitmqCR.Status.QueueType))
			instance.Status.QueueType = rabbitmqCR.Status.QueueType

			// Signal change to dependent controllers via annotation
			if instance.Annotations == nil {
				instance.Annotations = make(map[string]string)
			}
			instance.Annotations["rabbitmq.openstack.org/queuetype-hash"] = fmt.Sprintf("%s-%d", rabbitmqCR.Status.QueueType, time.Now().Unix())
		}
	}

	// Create a new secret with the transport URL for this CR
	secret := r.createTransportURLSecret(instance, finalUsername, finalPassword, hosts, string(port), vhostName, tlsEnabled, quorum)
	_, op, err := oko_secret.CreateOrPatchSecret(ctx, helper, instance, secret)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			rabbitmqv1.TransportURLReadyInitMessage))
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Update the CR status with actual values used
	instance.Status.SecretName = secret.Name
	instance.Status.RabbitmqUsername = finalUsername
	instance.Status.RabbitmqVhost = vhostName
	instance.Status.RabbitmqUserRef = userRef

	instance.Status.Conditions.MarkTrue(rabbitmqv1.TransportURLReadyCondition, rabbitmqv1.TransportURLReadyMessage)

	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	Log.Info("Reconciled Service successfully")

	// If we skipped any old user cleanup due to grace period or owner readiness,
	// schedule a requeue to check again later
	if requeueAfter > 0 {
		Log.Info("Scheduling requeue for old user cleanup", "after", requeueAfter.String())
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// Create k8s secret with transport URL
func (r *TransportURLReconciler) createTransportURLSecret(
	instance *rabbitmqv1.TransportURL,
	username string,
	password string,
	hosts []string,
	port string,
	vhost string,
	tlsEnabled bool,
	quorum bool,
) *corev1.Secret {
	query := "?ssl=0"
	if tlsEnabled {
		query = "?ssl=1"
	}

	// Ensure vhost has leading / (e.g., "/" or "/nova")
	if vhost != "/" && vhost[0] != '/' {
		vhost = "/" + vhost
	}

	// Build transport URL with all hosts
	var hostParts []string
	for _, host := range hosts {
		hostParts = append(hostParts, fmt.Sprintf("%s:%s@%s:%s", username, password, host, port))
	}
	transportURL := fmt.Sprintf("rabbit://%s%s%s", strings.Join(hostParts, ","), vhost, query)

	data := map[string][]byte{
		"transport_url": []byte(transportURL),
	}
	if quorum {
		data["quorumqueues"] = []byte("true")
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rabbitmq-transport-url-" + instance.Name,
			Namespace: instance.Namespace,
		},
		Data: data,
	}
}

// fields to index to reconcile when change
const (
	rabbitmqClusterNameField = ".spec.rabbitmqClusterName"
	transportURLFinalizer    = "transporturl.rabbitmq.openstack.org"
)

func (r *TransportURLReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.TransportURL, _ *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling delete")

	// Remove TransportURL finalizers from all owned users and vhosts
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err == nil {
		for i := range userList.Items {
			user := &userList.Items[i]
			// Check if this user is owned by this TransportURL
			isOwned := object.CheckOwnerRefExist(instance.GetUID(), user.GetOwnerReferences())
			if isOwned {
				// Remove both the TransportURL finalizer and the cleanup-blocked finalizer
				// When TransportURL is deleted, we want to allow full cleanup of owned users
				updated := false
				if controllerutil.RemoveFinalizer(user, rabbitmqv1.TransportURLFinalizer) {
					updated = true
				}
				if controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer) {
					updated = true
				}
				if updated {
					if err := r.Update(ctx, user); err != nil {
						return ctrl.Result{}, fmt.Errorf("failed to remove finalizers from user %s: %w", user.Name, err)
					}
				}
			}
		}
	}

	vhostList := &rabbitmqv1.RabbitMQVhostList{}
	if err := r.List(ctx, vhostList, client.InNamespace(instance.Namespace)); err == nil {
		for i := range vhostList.Items {
			vhost := &vhostList.Items[i]
			// Check if this vhost is owned by this TransportURL
			isOwned := object.CheckOwnerRefExist(instance.GetUID(), vhost.GetOwnerReferences())
			if isOwned {
				if controllerutil.RemoveFinalizer(vhost, rabbitmqv1.TransportURLFinalizer) {
					if err := r.Update(ctx, vhost); err != nil {
						return ctrl.Result{}, fmt.Errorf("failed to remove TransportURL finalizer from vhost %s: %w", vhost.Name, err)
					}
				}
			}
		}
	}

	// Remove own finalizer to allow deletion
	controllerutil.RemoveFinalizer(instance, transportURLFinalizer)
	return ctrl.Result{}, nil
}

var allWatchFields = []string{
	rabbitmqClusterNameField,
}

// SetupWithManager sets up the controller with the Manager.
func (r *TransportURLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// index caSecretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rabbitmqv1.TransportURL{}, rabbitmqClusterNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*rabbitmqv1.TransportURL)
		if cr.Spec.RabbitmqClusterName == "" {
			return nil
		}
		return []string{cr.Spec.RabbitmqClusterName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.TransportURL{}).
		Owns(&corev1.Secret{}).
		Owns(&rabbitmqv1.RabbitMQUser{}).
		Owns(&rabbitmqv1.RabbitMQVhost{}).
		Watches(
			&rabbitmqclusterv2.RabbitmqCluster{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&rabbitmqv1.RabbitMq{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

func (r *TransportURLReconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	Log := r.GetLogger(ctx)

	for _, field := range allWatchFields {
		crList := &rabbitmqv1.TransportURLList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(field, src.GetName()),
			Namespace:     src.GetNamespace(),
		}
		err := r.List(ctx, crList, listOps)
		if err != nil {
			Log.Error(err, fmt.Sprintf("listing %s for field: %s - %s", crList.GroupVersionKind().Kind, field, src.GetNamespace()))
			return requests
		}

		for _, item := range crList.Items {
			Log.Info(fmt.Sprintf("input source %s changed, reconcile: %s - %s", src.GetName(), item.GetName(), item.GetNamespace()))

			requests = append(requests,
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      item.GetName(),
						Namespace: item.GetNamespace(),
					},
				},
			)
		}
	}

	return requests
}

// GetRabbitmqCluster - get RabbitmqCluster object in namespace
func getRabbitmqCluster(
	ctx context.Context,
	h *helper.Helper,
	instance *rabbitmqv1.TransportURL,
) (*rabbitmqclusterv2.RabbitmqCluster, error) {
	rabbitMqCluster := &rabbitmqclusterv2.RabbitmqCluster{}

	err := h.GetClient().Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitMqCluster)

	return rabbitMqCluster, err
}
