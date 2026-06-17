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
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
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
	edpm "github.com/openstack-k8s-operators/lib-common/modules/edpm/unstructured"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PendingReleaseCheckInterval is the requeue interval while waiting for a
// pending user release.
const PendingReleaseCheckInterval = 1 * time.Minute

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

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete;
//+kubebuilder:rbac:groups=dataplane.openstack.org,resources=openstackdataplanenodesets,verbs=get;list;watch

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

	// Get RabbitMQ cluster
	rabbit, err := getRabbitmqCluster(ctx, helper, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Wait for RabbitMQ cluster to be ready
	if err := checkClusterReadiness(rabbit); err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			rabbitmqv1.TransportURLInProgressMessage))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Get cluster admin secret for connection details
	if rabbit.Status.DefaultUser == nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			rabbitmqv1.TransportURLInProgressMessage))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}
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

	// Migration: clean up legacy per-TransportURL owned CRs (remove old finalizers).
	// Legacy CRs are kept alive (owner ref to TransportURL) until the TransportURL is deleted,
	// at which point Kubernetes GC deletes them and the user controller's safety check prevents
	// the RabbitMQ user from being deleted (the canonical CR still exists).
	if _, err := r.cleanupLegacyOwnedResources(ctx, instance); err != nil {
		Log.Error(err, "Failed to clean up legacy owned resources")
		return ctrl.Result{}, err
	}

	// Determine credentials and vhost
	var finalUsername, finalPassword, vhostName string
	var userRef, vhostRef string

	if instance.Spec.UserRef != "" {
		userRef = instance.Spec.UserRef
	} else if instance.Spec.Username != "" {
		// Create RabbitMQVhost if needed
		vhostName = instance.Spec.Vhost
		if vhostName == "" {
			vhostName = "/"
		}
		if vhostName != "/" {
			vhostRef = rabbitmqv1.CanonicalVhostName(instance.Spec.RabbitmqClusterName, vhostName)
			perConsumerFinalizer := rabbitmqv1.TransportURLFinalizerFor(instance.Name)
			vhost := &rabbitmqv1.RabbitMQVhost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vhostRef,
					Namespace: instance.Namespace,
				},
			}
			if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, vhost, func() error {
				controllerutil.AddFinalizer(vhost, perConsumerFinalizer)
				if err := controllerutil.SetOwnerReference(rabbit, vhost, r.Scheme); err != nil {
					return err
				}
				vhost.Spec.RabbitmqClusterName = instance.Spec.RabbitmqClusterName
				vhost.Spec.Name = vhostName
				return nil
			}); err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
		}

		userRef = rabbitmqv1.CanonicalUserName(instance.Spec.RabbitmqClusterName, vhostName, instance.Spec.Username)
		perConsumerUserFinalizer := rabbitmqv1.TransportURLFinalizerFor(instance.Name)
		user := &rabbitmqv1.RabbitMQUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRef,
				Namespace: instance.Namespace,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, user, func() error {
			controllerutil.AddFinalizer(user, perConsumerUserFinalizer)
			// Clear orphaned label — this consumer is (re)claiming the user CR
			labels := user.GetLabels()
			if labels != nil {
				delete(labels, rabbitmqv1.RabbitMQUserOrphanedLabel)
				user.SetLabels(labels)
			}
			if err := controllerutil.SetOwnerReference(rabbit, user, r.Scheme); err != nil {
				return err
			}
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

	// Delete legacy CRs now that the canonical ones exist.
	// The user/vhost controller safety checks will find the canonical CRs and skip the
	// RabbitMQ API delete. This must run AFTER canonical CR creation above.
	if err := r.deleteLegacyOwnedResources(ctx, instance); err != nil {
		Log.Error(err, "Failed to delete legacy owned resources")
	}

	// Credential rotation: when the user ref changes, defer finalizer removal
	// until NodeSet deployments push the new credentials to all nodes. This
	// prevents the old RabbitMQ user from being deleted while computes still
	// use its credentials.
	perConsumerFin := rabbitmqv1.TransportURLFinalizerFor(instance.Name)
	if instance.Status.RabbitmqUserRef != "" && instance.Status.RabbitmqUserRef != userRef {
		// Force-release any earlier pending user first (double rotation edge case)
		if instance.Status.PreviousRabbitmqUserRef != "" && instance.Status.PreviousRabbitmqUserRef != instance.Status.RabbitmqUserRef {
			if _, err := r.releasePendingUser(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Only initialize pending release on the first detection — subsequent
		// reconciles re-enter this block (RabbitmqUserRef not yet updated) and
		// must not reset NodeSetSynced.
		if instance.Status.PreviousRabbitmqUserRef != instance.Status.RabbitmqUserRef {
			instance.Status.PreviousRabbitmqUserRef = instance.Status.RabbitmqUserRef
			instance.Status.NodeSetSynced = ""
			Log.Info("Credential rotation detected, deferring old user release",
				"oldUser", instance.Status.RabbitmqUserRef, "newUser", userRef)
		}
	}
	if instance.Status.RabbitmqVhost != "" && instance.Status.RabbitmqVhost != "/" {
		oldClusterName := instance.Status.RabbitmqClusterName
		if oldClusterName == "" {
			oldClusterName = instance.Spec.RabbitmqClusterName
		}
		oldVhostRef := rabbitmqv1.CanonicalVhostName(oldClusterName, instance.Status.RabbitmqVhost)
		if oldVhostRef != vhostRef {
			if err := r.removeFinalizerFromCR(ctx, &rabbitmqv1.RabbitMQVhost{}, oldVhostRef, instance.Namespace, perConsumerFin); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.deleteOrphanedCR(ctx, &rabbitmqv1.RabbitMQVhost{}, oldVhostRef, instance.Namespace); err != nil {
				return ctrl.Result{}, err
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
		usernameBytes, ok := userSecret.Data["username"]
		if !ok {
			err := fmt.Errorf("username key not found in user secret %s", rabbitUser.Status.SecretName)
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		passwordBytes, ok := userSecret.Data["password"]
		if !ok {
			err := fmt.Errorf("password key not found in user secret %s", rabbitUser.Status.SecretName)
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		finalUsername = string(usernameBytes)
		finalPassword = string(passwordBytes)
		vhostName = rabbitUser.Status.Vhost
		if vhostName == "" {
			vhostName = "/"
		}
	} else {
		// Use default cluster admin credentials
		finalUsername = string(adminUsername)
		finalPassword = string(adminPassword)
		vhostName = "/"
	}

	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	Log.Info(fmt.Sprintf("rabbitmq cluster %s has TLS enabled: %t", rabbit.Name, tlsEnabled))

	// Get RabbitMq CR for both secret generation and status update
	rabbitmqCR := &rabbitmqv1.RabbitMq{}
	rabbitmqCRFound := true
	err = r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitmqCR)
	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get RabbitMQ CR %s: %w", instance.Spec.RabbitmqClusterName, err)
		}
		rabbitmqCRFound = false
		Log.Info(fmt.Sprintf("RabbitMQ CR %s not found, using fallback values", instance.Spec.RabbitmqClusterName))
	}

	// Build list of hosts - use ServiceHostnames from status if available, otherwise use host from secret
	var hosts []string
	if rabbitmqCRFound && len(rabbitmqCR.Status.ServiceHostnames) > 0 {
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
	if rabbitmqCRFound {
		Log.Info(fmt.Sprintf("Found RabbitMQ CR: %s", rabbitmqCR.Name))

		// Determine quorum setting - prefer Spec over Status
		// Spec represents the configured queue type and is set immediately when the CR is created,
		// while Status.QueueType is updated asynchronously after cluster initialization.
		// This prevents a race condition where TransportURL reconciles before Status.QueueType is set.
		if rabbitmqCR.Spec.QueueType != nil {
			quorum = *rabbitmqCR.Spec.QueueType == rabbitmqv1.QueueTypeQuorum
			Log.Info(fmt.Sprintf("Setting quorum to: %t based on spec QueueType", quorum))
		} else if rabbitmqCR.Status.QueueType != "" {
			quorum = rabbitmqCR.Status.QueueType == rabbitmqv1.QueueTypeQuorum
			Log.Info(fmt.Sprintf("Setting quorum to: %t based on status QueueType (spec not set)", quorum))
		} else {
			// Default to false if neither is set - this should not normally happen
			// as the webhook should always set Spec.QueueType
			Log.Info("WARNING: Setting quorum to: false (neither spec nor status QueueType set - this is unexpected)",
				"rabbitmq", rabbitmqCR.Name,
				"namespace", rabbitmqCR.Namespace)
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

	// Create or update the transport URL secret.
	// During credential rotation, create a new immutable secret so consumers
	// can safely switch via the finalizer protocol. Otherwise patch the
	// existing mutable secret.
	if instance.Status.PreviousRabbitmqUserRef != "" && instance.Status.PreviousSecretName == "" {
		// First reconcile after rotation detection: create new immutable secret
		newSecret, hash, err := r.createImmutableTransportSecret(ctx, instance, finalUsername, finalPassword, hosts, string(port), vhostName, tlsEnabled, quorum)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.TransportURLReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				rabbitmqv1.TransportURLReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		instance.Status.PreviousSecretName = instance.Status.SecretName
		instance.Status.SecretName = newSecret.Name
		instance.Status.SecretHash = hash
		Log.Info("Created immutable transport secret for rotation",
			"secret", newSecret.Name,
			"previousSecret", instance.Status.PreviousSecretName)
	} else if instance.Status.PreviousSecretName == "" {
		// Normal path: create/patch mutable secret
		secret := r.createTransportURLSecret(instance, finalUsername, finalPassword, hosts, string(port), vhostName, tlsEnabled, quorum)

		// After credential rotation, SecretName points to an immutable secret
		// with a content-hash suffix. If the transport URL data hasn't changed,
		// keep the mutable secret up-to-date but don't flip SecretName back to
		// the base name — consumers would interpret that as a second rotation.
		skipSecretNameFlip := false
		if instance.Status.SecretName != "" && instance.Status.SecretName != secret.Name {
			newHash, err := oko_secret.Hash(secret)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to hash transport secret: %w", err)
			}
			if instance.Status.SecretHash == newHash {
				skipSecretNameFlip = true
			}
		}

		hash, op, err := oko_secret.CreateOrPatchSecret(ctx, helper, instance, secret)
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
		if !skipSecretNameFlip {
			if instance.Status.SecretName != "" && instance.Status.SecretName != secret.Name {
				instance.Status.PreviousSecretName = instance.Status.SecretName
			}
			instance.Status.SecretName = secret.Name
			instance.Status.SecretHash = hash
		}
	}
	// else: rotation in progress, immutable secret already created — keep
	// current status values and proceed to tryReleasePendingUser.

	// Update the CR status with actual values used
	instance.Status.RabbitmqClusterName = instance.Spec.RabbitmqClusterName
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

	// Release pending old user if deployment has completed
	if instance.Status.PreviousRabbitmqUserRef != "" {
		released, err := r.tryReleasePendingUser(ctx, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !released {
			Log.Info("Credential rotation in progress, old user retained until full NodeSet deployment completes",
				"oldUser", instance.Status.PreviousRabbitmqUserRef,
				"newUser", instance.Status.RabbitmqUserRef)
			return ctrl.Result{RequeueAfter: PendingReleaseCheckInterval}, nil
		}
	}

	// Clean up orphaned previous transport secrets that are no longer part of
	// an active rotation (e.g., immutable secrets left behind after switching
	// back to the standard mutable secret).
	if instance.Status.PreviousSecretName != "" && instance.Status.PreviousRabbitmqUserRef == "" {
		oldSecret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      instance.Status.PreviousSecretName,
			Namespace: instance.Namespace,
		}, oldSecret)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err == nil && rabbitmqv1.HasTransportConsumerFinalizer(oldSecret) {
			Log.Info("Waiting for consumer to release previous transport secret",
				"secret", instance.Status.PreviousSecretName)
			return ctrl.Result{RequeueAfter: PendingReleaseCheckInterval}, nil
		}
		if err := r.cleanupOldTransportSecret(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		instance.Status.PreviousSecretName = ""
	}

	// Only TransportURLs with names >61 chars need a periodic requeue because
	// their finalizer names are truncated+hashed, breaking the watch-based
	// reverse mapping in findTransportURLsFromFinalizers.
	if len(instance.Name) > rabbitmqv1.MaxTransportURLDirectName {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
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
	if vhost == "" {
		vhost = "/"
	} else if vhost != "/" && vhost[0] != '/' {
		vhost = "/" + vhost
	}

	// Build transport URL with all hosts.
	// URL-encode username and password to handle special characters
	// (e.g., @, :, /, %) that would break AMQP URL parsing.
	encodedUser := url.QueryEscape(username)
	encodedPass := url.QueryEscape(password)
	var hostParts []string
	for _, host := range hosts {
		hostParts = append(hostParts, fmt.Sprintf("%s:%s@%s:%s", encodedUser, encodedPass, host, port))
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

// createImmutableTransportSecret creates a new immutable secret for credential
// rotation. The secret name includes a content hash suffix so it is unique per
// credential set. A protection finalizer is added atomically to prevent
// premature deletion while the TransportURL still references it.
func (r *TransportURLReconciler) createImmutableTransportSecret(
	ctx context.Context,
	instance *rabbitmqv1.TransportURL,
	username string,
	password string,
	hosts []string,
	port string,
	vhost string,
	tlsEnabled bool,
	quorum bool,
) (*corev1.Secret, string, error) {
	template := r.createTransportURLSecret(instance, username, password, hosts, port, vhost, tlsEnabled, quorum)

	contentHash, err := oko_secret.Hash(template)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash transport secret data: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       fmt.Sprintf("rabbitmq-transport-url-%s-%s", instance.Name, contentHash[:8]),
			Namespace:  instance.Namespace,
			Finalizers: []string{rabbitmqv1.TransportSecretProtectionFinalizer},
		},
		Data:      template.Data,
		Immutable: ptr.To(true),
	}
	if err := controllerutil.SetControllerReference(instance, secret, r.Scheme); err != nil {
		return nil, "", fmt.Errorf("failed to set controller reference on transport secret: %w", err)
	}

	if err := r.Create(ctx, secret); err != nil {
		if !k8s_errors.IsAlreadyExists(err) {
			return nil, "", fmt.Errorf("failed to create immutable transport secret %s: %w", secret.Name, err)
		}
	}

	return secret, contentHash, nil
}

// cleanupOldTransportSecret removes the protection finalizer from the previous
// transport secret and deletes it. No-op if PreviousSecretName is empty or
// the secret no longer exists.
func (r *TransportURLReconciler) cleanupOldTransportSecret(ctx context.Context, instance *rabbitmqv1.TransportURL) error {
	secretName := instance.Status.PreviousSecretName
	if secretName == "" {
		return nil
	}
	Log := r.GetLogger(ctx)

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: instance.Namespace}, secret); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get old transport secret %s: %w", secretName, err)
	}

	if controllerutil.RemoveFinalizer(secret, rabbitmqv1.TransportSecretProtectionFinalizer) {
		if err := r.Update(ctx, secret); err != nil {
			if k8s_errors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to remove protection finalizer from %s: %w", secretName, err)
		}
	}

	Log.Info("Deleting old transport secret", "secret", secretName)
	if err := r.Delete(ctx, secret); err != nil && !k8s_errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete old transport secret %s: %w", secretName, err)
	}

	return nil
}

// fields to index to reconcile when change
const (
	rabbitmqClusterNameField = ".spec.rabbitmqClusterName"
	transportURLFinalizer    = "transporturl.rabbitmq.openstack.org"
)

func (r *TransportURLReconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1.TransportURL, _ *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling delete")

	perConsumerFinalizer := rabbitmqv1.TransportURLFinalizerFor(instance.Name)

	// Release any pending user from a credential rotation that hasn't completed
	if instance.Status.PreviousRabbitmqUserRef != "" {
		if err := r.releaseUserFinalizer(ctx, instance.Status.PreviousRabbitmqUserRef, instance.Namespace, perConsumerFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.cleanupOldTransportSecret(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		instance.Status.PreviousRabbitmqUserRef = ""
		instance.Status.PreviousSecretName = ""
		instance.Status.NodeSetSynced = ""
	}

	// Remove per-consumer finalizer from shared user CR
	if instance.Spec.Username != "" {
		vhostName := instance.Spec.Vhost
		if vhostName == "" {
			vhostName = "/"
		}
		userRef := rabbitmqv1.CanonicalUserName(instance.Spec.RabbitmqClusterName, vhostName, instance.Spec.Username)
		user := &rabbitmqv1.RabbitMQUser{}
		if err := r.Get(ctx, types.NamespacedName{Name: userRef, Namespace: instance.Namespace}, user); err == nil {
			updated := false
			if controllerutil.RemoveFinalizer(user, perConsumerFinalizer) {
				updated = true
			}
			// Determine orphaned state based on remaining consumer finalizers
			if user.DeletionTimestamp.IsZero() {
				hasConsumers := false
				for _, f := range user.GetFinalizers() {
					if strings.HasPrefix(f, rabbitmqv1.TransportURLFinalizerPrefix) {
						hasConsumers = true
						break
					}
				}
				labels := user.GetLabels()
				if labels == nil {
					labels = map[string]string{}
				}
				if !hasConsumers {
					if labels[rabbitmqv1.RabbitMQUserOrphanedLabel] != "true" {
						labels[rabbitmqv1.RabbitMQUserOrphanedLabel] = "true"
						user.SetLabels(labels)
						updated = true
						Log.Info("Marking user CR as orphaned (no remaining consumers)", "name", userRef)
					}
				} else if labels[rabbitmqv1.RabbitMQUserOrphanedLabel] == "true" {
					delete(labels, rabbitmqv1.RabbitMQUserOrphanedLabel)
					user.SetLabels(labels)
					updated = true
				}
			}
			if updated {
				if err := r.Update(ctx, user); err != nil && !k8s_errors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("failed to update user %s during delete: %w", userRef, err)
				}
			}
		} else if !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get user %s for cleanup: %w", userRef, err)
		}

		// Remove per-consumer finalizer from shared vhost CR
		if vhostName != "/" {
			vhostCRName := rabbitmqv1.CanonicalVhostName(instance.Spec.RabbitmqClusterName, vhostName)
			vhost := &rabbitmqv1.RabbitMQVhost{}
			if err := r.Get(ctx, types.NamespacedName{Name: vhostCRName, Namespace: instance.Namespace}, vhost); err == nil {
				if controllerutil.RemoveFinalizer(vhost, perConsumerFinalizer) {
					if err := r.Update(ctx, vhost); err != nil && !k8s_errors.IsNotFound(err) {
						return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from vhost %s: %w", vhostCRName, err)
					}
				}
			} else if !k8s_errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed to get vhost %s for cleanup: %w", vhostCRName, err)
			}
		}
	}

	// Migration: clean up legacy per-TransportURL owned CRs
	if _, err := r.cleanupLegacyOwnedResources(ctx, instance); err != nil {
		Log.Error(err, "Failed to clean up legacy owned resources")
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

	b := ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1.TransportURL{}).
		Owns(&corev1.Secret{}).
		Watches(
			&rabbitmqv1.RabbitMQUser{},
			handler.EnqueueRequestsFromMapFunc(r.findTransportURLsFromFinalizers),
		).
		Watches(
			&rabbitmqv1.RabbitMQVhost{},
			handler.EnqueueRequestsFromMapFunc(r.findTransportURLsFromFinalizers),
		).
		Watches(
			&rabbitmqv1.RabbitMq{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		)

	gk := schema.GroupKind{Group: edpm.NodeSetGVK.Group, Kind: edpm.NodeSetGVK.Kind}
	if _, err := mgr.GetRESTMapper().RESTMapping(gk, edpm.NodeSetGVK.Version); err == nil {
		b = b.Watches(
			edpm.NewNodeSetObject(),
			handler.EnqueueRequestsFromMapFunc(r.findTransportURLsWithPendingRelease),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		)
	}

	return b.Complete(r)
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

// findTransportURLsFromFinalizers maps changes on shared vhost/user CRs back to the
// TransportURLs that have per-consumer finalizers on them.
// Limitation: for TransportURL names >61 chars, TransportURLFinalizerFor truncates+hashes
// the name, so the reverse mapping here produces incorrect names. Those TransportURLs
// rely on requeue timers rather than this watch for reconciliation.
func (r *TransportURLReconciler) findTransportURLsFromFinalizers(_ context.Context, obj client.Object) []reconcile.Request {
	var requests []reconcile.Request
	for _, finalizer := range obj.GetFinalizers() {
		if strings.HasPrefix(finalizer, rabbitmqv1.TransportURLFinalizerPrefix) {
			turlName := finalizer[len(rabbitmqv1.TransportURLFinalizerPrefix):]
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      turlName,
					Namespace: obj.GetNamespace(),
				},
			})
		}
	}
	return requests
}

