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
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqapi "github.com/openstack-k8s-operators/infra-operator/pkg/rabbitmq/api"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// federationFinalizer is the controller-level finalizer for RabbitMQFederation resources
const federationFinalizer = "rabbitmqfederation.openstack.org/finalizer"

// RabbitMQFederationReconciler reconciles a RabbitMQFederation object
//
//nolint:revive
type RabbitMQFederationReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqfederations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqfederations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqfederations/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile reconciles a RabbitMQFederation object
func (r *RabbitMQFederationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	instance := &rabbitmqv1.RabbitMQFederation{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	h, _ := helper.NewHelper(instance, r.Client, r.Kclient, r.Scheme, Log)

	// Save a copy of the conditions so that we can restore the LastTransitionTime
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Initialize status conditions
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(rabbitmqv1.RabbitMQFederationReadyCondition, condition.InitReason, rabbitmqv1.RabbitMQFederationReadyInitMessage),
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
	// 1. Add federation finalizer first - prevents federation deletion before vhost/cluster finalizers are added
	// 2. Add vhost/upstream cluster finalizers second - ensures federation is protected during finalizer addition
	//
	// If deletion occurs between phases, reconcileDelete handles it with best-effort cleanup
	if controllerutil.AddFinalizer(instance, federationFinalizer) {
		Log.Info("Added federation finalizer, will reconcile again to add vhost/cluster finalizers")
		return ctrl.Result{}, nil
	}

	// Add vhost finalizer if VhostRef is set
	if instance.Spec.VhostRef != "" {
		vhostFinalizer := rabbitmqv1.FederationVhostFinalizerPrefix + instance.Name

		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQFederationReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQFederationReadyErrorMessage,
				fmt.Sprintf("failed to get vhost %s: %v", instance.Spec.VhostRef, err)))
			return ctrl.Result{}, err
		}

		// Add per-federation finalizer to vhost to prevent deletion while this federation exists
		if controllerutil.AddFinalizer(vhost, vhostFinalizer) {
			if err := r.Update(ctx, vhost); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to add finalizer %s to vhost %s: %w", vhostFinalizer, instance.Spec.VhostRef, err)
			}
			Log.Info("Added finalizer to vhost", "vhost", instance.Spec.VhostRef, "finalizer", vhostFinalizer)
		}
	}

	// Add upstream cluster finalizer if UpstreamClusterName is set
	if instance.Spec.UpstreamClusterName != "" {
		upstreamFinalizer := rabbitmqv1.FederationUpstreamFinalizerPrefix + instance.Name

		upstreamCluster := &rabbitmqv1.RabbitMq{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.UpstreamClusterName, Namespace: instance.Namespace}, upstreamCluster); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQFederationReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQFederationReadyErrorMessage,
				fmt.Sprintf("failed to get upstream cluster %s: %v", instance.Spec.UpstreamClusterName, err)))
			return ctrl.Result{}, err
		}

		// Add per-federation finalizer to upstream cluster to prevent deletion while this federation exists
		if controllerutil.AddFinalizer(upstreamCluster, upstreamFinalizer) {
			if err := r.Update(ctx, upstreamCluster); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to add finalizer %s to upstream cluster %s: %w", upstreamFinalizer, instance.Spec.UpstreamClusterName, err)
			}
			Log.Info("Added finalizer to upstream cluster", "cluster", instance.Spec.UpstreamClusterName, "finalizer", upstreamFinalizer)
		}
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQFederationReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQFederation, _ *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// Handle VhostRef changes - remove finalizer from old vhost if changed
	federationFinalizer := rabbitmqv1.FederationVhostFinalizerPrefix + instance.Name
	if instance.Status.VhostRef != "" && instance.Status.VhostRef != instance.Spec.VhostRef {
		// VhostRef changed - remove finalizer from old vhost
		oldVhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Status.VhostRef, Namespace: instance.Namespace}, oldVhost); err == nil {
			if controllerutil.RemoveFinalizer(oldVhost, federationFinalizer) {
				if err := r.Update(ctx, oldVhost); err != nil {
					// Requeue to retry - this is important for VhostRef changes
					return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, fmt.Errorf("failed to remove finalizer %s from old vhost %s: %w", federationFinalizer, instance.Status.VhostRef, err)
				}
				Log.Info("Removed finalizer from old vhost", "vhost", instance.Status.VhostRef, "finalizer", federationFinalizer)
			}
		} else if !k8s_errors.IsNotFound(err) {
			// If we get an error other than NotFound, return it to requeue
			return ctrl.Result{}, fmt.Errorf("failed to get old vhost %s for finalizer removal: %w", instance.Status.VhostRef, err)
		}
		// If old vhost not found, continue (it may have been deleted)
	}

	// Get the vhost name (default to "/" if not specified)
	vhostName := "/"
	if instance.Spec.VhostRef != "" {
		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.RabbitMQFederationReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.RabbitMQFederationReadyErrorMessage,
				fmt.Sprintf("referenced vhost %s not found: %v", instance.Spec.VhostRef, err)))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, err
		}
		vhostName = vhost.Spec.Name
		// Track the vhost reference in status
		instance.Status.VhostRef = instance.Spec.VhostRef
	}

	// Update status with vhost name
	instance.Status.Vhost = vhostName
	instance.Status.UpstreamName = instance.Spec.UpstreamName

	// Build the upstream URI
	upstreamURI, err := r.buildUpstreamURI(ctx, instance, vhostName)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQFederationReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQFederationReadyErrorMessage,
			fmt.Sprintf("failed to build upstream URI: %v", err)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, err
	}

	// Get RabbitMQ cluster connection details
	apiClient, err := r.getRabbitMQClient(ctx, instance.Spec.RabbitmqClusterName, instance.Namespace)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQFederationReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQFederationReadyErrorMessage,
			fmt.Sprintf("failed to get RabbitMQ client: %v", err)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, err
	}

	// Create or update federation upstream
	upstream := rabbitmqapi.FederationUpstream{
		URI:            upstreamURI,
		AckMode:        instance.Spec.AckMode,
		Expires:        instance.Spec.Expires,
		MessageTTL:     instance.Spec.MessageTTL,
		MaxHops:        instance.Spec.MaxHops,
		PrefetchCount:  instance.Spec.PrefetchCount,
		ReconnectDelay: instance.Spec.ReconnectDelay,
		TrustUserId:    instance.Spec.TrustUserId,
		Exchange:       instance.Spec.Exchange,
		Queue:          instance.Spec.Queue,
	}

	if err := apiClient.CreateOrUpdateFederationUpstream(vhostName, instance.Spec.UpstreamName, upstream); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQFederationReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQFederationReadyErrorMessage,
			fmt.Sprintf("failed to create federation upstream: %v", err)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, err
	}

	Log.Info("Created/updated federation upstream", "upstream", instance.Spec.UpstreamName, "vhost", vhostName)

	// Create policy to activate federation
	policyName := fmt.Sprintf("federation-%s", instance.Spec.UpstreamName)
	instance.Status.PolicyName = policyName

	policyDefinition := map[string]interface{}{
		"federation-upstream": instance.Spec.UpstreamName,
	}

	// Determine applyTo based on whether Exchange or Queue is specified
	applyTo := "all"
	if instance.Spec.Exchange != "" {
		applyTo = "exchanges"
	} else if instance.Spec.Queue != "" {
		applyTo = "queues"
	}

	if err := apiClient.CreateOrUpdatePolicy(vhostName, policyName, instance.Spec.PolicyPattern, policyDefinition, int(instance.Spec.Priority), applyTo); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.RabbitMQFederationReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.RabbitMQFederationReadyErrorMessage,
			fmt.Sprintf("failed to create federation policy: %v", err)))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, err
	}

	Log.Info("Created/updated federation policy", "policy", policyName, "vhost", vhostName)

	// Set ready condition
	instance.Status.Conditions.Set(condition.TrueCondition(
		rabbitmqv1.RabbitMQFederationReadyCondition,
		rabbitmqv1.RabbitMQFederationReadyMessage))

	return ctrl.Result{}, nil
}

