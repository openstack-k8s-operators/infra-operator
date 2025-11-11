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
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
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

//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=transporturls/finalizers,verbs=update
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch
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

	// Determine credentials and vhost
	var finalUsername, finalPassword, vhostName string
	var userRef string

	if instance.Spec.UserRef != "" {
		userRef = instance.Spec.UserRef
	} else if instance.Spec.Username != "" {
		// Create RabbitMQVhost if needed
		vhostName = instance.Spec.Vhost
		if vhostName == "" {
			vhostName = "/"
		}
		var vhostRef string
		if vhostName != "/" {
			vhostRef = fmt.Sprintf("%s-%s-vhost", instance.Name, vhostName)
			vhost := &rabbitmqv1.RabbitMQVhost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vhostRef,
					Namespace: instance.Namespace,
				},
				Spec: rabbitmqv1.RabbitMQVhostSpec{RabbitmqClusterName: instance.Spec.RabbitmqClusterName, Name: vhostName},
			}
			// Note: During normal reconciliation (not deletion), we return errors rather than
			// just logging them, as we need these operations to succeed for correct functionality.
			if err := controllerutil.SetControllerReference(instance, vhost, r.Scheme); err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
			if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, vhost, func() error {
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
			Spec: rabbitmqv1.RabbitMQUserSpec{RabbitmqClusterName: instance.Spec.RabbitmqClusterName, Username: instance.Spec.Username, VhostRef: vhostRef},
		}
		if err := controllerutil.SetControllerReference(instance, user, r.Scheme); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, user, func() error {
			// Add TransportURL finalizer to protect user while in use
			controllerutil.AddFinalizer(user, rabbitmqv1.TransportURLFinalizer)
			user.Spec.RabbitmqClusterName = instance.Spec.RabbitmqClusterName
			user.Spec.Username = instance.Spec.Username
			user.Spec.VhostRef = vhostRef
			return nil
		}); err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(rabbitmqv1.TransportURLReadyCondition, condition.ErrorReason, condition.SeverityWarning, rabbitmqv1.TransportURLReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

		// Remove TransportURL finalizer from previously used users that are no longer referenced
		// This handles credential rotation and rollback scenarios
		userList := &rabbitmqv1.RabbitMQUserList{}
		if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err == nil {
			for i := range userList.Items {
				oldUser := &userList.Items[i]
				// Check if this user is owned by this TransportURL
				isOwned := object.CheckOwnerRefExist(instance.GetUID(), oldUser.GetOwnerReferences())
				// If owned by this TransportURL but not the current userRef, remove finalizer
				if isOwned && oldUser.Name != userRef {
					if controllerutil.RemoveFinalizer(oldUser, rabbitmqv1.TransportURLFinalizer) {
						if err := r.Update(ctx, oldUser); err != nil {
							Log.Error(err, "Failed to remove TransportURL finalizer from old user", "user", oldUser.Name)
						}
					}
				}
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

	// Get RabbitMq CR for both secret generation and status update
	rabbitmqCR := &rabbitmqv1.RabbitMq{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitmqCR)

	// Build list of hosts - use ServiceHostnames from status if available, otherwise use host from secret
	var hosts []string
	if err == nil && len(rabbitmqCR.Status.ServiceHostnames) > 0 {
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
	if err != nil {
		Log.Info(fmt.Sprintf("Could not fetch RabbitMQ CR: %v", err))
		// Default to false for quorum if we can't fetch the CR
	} else {
		Log.Info(fmt.Sprintf("Found RabbitMQ CR: %s", rabbitmqCR.Name))

		quorum = rabbitmqCR.Status.QueueType == rabbitmqv1.QueueTypeQuorum
		Log.Info(fmt.Sprintf("Setting quorum to: %t based on status QueueType", quorum))

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

	instance.Status.Conditions.MarkTrue(rabbitmqv1.TransportURLReadyCondition, rabbitmqv1.TransportURLReadyMessage)

	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	Log.Info("Reconciled Service successfully")
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

	// Remove TransportURL finalizer from all owned users and vhosts
	userList := &rabbitmqv1.RabbitMQUserList{}
	if err := r.List(ctx, userList, client.InNamespace(instance.Namespace)); err == nil {
		for i := range userList.Items {
			user := &userList.Items[i]
			// Check if this user is owned by this TransportURL
			isOwned := object.CheckOwnerRefExist(instance.GetUID(), user.GetOwnerReferences())
			if isOwned {
				if controllerutil.RemoveFinalizer(user, rabbitmqv1.TransportURLFinalizer) {
					if err := r.Update(ctx, user); err != nil {
						Log.Error(err, "Failed to remove TransportURL finalizer from user", "user", user.Name)
						return ctrl.Result{}, err
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
						Log.Error(err, "Failed to remove TransportURL finalizer from vhost", "vhost", vhost.Name)
						return ctrl.Result{}, err
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