// findTransportURLsWithPendingRelease maps NodeSet changes to TransportURLs that have a
// pending user release (waiting for deployment to complete before releasing old credentials).
func (r *TransportURLReconciler) findTransportURLsWithPendingRelease(ctx context.Context, obj client.Object) []reconcile.Request {
	turlList := &rabbitmqv1.TransportURLList{}
	if err := r.List(ctx, turlList, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, turl := range turlList.Items {
		if turl.Status.PreviousRabbitmqUserRef != "" {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      turl.Name,
					Namespace: turl.Namespace,
				},
			})
		}
	}
	return requests
}

// deleteOrphanedCR deletes a shared user/vhost CR if no active consumer finalizers remain.
// Consumer finalizers are per-TransportURL (turl.openstack.org/t-*) and per-user on vhosts
// (rmquser.openstack.org/u-*). External finalizers do NOT block the Delete call — the
// user/vhost controllers handle waiting for external finalizers during their own reconcileDelete.
func (r *TransportURLReconciler) deleteOrphanedCR(ctx context.Context, obj client.Object, name, namespace string) error {
	Log := r.GetLogger(ctx)
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get %s: %w", name, err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return nil
	}
	for _, f := range obj.GetFinalizers() {
		if strings.HasPrefix(f, rabbitmqv1.TransportURLFinalizerPrefix) {
			return nil
		}
		if strings.HasPrefix(f, rabbitmqv1.UserVhostFinalizerPrefix) {
			Log.Info("Skipping deletion of vhost CR still referenced by user", "name", name, "finalizer", f)
			return nil
		}
	}
	Log.Info("Deleting orphaned CR (no remaining consumers)", "name", name)
	if err := r.Delete(ctx, obj); err != nil && !k8s_errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete orphaned CR %s: %w", name, err)
	}
	return nil
}

