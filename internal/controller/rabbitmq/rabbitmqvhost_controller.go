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
	"fmt"
	"time"

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
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	vhostFinalizer = "rabbitmqvhost.openstack.org/finalizer"

	// orphanedFinalizerTimeout is how long to wait before automatically removing
	// orphaned user finalizers (e.g., when user was force-deleted)
	orphanedFinalizerTimeout = 10 * time.Minute
)

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
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
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

	// Check if cluster is being deleted
	if !rabbit.DeletionTimestamp.IsZero() {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQVhostReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQVhostReadyErrorMessage,
			fmt.Sprintf("RabbitMQ cluster %s is being deleted", instance.Spec.RabbitmqClusterName)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Check if cluster is ready - need DefaultUser secret to proceed
	if rabbit.Status.DefaultUser == nil || rabbit.Status.DefaultUser.SecretReference == nil || rabbit.Status.DefaultUser.SecretReference.Name == "" {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQVhostReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			"RabbitMQ vhost waiting for dependencies %s",
			fmt.Sprintf("RabbitMQ cluster %s", instance.Spec.RabbitmqClusterName)))
		log.FromContext(ctx).Info("Waiting for RabbitMQ cluster to be ready", "cluster", instance.Spec.RabbitmqClusterName, "hasDefaultUser", rabbit.Status.DefaultUser != nil)
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
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
	// The watch on TransportURL will trigger reconciliation when the finalizer is removed
	if controllerutil.ContainsFinalizer(instance, rabbitmqv1.TransportURLFinalizer) {
		return ctrl.Result{}, nil
	}

	// Wait for user-managed finalizers to be removed before deleting vhost
	// These follow the pattern: rmquser.openstack.org/u-<username>
	// The RabbitMQUser controller removes these finalizers during user deletion.
	// Note: If a user is force-deleted (finalizers manually removed), the vhost may remain
	// stuck with an orphaned finalizer. In such cases, manually remove the finalizer from
	// the vhost using: kubectl patch rabbitmqvhost <name> --type json -p='[{"op": "remove", "path": "/metadata/finalizers/<index>"}]'

	// List all users once and build a map for efficient lookup
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list users while checking vhost finalizers: %w", err)
	}

	// Build a map of username -> users for efficient lookup
	usersByUsername := make(map[string][]*rabbitmqv1.RabbitMQUser)
	for i := range userList.Items {
		user := &userList.Items[i]
		username := user.Spec.Username
		usersByUsername[username] = append(usersByUsername[username], user)
	}

	// Process all user finalizers in a single pass
	var finalizersToRemove []string
	for _, finalizer := range instance.Finalizers {
		if len(finalizer) > len(rabbitmqv1.UserVhostFinalizerPrefix) &&
			finalizer[:len(rabbitmqv1.UserVhostFinalizerPrefix)] == rabbitmqv1.UserVhostFinalizerPrefix {
			// Extract username from finalizer
			username := finalizer[len(rabbitmqv1.UserVhostFinalizerPrefix):]

			// Look up users with this username from our pre-built map
			users := usersByUsername[username]
			var matchingUser *rabbitmqv1.RabbitMQUser
			if len(users) > 0 {
				matchingUser = users[0]
			}

			if matchingUser == nil {
				// User not found - this shouldn't happen in normal flow
				// User controller should have removed the finalizer before deletion
				// If we get here, the user was likely force-deleted
				// Check if this finalizer has been orphaned for too long
				if !instance.DeletionTimestamp.IsZero() && time.Since(instance.DeletionTimestamp.Time) > orphanedFinalizerTimeout {
					Log.Info("Orphaned user finalizer ready for removal after timeout - user was likely force-deleted",
						"vhost", instance.Name,
						"username", username,
						"finalizer", finalizer,
						"deletionAge", time.Since(instance.DeletionTimestamp.Time).Round(time.Second))
					finalizersToRemove = append(finalizersToRemove, finalizer)
					continue
				}
				Log.Info("User not found but finalizer remains on vhost, waiting for timeout",
					"vhost", instance.Name,
					"username", username,
					"finalizer", finalizer,
					"deletionAge", time.Since(instance.DeletionTimestamp.Time).Round(time.Second),
					"timeout", orphanedFinalizerTimeout)
				return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
			}

			// User exists - check if it's being deleted
			if matchingUser.DeletionTimestamp.IsZero() {
				// Active user still references this vhost - wait
				// The watch on RabbitMQUser will trigger reconciliation when the user is deleted
				Log.Info("Vhost deletion blocked by active user", "vhost", instance.Name, "user", matchingUser.Name, "username", username)
				return ctrl.Result{}, nil
			}
			// User is being deleted - wait for it to remove its finalizer
			// The watch on RabbitMQUser will trigger reconciliation when the user deletion completes
			Log.Info("Vhost deletion waiting for user deletion to complete", "vhost", instance.Name, "user", matchingUser.Name, "username", username)
			return ctrl.Result{}, nil
		}
	}

	// Remove all orphaned finalizers in a single update
	if len(finalizersToRemove) > 0 {
		Log.Info("Removing orphaned user finalizers in batch", "vhost", instance.Name, "count", len(finalizersToRemove))
		for _, finalizer := range finalizersToRemove {
			controllerutil.RemoveFinalizer(instance, finalizer)
		}
		if err := r.Update(ctx, instance); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove %d orphaned finalizer(s) from vhost %s: %w", len(finalizersToRemove), instance.Name, err)
		}
		// Requeue to check for remaining finalizers
		return ctrl.Result{Requeue: true}, nil
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
			return ctrl.Result{}, fmt.Errorf("failed to delete vhost %s from RabbitMQ: %w", vhostName, err)
		}
	}

	controllerutil.RemoveFinalizer(instance, vhostFinalizer)
	return ctrl.Result{}, nil
}

