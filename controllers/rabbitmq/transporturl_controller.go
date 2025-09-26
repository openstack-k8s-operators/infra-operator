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
//+kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch
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

	return r.reconcileNormal(ctx, instance, helper)
}

func (r *TransportURLReconciler) reconcileNormal(ctx context.Context, instance *rabbitmqv1.TransportURL, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// TODO (implement a watch on the rabbitmq cluster resources to update things if there are changes)
	rabbit, err := getRabbitmqCluster(ctx, helper, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Wait on RabbitmqCluster to be ready
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

	// TODO(dprince): Future we may want to use vhosts for each OpenStackService instead.
	// vhosts would likely require use of https://github.com/rabbitmq/messaging-topology-operator/ which we do not yet include
	rabbitSecret, _, err := oko_secret.GetSecret(ctx, helper, rabbit.Status.DefaultUser.SecretReference.Name, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				rabbitmqv1.TransportURLReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
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

	var username string
	if u, ok := rabbitSecret.Data["username"]; ok {
		username = string(u)
	} else {
		err := fmt.Errorf("username does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	var password string
	if p, ok := rabbitSecret.Data["password"]; ok {
		password = string(p)
	} else {
		err := fmt.Errorf("password does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	var host string
	if h, ok := rabbitSecret.Data["host"]; ok {
		host = string(h)
	} else {
		err := fmt.Errorf("host does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	var port string
	if p, ok := rabbitSecret.Data["port"]; ok {
		port = string(p)
	} else {
		err := fmt.Errorf("port does not exist in rabbitmq secret %s", rabbitSecret.Name)
		instance.Status.Conditions.Set(condition.FalseCondition(
			rabbitmqv1.TransportURLReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			rabbitmqv1.TransportURLReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	tlsEnabled := rabbit.Spec.TLS.SecretName != ""

	Log.Info(fmt.Sprintf("rabbitmq cluster %s has TLS enabled: %t", rabbit.Name, tlsEnabled))

	// Get RabbitMq CR for both secret generation and status update
	rabbitmqCR := &rabbitmqv1.RabbitMq{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Spec.RabbitmqClusterName, Namespace: instance.Namespace}, rabbitmqCR)

	// Determine quorum setting for secret generation
	quorum := false
	if err != nil {
		Log.Info(fmt.Sprintf("Could not fetch RabbitMQ CR: %v", err))
		// Default to false for quorum if we can't fetch the CR
	} else {
		Log.Info(fmt.Sprintf("Found RabbitMQ CR: %s", rabbitmqCR.Name))

		quorum = rabbitmqCR.Status.QueueType == "Quorum"
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
	secret := r.createTransportURLSecret(instance, string(username), string(password), string(host), string(port), tlsEnabled, quorum)
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

	// Update the CR and return
	instance.Status.SecretName = secret.Name

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
	host string,
	port string,
	tlsEnabled bool,
	quorum bool,
) *corev1.Secret {
	query := ""
	if tlsEnabled {
		query += "?ssl=1"
	} else {
		query += "?ssl=0"
	}

	// Create a new secret with the transport URL for this CR
	data := map[string][]byte{
		"transport_url": fmt.Appendf(nil, "rabbit://%s:%s@%s:%s/%s", username, password, host, port, query),
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
)

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
