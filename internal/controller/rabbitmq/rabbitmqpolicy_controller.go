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
	"fmt"

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

	// Initialize status
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
		cl := condition.CreateList(
			condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
			condition.UnknownCondition(rabbitmqv1.PolicyReadyCondition, condition.InitReason, rabbitmqv1.PolicyReadyInitMessage),
		)
		instance.Status.Conditions.Init(&cl)
		instance.Status.ObservedGeneration = instance.Generation
	}

	defer func() {
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

	// Add finalizer
	if !controllerutil.ContainsFinalizer(instance, policyFinalizer) {
		controllerutil.AddFinalizer(instance, policyFinalizer)
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQPolicyReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQPolicy, h *helper.Helper) (ctrl.Result, error) {
	// Determine policy name
	policyName := instance.Spec.Name
	if policyName == "" {
		policyName = instance.Name
	}

	// Determine vhost name
	vhostName := "/"
	if instance.Spec.VhostRef != "" {
		vhost := &rabbitmqv1.RabbitMQVhost{}
		err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.PolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.PolicyReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		vhostName = vhost.Spec.Name
		if vhostName == "" {
			vhostName = "/"
		}
	}

	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.PolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.PolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Get admin credentials
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.PolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.PolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	// Create API client
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	protocol := "http"
	managementPort := "15672"
	if tlsEnabled {
		protocol = "https"
		managementPort = "15671"
	}
	baseURL := fmt.Sprintf("%s://%s:%s", protocol, string(rabbitSecret.Data["host"]), managementPort)
	apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled)

	// Create or update policy
	applyTo := instance.Spec.ApplyTo
	if applyTo == "" {
		applyTo = "all"
	}
	var definition map[string]interface{}
	if err := json.Unmarshal(instance.Spec.Definition.Raw, &definition); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.PolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.PolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}
	err = apiClient.CreateOrUpdatePolicy(vhostName, policyName, instance.Spec.Pattern, definition, instance.Spec.Priority, applyTo)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.PolicyReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.PolicyReadyErrorMessage, err.Error()))
		return ctrl.Result{}, err
	}

	instance.Status.Conditions.MarkTrue(rabbitmqv1.PolicyReadyCondition, rabbitmqv1.PolicyReadyMessage)
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
		if err == nil {
			vhostName = vhost.Spec.Name
			if vhostName == "" {
				vhostName = "/"
			}
		}
	}

	// Get RabbitMQ cluster
	rabbit := &rabbitmqclusterv2.RabbitmqCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbit)
	if err == nil {
		// Get admin credentials
		rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
		if err == nil {
			// Create API client
			tlsEnabled := rabbit.Spec.TLS.SecretName != ""
			protocol := "http"
			managementPort := "15672"
			if tlsEnabled {
				protocol = "https"
				managementPort = "15671"
			}
			baseURL := fmt.Sprintf("%s://%s:%s", protocol, string(rabbitSecret.Data["host"]), managementPort)
			apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled)

			// Delete policy
			if err := apiClient.DeletePolicy(vhostName, policyName); err != nil {
				// Log error but don't fail deletion - the policy may already be gone
				log.FromContext(ctx).Error(err, "Failed to delete policy from RabbitMQ", "policy", policyName, "vhost", vhostName)
			}
		}
	}

	controllerutil.RemoveFinalizer(instance, policyFinalizer)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQPolicy{}).
		Complete(r)
}
