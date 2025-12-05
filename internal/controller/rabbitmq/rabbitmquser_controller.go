/*
Copyright 2024.

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
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch
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
	if !controllerutil.ContainsFinalizer(instance, userFinalizer) {
		controllerutil.AddFinalizer(instance, userFinalizer)
		// No need to requeue, the update will trigger a reconcile
		return ctrl.Result{}, nil
	}

	return r.reconcileNormal(ctx, instance, h)
}

func (r *RabbitMQUserReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	// Username is defaulted by webhook
	username := instance.Spec.Username

	// Get vhost - default to "/" if VhostRef is empty
	vhostName := "/"
	if instance.Spec.VhostRef != "" {
		vhost := &rabbitmqv1.RabbitMQVhost{}
		if err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.VhostRef, Namespace: instance.Namespace}, vhost); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.RabbitMQUserReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.RabbitMQUserReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		vhostName = vhost.Spec.Name
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
			password, _ = generatePassword(32)
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
		Data: map[string][]byte{
			"username": []byte(username),
			"password": []byte(password),
		},
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
		tlsEnabled := rabbit.Spec.TLS.SecretName != ""
		protocol := "http"
		managementPort := "15672"
		if tlsEnabled {
			protocol = "https"
			managementPort = "15671"
		}
		baseURL := fmt.Sprintf("%s://%s:%s", protocol, string(rabbitSecret.Data["host"]), managementPort)
		apiClient := rabbitmqapi.NewClient(baseURL, string(rabbitSecret.Data["username"]), string(rabbitSecret.Data["password"]), tlsEnabled)

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
		// Permissions are defaulted by kubebuilder defaults
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
	instance.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQUserReadyCondition, rabbitmqv1.RabbitMQUserReadyMessage)
	instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)

	return ctrl.Result{}, nil
}

func (r *RabbitMQUserReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.RabbitMQUser, h *helper.Helper) (ctrl.Result, error) {
	// If TransportURL finalizer exists, wait for TransportURL to remove it
	// The TransportURL controller manages this finalizer and removes it when switching users
	if controllerutil.ContainsFinalizer(instance, rabbitmqv1.TransportURLFinalizer) {
		return ctrl.Result{RequeueAfter: time.Duration(2) * time.Second}, nil
	}

	username := instance.Status.Username
	if username == "" {
		username = instance.Spec.Username
		if username == "" {
			username = instance.Name
		}
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
	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			// Log non-NotFound errors but continue with deletion
			log.FromContext(ctx).Error(err, "Failed to get RabbitMQ cluster", "cluster", instance.Spec.RabbitmqClusterName)
		}
		// Skip user deletion if cluster is not accessible - finalizer will still be removed
	} else {
		// Get admin credentials
		rabbitSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
		if err != nil {
			if !k8s_errors.IsNotFound(err) {
				// Log non-NotFound errors but continue with deletion
				log.FromContext(ctx).Error(err, "Failed to get admin secret")
			}
			// Skip user deletion if credentials are not accessible - finalizer will still be removed
		} else {
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

			// Delete permissions and user from RabbitMQ
			// Note: Delete methods already treat 404 as success
			if err := apiClient.DeletePermissions(vhostName, username); err != nil {
				// Log error but don't fail deletion - finalizer will still be removed to prevent CR from being stuck
				log.FromContext(ctx).Error(err, "Failed to delete permissions from RabbitMQ", "user", username, "vhost", vhostName)
			}
			if err := apiClient.DeleteUser(username); err != nil {
				// Log error but don't fail deletion - finalizer will still be removed to prevent CR from being stuck
				log.FromContext(ctx).Error(err, "Failed to delete user from RabbitMQ", "user", username)
			}
		}
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