// tryReleasePendingUser checks whether it is safe to release the pending old
// user from a credential rotation.
//
// The flow is unified for all services:
//  1. Wait for the consumer finalizer on the old transport secret to be
//     removed, indicating the consuming service has completed its rollout
//     with the new credentials.
//  2. If NodeSets exist, check AreSecretHashesInSync. If hashes are out of
//     sync, the regenerated config secret is tracked by the dataplane and a
//     deployment is needed before the old user can be released. If hashes
//     are in sync, the secret is either not tracked by the dataplane or the
//     deployment has already completed.
//
// Returns (true, nil) if the user was released.
func (r *TransportURLReconciler) tryReleasePendingUser(ctx context.Context, instance *rabbitmqv1.TransportURL) (bool, error) {
	Log := r.GetLogger(ctx)
	pendingRef := instance.Status.PreviousRabbitmqUserRef

	if instance.Status.PreviousSecretName != "" {
		oldSecret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      instance.Status.PreviousSecretName,
			Namespace: instance.Namespace,
		}, oldSecret)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return false, err
		}
		if err == nil && rabbitmqv1.HasTransportConsumerFinalizer(oldSecret) {
			Log.Info("Waiting for consumer to release old transport secret",
				"secret", instance.Status.PreviousSecretName,
				"pendingUser", pendingRef)
			return false, nil
		}
	}

	haveNS, err := edpm.HaveNodeSets(ctx, r.Client, instance.Namespace)
	if err != nil {
		Log.Error(err, "Failed to check for nodesets")
		return false, nil
	}
	if haveNS {
		inSync, info, err := edpm.AreSecretHashesInSync(ctx, r.Client, instance.Namespace)
		if err != nil {
			Log.Error(err, "Failed to check nodeset sync status for pending release")
			return false, nil
		}
		if !inSync {
			Log.Info("Waiting for dataplane deployment to complete",
				"pendingUser", pendingRef, "info", info)
			return false, nil
		}
	}

	return r.releasePendingUser(ctx, instance)
}