// userToVhostMapFunc maps user changes to vhost reconciliation requests
// This allows the vhost controller to react when a user is deleted, eliminating
// the need to poll when waiting for user deletion to complete
func (r *RabbitMQVhostReconciler) userToVhostMapFunc(_ context.Context, obj client.Object) []reconcile.Request {
	user := obj.(*rabbitmqv1.RabbitMQUser)
	if user.Spec.VhostRef == "" {
		return []reconcile.Request{}
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      user.Spec.VhostRef,
			Namespace: user.Namespace,
		},
	}}
}

// transportURLToVhostMapFunc maps TransportURL changes to vhost reconciliation requests
// This allows the vhost controller to react when a TransportURL is deleted or changes,
// eliminating the need to poll when waiting for TransportURL finalizers to be removed
func (r *RabbitMQVhostReconciler) transportURLToVhostMapFunc(_ context.Context, obj client.Object) []reconcile.Request {
	turl := obj.(*rabbitmqv1.TransportURL)

	// Reconcile the vhost if it's specified in the spec or tracked in the status
	requests := []reconcile.Request{}
	vhostName := turl.Spec.Vhost
	if vhostName == "" {
		vhostName = turl.Status.RabbitmqVhost
	}
	if vhostName != "" && vhostName != "/" {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      vhostName,
				Namespace: turl.Namespace,
			},
		})
	}
	return requests
}

// clusterToVhostMapFunc maps RabbitMQ cluster changes to vhost reconciliation requests
// This allows the vhost controller to react when a cluster is created, deleted,
// or becomes ready, ensuring vhosts are created/updated when the cluster is available
// Works with both RabbitmqCluster (cluster-operator) and RabbitMq CRs
func (r *RabbitMQVhostReconciler) clusterToVhostMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName := obj.GetName()
	clusterNamespace := obj.GetNamespace()

	vhostList := &rabbitmqv1.RabbitMQVhostList{}
	if err := r.List(ctx, vhostList, client.InNamespace(clusterNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list vhosts for cluster watch", "cluster", clusterName)
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, vhost := range vhostList.Items {
		// Reconcile vhosts that reference this cluster
		if vhost.Spec.RabbitmqClusterName == clusterName {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vhost.Name,
					Namespace: vhost.Namespace,
				},
			})
		}
	}
	return requests
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
		Watches(&rabbitmqv1.RabbitMQUser{},
			handler.EnqueueRequestsFromMapFunc(r.userToVhostMapFunc)).
		Watches(&rabbitmqclusterv2.RabbitmqCluster{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToVhostMapFunc)).
		Watches(&rabbitmqv1.RabbitMq{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToVhostMapFunc)).
		Watches(&rabbitmqv1.TransportURL{},
			handler.EnqueueRequestsFromMapFunc(r.transportURLToVhostMapFunc)).
		Complete(r)
}
