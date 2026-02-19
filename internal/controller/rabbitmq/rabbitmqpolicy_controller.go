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
	"encoding/json"
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

const policyFinalizer = "rabbitmqpolicy.openstack.org/finalizer"

// RabbitMQPolicyReconciler reconciles a RabbitMQPolicy object
//
//nolint:revive
type RabbitMQPolicyReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqpolicies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqpolicies/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch

// Reconcile reconciles a RabbitMQPolicy object
func (r *RabbitMQPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	instance := &rabbitmqv1.RabbitMQPolicy{}
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
		condition.UnknownCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.InitReason, rabbitmqv1.RabbitMQPolicyReadyInitMessage),
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
	if controllerutil.AddFinalizer(instance, policyFinalizer) {
		// Finalizer was added, update will trigger reconcile
		return ctrl.Result{}, nil
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQPolicyReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQPolicy, h *helper.Helper) (ctrl.Result, error) {
	// Policy name is defaulted by webhook
	policyName := instance.Spec.Name

	// Determine vhost name
	vhostName := "/"
	if instance.Spec.VhostRef != "" {
		vhost := &rabbitmqv1.RabbitMQVhost{}
		err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		vhostName = vhost.Spec.Name
	}

	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Check if cluster is ready for operations
	if readinessErr := checkClusterReadiness(rabbit); readinessErr != nil {
		if readinessErr.IsWaiting {
			// Cluster is starting up - set waiting condition
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQPolicyReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				"RabbitMQ policy waiting for dependencies %s",
				readinessErr.Reason))
			log.FromContext(ctx).Info("Waiting for RabbitMQ cluster to be ready", "cluster", instance.Spec.RabbitmqClusterName)
		} else {
			// Cluster is being deleted - set error condition
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQPolicyReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQPolicyReadyErrorMessage,
				readinessErr.Reason))
		}
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Get admin credentials
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Create API client
	baseURL := getManagementURL(rabbit, rabbitSecret)
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	caCert, err := getTLSCACert(ctx, h, rabbit, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// Create or update policy
	var definition map[string]interface{}
	if err := json.Unmarshal(instance.Spec.Definition.Raw, &definition); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	err = apiClient.CreateOrUpdatePolicy(vhostName, policyName, instance.Spec.Pattern, definition, instance.Spec.Priority, instance.Spec.ApplyTo)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	instance.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQPolicyReadyCondition, rabbitmqv1.RabbitMQPolicyReadyMessage)
	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)

	return ctrl.Result{}, nil
}

func (r *RabbitMQPolicyReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQPolicy, h *helper.Helper) (ctrl.Result, error) {
	policyName := instance.Spec.Name
	if policyName == "" {
		policyName = instance.Name
	}

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
		controllerutil.RemoveFinalizer(instance, policyFinalizer)
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
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQPolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQPolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled, caCert)

	// Delete policy from RabbitMQ
	// Note: DeletePolicy already treats 404 as success
	if err := apiClient.DeletePolicy(vhostName, policyName); err != nil {
		// Return error to trigger retry - this ensures proper cleanup in normal operations
		// Trade-off: CR may be stuck in Terminating state if RabbitMQ is persistently unavailable
		// Rationale:
		// - In normal operations, we want to ensure policies are properly cleaned up from RabbitMQ
		// - Prevents orphaned policies that could cause issues or confusion
		// - Controller will retry with exponential backoff for transient issues
		// - For persistent issues, operator logs will show clear errors
		// - Admin escape hatch: manually remove finalizer using kubectl patch if needed
		log.FromContext(ctx).Error(err, "Failed to delete policy from RabbitMQ, will retry", "policy", policyName, "vhost", vhostName)
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(instance, policyFinalizer)
	return ctrl.Result{}, nil
}

// clusterToPolicyMapFunc maps RabbitMQ cluster changes to policy reconciliation requests
// This allows the policy controller to react when a cluster is created, deleted,
// or becomes ready, ensuring policies are created/updated when the cluster is available
// Works with both RabbitmqCluster (cluster-operator) and RabbitMq CRs
func (r *RabbitMQPolicyReconciler) clusterToPolicyMapFunc(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName := obj.GetName()
	clusterNamespace := obj.GetNamespace()

	policyList := &rabbitmqv1.RabbitMQPolicyList{}
	if err := r.List(ctx, policyList, client.InNamespace(clusterNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list policies for cluster watch", "cluster", clusterName)
		return []reconcile.Request{}
	}

	requests := []reconcile.Request{}
	for _, policy := range policyList.Items {
		// Reconcile policies that reference this cluster
		if policy.Spec.RabbitmqClusterName == clusterName {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      policy.Name,
					Namespace: policy.Namespace,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQPolicy{}).
		Watches(&rabbitmqclusterv2.RabbitmqCluster{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToPolicyMapFunc)).
		Watches(&rabbitmqv1.RabbitMq{},
			handler.EnqueueRequestsFromMapFunc(r.clusterToPolicyMapFunc)).
		Complete(r)
}