// releaseUserFinalizer removes the per-consumer finalizer from a RabbitMQUser
// and marks it as orphaned if no consumer finalizers remain.
func (r *TransportURLReconciler) releaseUserFinalizer(ctx context.Context, userRef, namespace, finalizer string) error {
	Log := r.GetLogger(ctx)

	user := &rabbitmqv1.RabbitMQUser{}
	if err := r.Get(ctx, types.NamespacedName{Name: userRef, Namespace: namespace}, user); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get user %s: %w", userRef, err)
	}

	updated := controllerutil.RemoveFinalizer(user, finalizer)

	if user.DeletionTimestamp.IsZero() {
		hasConsumers := false
		for _, f := range user.GetFinalizers() {
			if strings.HasPrefix(f, rabbitmqv1.TransportURLFinalizerPrefix) {
				hasConsumers = true
				break
			}
		}
		if !hasConsumers {
			labels := user.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			if labels[rabbitmqv1.RabbitMQUserOrphanedLabel] != "true" {
				labels[rabbitmqv1.RabbitMQUserOrphanedLabel] = "true"
				user.SetLabels(labels)
				updated = true
				Log.Info("Marking user CR as orphaned (no remaining consumers)", "name", userRef)
			}
		}
	}

	if updated {
		if err := r.Update(ctx, user); err != nil && !k8s_errors.IsNotFound(err) {
			return fmt.Errorf("failed to release user %s: %w", userRef, err)
		}
	}

	return nil
}

