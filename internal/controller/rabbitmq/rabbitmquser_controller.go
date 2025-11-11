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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqapi "github.com/openstack-k8s-operators/infra-operator/pkg/rabbitmq/api"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
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

	// Add finalizer if not being deleted
	if controllerutil.AddFinalizer(instance, userFinalizer) {
		// Finalizer was added, update will trigger reconcile
		return ctrl.Result{}, nil
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQUserReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// Username is defaulted by webhook
	username := instance.Spec.Username

	// Handle VhostRef changes - remove finalizer from old vhost if changed
	// We track the previous vhost CR name in status.VhostRef to detect changes
	userFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + instance.Name
	if instance.Status.VhostRef != "" && instance.Status.VhostRef != instance.Spec.VhostRef {
		// VhostRef changed - remove finalizer from old vhost
		oldVhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Status.VhostRef, Namespace: instance.Namespace}, oldVhost); err == nil {
			if controllerutil.RemoveFinalizer(oldVhost, userFinalizer) {
				if err := r.Update(ctx, oldVhost); err != nil {
					// Requeue to retry - this is important for VhostRef changes
					Log.Error(err, "Failed to remove finalizer from old vhost, requeueing", "vhost", instance.Status.VhostRef, "finalizer", userFinalizer)
					return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, err
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			// If we get an error other than NotFound, return it to requeue
			Log.Error(err, "Failed to get old vhost for finalizer removal", "vhost", instance.Status.VhostRef)
			return ctrl.Result{}, err
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
				Log.Error(err, "Failed to add finalizer to vhost, requeueing", "vhost", instance.Spec.VhostRef, "finalizer", userFinalizer)
				return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, err
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

	// Get or create user secret (use CR name to avoid conflicts when multiple CRs have same username)
	secretName := fmt.Sprintf("rabbitmq-user-%s", instance.Name)
	userSecret, _, err := oko_secret.GetSecret(ctx, h, secretName, instance.Namespace)
	var password string

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

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Data["username"] = []byte(username)
		secret.Data["password"] = []byte(password)
		return controllerutil.SetControllerReference(instance, secret, r.Scheme)
	})
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Only create/update user in RabbitMQ if secret was just created
	if op == controllerutil.OperationResultCreated {
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

		// Create user
		tags := instance.Spec.Tags
		if tags == nil {
			tags = []string{}
		}
		err = apiClient.CreateOrUpdateUser(username, password, tags)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

		// Set permissions
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
	}

	instance.Status.SecretName = secretName
	instance.Status.Username = username
	instance.Status.Vhost = vhostName
	instance.Status.VhostRef = instance.Spec.VhostRef // Track the vhost CR name for finalizer management
	instance.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQUserReadyCondition, rabbitmqv1.RabbitMQUserReadyMessage)
	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)

	return ctrl.Result{}, nil
}

func (r *RabbitMQUserReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// If TransportURL finalizer exists, wait for TransportURL to remove it
	// The TransportURL controller manages this finalizer and removes it when switching users
	if controllerutil.ContainsFinalizer(instance, rabbitmqv1.TransportURLFinalizer) {
		return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
	}

	// Remove per-user finalizer from vhost if it exists
	// Use VhostRef from status (current) or spec (fallback) to find the vhost
	vhostRef := instance.Status.VhostRef
	if vhostRef == "" {
		vhostRef = instance.Spec.VhostRef
	}
	if vhostRef != "" {
		userFinalizer := rabbitmqv1.UserVhostFinalizerPrefix + instance.Name
		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: vhostRef, Namespace: instance.Namespace}, vhost); err == nil {
			if controllerutil.RemoveFinalizer(vhost, userFinalizer) {
				if err := r.Update(ctx, vhost); err != nil {
					// Requeue to retry - we want to clean up the vhost finalizer
					Log.Error(err, "Failed to remove finalizer from vhost during user deletion, requeueing", "vhost", vhostRef, "finalizer", userFinalizer)
					return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, err
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			// If we get an error other than NotFound, return it to requeue
			// This prevents us from potentially deleting permissions from wrong vhost
			Log.Error(err, "Failed to get vhost for finalizer removal during user deletion", "vhost", vhostRef)
			return ctrl.Result{}, err
		}
	}

	username := instance.Status.Username
	if username == "" {
		// Username is defaulted by webhook
		username = instance.Spec.Username
	}

	// Get vhost - default to "/" if VhostRef is empty
	vhostName := "/"
	if instance.Spec.VhostRef != "" {
		vhost := &rabbitmqv1.RabbitMQVhost{}
		err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost)
		if err != nil && !k8s_errors.IsNotFound(err) {
			// Log non-NotFound errors but continue with deletion
			log.FromContext(ctx).Error(err, "Failed to get vhost", "vhost", instance.Spec.VhostRef)
		}
		if vhost.Spec.Name != "" {
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
		// Cluster doesn't exist or is being deleted - nothing to clean up
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
		Log.Error(err, "Failed to delete permissions from RabbitMQ, will retry", "user", username, "vhost", vhostName)
		return ctrl.Result{}, err
	}

	if err := apiClient.DeleteUser(username); err != nil {
		// Return error to trigger retry - see rabbitmqpolicy_controller.go for detailed rationale
		Log.Error(err, "Failed to delete user from RabbitMQ, will retry", "user", username)
		return ctrl.Result{}, err
	}

	// Delete secret (use CR name, same as in reconcileNormal)
	secretName := fmt.Sprintf("rabbitmq-user-%s", instance.Name)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: instance.Namespace,
		},
	}
	if err := r.Delete(ctx, secret); err != nil && !k8s_errors.IsNotFound(err) {
		log.FromContext(ctx).Error(err, "Failed to delete user secret", "secret", secretName)
	}

	controllerutil.RemoveFinalizer(instance, userFinalizer)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQUser{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
