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
	"time"

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

const vhostFinalizer = "rabbitmqvhost.openstack.org/finalizer"

// RabbitMQVhostReconciler reconciles a RabbitMQVhost object
//
//nolint:revive
type RabbitMQVhostReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile reconciles a RabbitMQVhost object
func (r *RabbitMQVhostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	instance := &rabbitmqv1.RabbitMQVhost{}
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
		condition.UnknownCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.InitReason, rabbitmqv1.RabbitMQVhostReadyInitMessage),
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
	if controllerutil.AddFinalizer(instance, vhostFinalizer) {
		// Finalizer was added, update will trigger reconcile
		return ctrl.Result{}, nil
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQVhostReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQVhost, h *helper.Helper) (ctrl.Result, error) {
	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQVhostReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Get admin credentials
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQVhostReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Create API client
	baseURL := getManagementURL(rabbit, rabbitSecret)
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	caCert, err := getTLSCACert(ctx, h, rabbit, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQVhostReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// Create vhost
	vhostName := instance.Spec.Name
	if vhostName == "" {
		vhostName = "/"
	}

	if vhostName != "/" {
		err = apiClient.CreateOrUpdateVhost(vhostName)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQVhostReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
	}

	instance.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQVhostReadyCondition, rabbitmqv1.RabbitMQVhostReadyMessage)
	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)

	return ctrl.Result{}, nil
}

func (r *RabbitMQVhostReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQVhost, h *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// If TransportURL finalizer exists, wait for TransportURL to remove it
	// The TransportURL controller manages this finalizer
	if controllerutil.ContainsFinalizer(instance, rabbitmqv1.TransportURLFinalizer) {
		return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
	}

	// Wait for user-managed finalizers to be removed before deleting vhost
	// These follow the pattern: rmquser.openstack.org/u-<username>
	// The RabbitMQUser controller removes these finalizers during user deletion.
	// Note: If a user is force-deleted (finalizers manually removed), the vhost may remain
	// stuck with an orphaned finalizer. In such cases, manually remove the finalizer from
	// the vhost using: kubectl patch rabbitmqvhost <name> --type json -p='[{"op": "remove", "path": "/metadata/finalizers/<index>"}]'
	for _, finalizer := range instance.Finalizers {
		if len(finalizer) > len(rabbitmqv1.UserVhostFinalizerPrefix) &&
			finalizer[:len(rabbitmqv1.UserVhostFinalizerPrefix)] == rabbitmqv1.UserVhostFinalizerPrefix {
			// Extract username from finalizer
			username := finalizer[len(rabbitmqv1.UserVhostFinalizerPrefix):]

			// Use field index to efficiently find users with this username
			userList := &rabbitmqv1.RabbitMQUserList{}
			if err := r.List(ctx, userList,
				client.InNamespace(instance.Namespace),
				client.MatchingFields{"spec.username": username}); err != nil {
				Log.Error(err, "Failed to list users while checking vhost finalizers")
				return ctrl.Result{}, err
			}

			// Check if any users with this username still exist
			// (already filtered by field index above, so just check if we got any results)
			var matchingUser *rabbitmqv1.RabbitMQUser
			if len(userList.Items) > 0 {
				matchingUser = &userList.Items[0]
			}

			if matchingUser == nil {
				// User not found - this shouldn't happen in normal flow
				// User controller should have removed the finalizer before deletion
				// If we get here, the user was likely force-deleted
				Log.Info("User not found but finalizer remains on vhost", "vhost", instance.Name, "username", username, "finalizer", finalizer)
				return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
			}

			// User exists - check if it's being deleted
			if matchingUser.DeletionTimestamp.IsZero() {
				// Active user still references this vhost - wait
				Log.Info("Vhost deletion blocked by active user", "vhost", instance.Name, "user", matchingUser.Name, "username", username)
				return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
			}
			// User is being deleted - wait for it to remove its finalizer
			Log.Info("Vhost deletion waiting for user deletion to complete", "vhost", instance.Name, "user", matchingUser.Name, "username", username)
			return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
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
		controllerutil.RemoveFinalizer(instance, vhostFinalizer)
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
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQVhostReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQVhostReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// Delete vhost (skip default)
	vhostName := instance.Spec.Name
	if vhostName == "" {
		vhostName = "/"
	}
	if vhostName != "/" {
		// DeleteVhost already treats 404 as success
		if err := apiClient.DeleteVhost(vhostName); err != nil {
			// Return error to trigger retry - see rabbitmqpolicy_controller.go for detailed rationale
			Log.Error(err, "Failed to delete vhost from RabbitMQ, will retry", "vhost", vhostName)
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(instance, vhostFinalizer)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQVhostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index RabbitMQUsers by username for efficient lookup
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rabbitmqv1.RabbitMQUser{}, "spec.username",
		func(obj client.Object) []string {
			user := obj.(*rabbitmqv1.RabbitMQUser)
			return []string{user.Spec.Username}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQVhost{}).
		Complete(r)
}