// releasePendingUser removes the consumer finalizer from the pending user,
// marks it as orphaned if no consumer finalizers remain, and cleans up the
// old transport secret. Clears the pending release fields from status.
func (r *TransportURLReconciler) releasePendingUser(ctx context.Context, instance *rabbitmqv1.TransportURL) (bool, error) {
	Log := r.GetLogger(ctx)
	pendingRef := instance.Status.PreviousRabbitmqUserRef
	perConsumerFin := rabbitmqv1.TransportURLFinalizerFor(instance.Name)

	Log.Info("Releasing pending user (deployment verified)", "pendingUser", pendingRef)

	if err := r.releaseUserFinalizer(ctx, pendingRef, instance.Namespace, perConsumerFin); err != nil {
		return false, err
	}

	if err := r.cleanupOldTransportSecret(ctx, instance); err != nil {
		return false, err
	}

	instance.Status.PreviousRabbitmqUserRef = ""
	instance.Status.PreviousSecretName = ""
	instance.Status.NodeSetSynced = ""
	return true, nil
}

// removeFinalizerFromCR removes a finalizer from a CR by name. NotFound is not an error.
func (r *TransportURLReconciler) removeFinalizerFromCR(ctx context.Context, obj client.Object, name, namespace, finalizer string) error {
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get %s for finalizer removal: %w", name, err)
	}
	if controllerutil.RemoveFinalizer(obj, finalizer) {
		if err := r.Update(ctx, obj); err != nil && !k8s_errors.IsNotFound(err) {
			return fmt.Errorf("failed to remove finalizer %s from %s: %w", finalizer, name, err)
		}
	}
	return nil
}