func (r *RabbitMQFederationReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQFederation, _ *helper.Helper) (ctrl.Result, error) {
	Log := log.FromContext(ctx)

	// Check if the RabbitMQ cluster is being deleted
	// If so, skip cleanup as the cluster resources will be deleted anyway
	cluster := &rabbitmqv1.RabbitMq{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, cluster); err != nil {
		if k8s_errors.IsNotFound(err) {
			Log.Info("RabbitMQ cluster not found, skipping federation cleanup")
			controllerutil.RemoveFinalizer(instance, federationFinalizer)
			return ctrl.Result{}, nil
		}
		// If there's an error other than NotFound, log it but continue with cleanup attempt
		Log.Info("Unable to get RabbitMQ cluster, will attempt cleanup anyway", "error", err)
	} else if !cluster.DeletionTimestamp.IsZero() {
		// Cluster is being deleted, skip cleanup
		Log.Info("RabbitMQ cluster is being deleted, skipping federation cleanup")
		controllerutil.RemoveFinalizer(instance, federationFinalizer)
		return ctrl.Result{}, nil
	}

	// Get the vhost name from status (or spec if status not set)
	vhostName := instance.Status.Vhost
	if vhostName == "" {
		vhostName = "/"
		if instance.Spec.VhostRef != "" {
			vhost := &rabbitmqv1.RabbitMQVhost{}
			if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err == nil {
				vhostName = vhost.Spec.Name
			}
		}
	}

	// Get RabbitMQ client
	apiClient, err := r.getRabbitMQClient(ctx, instance.Spec.RabbitmqClusterName, instance.Namespace)
	if err != nil {
		Log.Info("Failed to get RabbitMQ client for cleanup, removing finalizer anyway", "error", err)
		controllerutil.RemoveFinalizer(instance, federationFinalizer)
		return ctrl.Result{}, nil
	}

	// Delete policy
	policyName := instance.Status.PolicyName
	if policyName == "" {
		policyName = fmt.Sprintf("federation-%s", instance.Spec.UpstreamName)
	}

	if err := apiClient.DeletePolicy(vhostName, policyName); err != nil {
		Log.Info("Failed to delete federation policy", "policy", policyName, "error", err)
		// Continue with upstream deletion even if policy deletion fails
	} else {
		Log.Info("Deleted federation policy", "policy", policyName, "vhost", vhostName)
	}

	// Delete federation upstream
	if err := apiClient.DeleteFederationUpstream(vhostName, instance.Spec.UpstreamName); err != nil {
		Log.Info("Failed to delete federation upstream", "upstream", instance.Spec.UpstreamName, "error", err)
		// Continue anyway - don't block deletion on cleanup failures
	} else {
		Log.Info("Deleted federation upstream", "upstream", instance.Spec.UpstreamName, "vhost", vhostName)
	}

	// Remove finalizer from vhost if VhostRef is set
	if instance.Spec.VhostRef != "" {
		vhostFinalizer := rabbitmqv1.FederationVhostFinalizerPrefix + instance.Name

		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err == nil {
			if controllerutil.RemoveFinalizer(vhost, vhostFinalizer) {
				if err := r.Update(ctx, vhost); err != nil {
					Log.Info("Failed to remove finalizer from vhost", "vhost", instance.Spec.VhostRef, "error", err)
					// Continue anyway - don't block deletion
				} else {
					Log.Info("Removed finalizer from vhost", "vhost", instance.Spec.VhostRef, "finalizer", vhostFinalizer)
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			Log.Info("Failed to get vhost for finalizer removal", "vhost", instance.Spec.VhostRef, "error", err)
			// Continue anyway
		}
	}

	// Remove finalizer from upstream cluster if UpstreamClusterName is set
	if instance.Spec.UpstreamClusterName != "" {
		upstreamFinalizer := rabbitmqv1.FederationUpstreamFinalizerPrefix + instance.Name

		upstreamCluster := &rabbitmqv1.RabbitMq{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.UpstreamClusterName, Namespace: instance.Namespace}, upstreamCluster); err == nil {
			if controllerutil.RemoveFinalizer(upstreamCluster, upstreamFinalizer) {
				if err := r.Update(ctx, upstreamCluster); err != nil {
					Log.Info("Failed to remove finalizer from upstream cluster", "cluster", instance.Spec.UpstreamClusterName, "error", err)
					// Continue anyway - don't block deletion
				} else {
					Log.Info("Removed finalizer from upstream cluster", "cluster", instance.Spec.UpstreamClusterName, "finalizer", upstreamFinalizer)
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			Log.Info("Failed to get upstream cluster for finalizer removal", "cluster", instance.Spec.UpstreamClusterName, "error", err)
			// Continue anyway
		}
	}

	// Remove federation finalizer
	controllerutil.RemoveFinalizer(instance, federationFinalizer)

	return ctrl.Result{}, nil
}

// buildUpstreamURI builds the AMQP URI for the federation upstream
func (r *RabbitMQFederationReconciler) buildUpstreamURI(ctx context.Context, instance *rabbitmqv1.RabbitMQFederation, vhostName string) (string, error) {
	// If UpstreamSecretRef is specified, use URI from secret
	if instance.Spec.UpstreamSecretRef != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.UpstreamSecretRef.Name, Namespace: instance.Namespace}, secret); err != nil {
			return "", fmt.Errorf("failed to get upstream secret %s: %w", instance.Spec.UpstreamSecretRef.Name, err)
		}

		uri, ok := secret.Data["uri"]
		if !ok {
			return "", fmt.Errorf("secret %s does not contain 'uri' key", instance.Spec.UpstreamSecretRef.Name)
		}

		return string(uri), nil
	}

	// If UpstreamClusterName is specified, build URI from local cluster
	if instance.Spec.UpstreamClusterName != "" {
		// Get the upstream RabbitMQ cluster
		upstreamCluster := &rabbitmqv1.RabbitMq{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.UpstreamClusterName, Namespace: instance.Namespace}, upstreamCluster); err != nil {
			return "", fmt.Errorf("failed to get upstream cluster %s: %w", instance.Spec.UpstreamClusterName, err)
		}

		// Get the credentials secret for the upstream cluster
		secretName := fmt.Sprintf("%s-default-user", instance.Spec.UpstreamClusterName)
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: instance.Namespace}, secret); err != nil {
			return "", fmt.Errorf("failed to get upstream cluster credentials secret %s: %w", secretName, err)
		}

		username, ok := secret.Data["username"]
		if !ok {
			return "", fmt.Errorf("secret %s does not contain 'username' key", secretName)
		}

		password, ok := secret.Data["password"]
		if !ok {
			return "", fmt.Errorf("secret %s does not contain 'password' key", secretName)
		}

		// Build the service name
		serviceName := fmt.Sprintf("%s.%s.svc.cluster.local", instance.Spec.UpstreamClusterName, instance.Namespace)

		// Encode the vhost for the URI
		// Fix: default vhost "/" must be encoded as %2F in URI path
		encodedVhost := url.PathEscape(vhostName)

		// Build AMQP URI
		// Format: amqp://username:password@host:port/vhost
		uri := fmt.Sprintf("amqp://%s:%s@%s:5672/%s",
			url.QueryEscape(string(username)),
			url.QueryEscape(string(password)),
			serviceName,
			encodedVhost)

		return uri, nil
	}

	return "", fmt.Errorf("either upstreamClusterName or upstreamSecretRef must be specified")
}

// getRabbitMQClient creates a RabbitMQ API client for the specified cluster
func (r *RabbitMQFederationReconciler) getRabbitMQClient(ctx context.Context, clusterName, namespace string) (*rabbitmqapi.Client, error) {
	// Get the RabbitMQ cluster credentials
	secretName := fmt.Sprintf("%s-default-user", clusterName)
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		return nil, fmt.Errorf("failed to get cluster credentials secret %s: %w", secretName, err)
	}

	username, ok := secret.Data["username"]
	if !ok {
		return nil, fmt.Errorf("secret %s does not contain 'username' key", secretName)
	}

	password, ok := secret.Data["password"]
	if !ok {
		return nil, fmt.Errorf("secret %s does not contain 'password' key", secretName)
	}

	// Build the management API URL
	serviceName := fmt.Sprintf("%s.%s.svc.cluster.local", clusterName, namespace)
	baseURL := fmt.Sprintf("http://%s:15672", serviceName)

	// Create and return the client
	return rabbitmqapi.NewClient(baseURL, string(username), string(password), false, nil), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RabbitMQFederationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.RabbitMQFederation{}).
		Complete(r)
}