// cleanupLegacyOwnedResources removes legacy TransportURL finalizers from CRs that were
// created with the old per-TransportURL naming scheme (owner-ref based).
func (r *TransportURLReconciler) cleanupLegacyOwnedResources(ctx context.Context, instance *rabbitmqv1.TransportURL) (bool, error) {
	Log := r.GetLogger(ctx)
	cleaned := false

	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list users for legacy cleanup: %w", err)
	}
	for i := range userList.Items {
		user := &userList.Items[i]
		if !object.CheckOwnerRefExist(instance.GetUID(), user.GetOwnerReferences()) {
			continue
		}
		updated := false
		if controllerutil.RemoveFinalizer(user, rabbitmqv1.TransportURLFinalizer) {
			updated = true
		}
		if controllerutil.RemoveFinalizer(user, rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer) {
			updated = true
		}
		if updated {
			Log.Info("Cleaning up legacy owned user", "user", user.Name)
			if err := r.Update(ctx, user); err != nil && !k8s_errors.IsNotFound(err) {
				return false, fmt.Errorf("failed to clean up legacy user %s: %w", user.Name, err)
			}
			cleaned = true
		}

		// Migrate credentials: copy the legacy user's secret to the canonical secret name
		// so the canonical CR uses the same password and doesn't break existing connections.
		if user.Status.SecretName != "" {
			vhostName := instance.Spec.Vhost
			if vhostName == "" {
				vhostName = "/"
			}
			canonicalUserRef := rabbitmqv1.CanonicalUserName(instance.Spec.RabbitmqClusterName, vhostName, user.Spec.Username)
			canonicalSecretName := fmt.Sprintf("rabbitmq-user-%s", canonicalUserRef)
			if user.Status.SecretName != canonicalSecretName {
				legacySecret := &corev1.Secret{}
				if err := r.Get(ctx, types.NamespacedName{Name: user.Status.SecretName, Namespace: instance.Namespace}, legacySecret); err == nil {
					canonicalSecret := &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      canonicalSecretName,
							Namespace: instance.Namespace,
						},
					}
					legacyUsername, uOk := legacySecret.Data["username"]
					legacyPassword, pOk := legacySecret.Data["password"]
					if uOk && pOk {
						if err := r.Get(ctx, types.NamespacedName{Name: canonicalSecretName, Namespace: instance.Namespace}, canonicalSecret); err != nil {
							if k8s_errors.IsNotFound(err) {
								canonicalSecret.Data = map[string][]byte{
									"username": legacyUsername,
									"password": legacyPassword,
								}
								if err := r.Create(ctx, canonicalSecret); err != nil && !k8s_errors.IsAlreadyExists(err) {
									Log.Error(err, "Failed to pre-create canonical secret with legacy credentials", "secret", canonicalSecretName)
								} else {
									Log.Info("Pre-created canonical secret with legacy credentials", "legacy", user.Status.SecretName, "canonical", canonicalSecretName)
								}
							}
						}
					} else {
						Log.Info("Skipping credential migration - legacy secret missing username/password keys", "secret", user.Status.SecretName)
					}
				}
			}
		}
	}

	vhostList := &rabbitmqv1.RabbitMQVhostList{}
	if err := r.List(ctx, vhostList, client.InNamespace(instance.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list vhosts for legacy cleanup: %w", err)
	}
	for i := range vhostList.Items {
		vhost := &vhostList.Items[i]
		if !object.CheckOwnerRefExist(instance.GetUID(), vhost.GetOwnerReferences()) {
			continue
		}
		if controllerutil.RemoveFinalizer(vhost, rabbitmqv1.TransportURLFinalizer) {
			Log.Info("Cleaning up legacy owned vhost", "vhost", vhost.Name)
			if err := r.Update(ctx, vhost); err != nil && !k8s_errors.IsNotFound(err) {
				return false, fmt.Errorf("failed to clean up legacy vhost %s: %w", vhost.Name, err)
			}
			cleaned = true
		}
	}

	return cleaned, nil
}

// deleteLegacyOwnedResources explicitly deletes legacy per-TransportURL owned CRs.
// This must be called AFTER the canonical CRs are created, so the user/vhost controller
// safety checks find the canonical CR and skip the RabbitMQ API delete.
func (r *TransportURLReconciler) deleteLegacyOwnedResources(ctx context.Context, instance *rabbitmqv1.TransportURL) error {
	Log := r.GetLogger(ctx)

	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err != nil {
		return fmt.Errorf("failed to list users for legacy deletion: %w", err)
	}
	for i := range userList.Items {
		user := &userList.Items[i]
		if !object.CheckOwnerRefExist(instance.GetUID(), user.GetOwnerReferences()) {
			continue
		}
		if user.DeletionTimestamp.IsZero() {
			Log.Info("Deleting legacy owned user", "user", user.Name)
			if err := r.Delete(ctx, user); err != nil && !k8s_errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete legacy user %s: %w", user.Name, err)
			}
		}
	}

	vhostList := &rabbitmqv1.RabbitMQVhostList{}
	if err := r.List(ctx, vhostList, client.InNamespace(instance.Namespace)); err != nil {
		return fmt.Errorf("failed to list vhosts for legacy deletion: %w", err)
	}
	for i := range vhostList.Items {
		vhost := &vhostList.Items[i]
		if !object.CheckOwnerRefExist(instance.GetUID(), vhost.GetOwnerReferences()) {
			continue
		}
		if vhost.DeletionTimestamp.IsZero() {
			Log.Info("Deleting legacy owned vhost", "vhost", vhost.Name)
			if err := r.Delete(ctx, vhost); err != nil && !k8s_errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete legacy vhost %s: %w", vhost.Name, err)
			}
		}
	}

	return nil
}

// GetRabbitmqCluster - get RabbitMq object in namespace
func getRabbitmqCluster(
	ctx context.Context,
	h *helper.Helper,
	instance *rabbitmqv1.TransportURL,
) (*rabbitmqv1.RabbitMq, error) {
	rabbitMqCluster := &rabbitmqv1.RabbitMq{}

	err := h.GetClient().Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitMqCluster)

	return rabbitMqCluster, err
}
