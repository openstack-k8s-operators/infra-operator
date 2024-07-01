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

package memcached

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

	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// GetClient -
func (r *ServiceTransportReconciler) GetClient() client.Client {
	return r.Client
}

// GetKClient -
func (r *ServiceTransportReconciler) GetKClient() kubernetes.Interface {
	return r.Kclient
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func GetLog(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("ServiceTransport")
}

// GetScheme -
func (r *ServiceTransportReconciler) GetScheme() *runtime.Scheme {
	return r.Scheme
}

// ServiceTransportReconciler reconciles a ServiceTransport object
type ServiceTransportReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *ServiceTransportReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("ServiceTransport")
}

//+kubebuilder:rbac:groups=memcached.openstack.org,resources=transporturls,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=memcached.openstack.org,resources=transporturls/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=memcached.openstack.org,resources=transporturls/finalizers,verbs=update
//+kubebuilder:rbac:groups=memcached.com,resources=memcachedclusters,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete;

// Reconcile - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *ServiceTransportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)
	// Fetch the ServiceTransport instance
	instance := &memcachedv1.ServiceTransport{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
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

	//
	// initialize status
	//
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}

		cl := condition.CreateList(condition.UnknownCondition(memcachedv1.ServiceTransportReadyCondition, condition.InitReason, memcachedv1.ServiceTransportReadyInitMessage))

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		if err := r.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
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

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// update the Ready condition based on the sub conditions
		if instance.Status.Conditions.AllSubConditionIsTrue() {
			instance.Status.Conditions.MarkTrue(
				condition.ReadyCondition, condition.ReadyMessage)
		} else {
			// something is not ready so reset the Ready condition
			instance.Status.Conditions.MarkUnknown(
				condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage)
			// and recalculate it based on the state of the rest of the conditions
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	return r.reconcileNormal(ctx, instance, helper)
}

func (r *ServiceTransportReconciler) reconcileNormal(ctx context.Context, instance *memcachedv1.ServiceTransport, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	// TODO (implement a watch on the memcached cluster resources to update things if there are changes)
	memcached, err := getMemcachedInstance(ctx, helper, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Wait on memcached to be ready
	if !memcached.IsReady() {
		instance.Status.Conditions.Set(condition.FalseCondition(
			memcachedv1.ServiceTransportReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			memcachedv1.ServiceTransportInProgressMessage))
		return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
	}

	// Create a new secret with the transport URL for this CR
	secret := r.createServiceTransportSecret(instance, memcached)
	_, op, err := oko_secret.CreateOrPatchSecret(ctx, helper, instance, secret)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			memcachedv1.ServiceTransportReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			memcachedv1.ServiceTransportReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		instance.Status.Conditions.Set(condition.FalseCondition(
			memcachedv1.ServiceTransportReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			memcachedv1.ServiceTransportReadyInitMessage))
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Update the CR and return
	instance.Status.SecretName = secret.Name
	instance.Status.Conditions.MarkTrue(memcachedv1.ServiceTransportReadyCondition, memcachedv1.ServiceTransportReadyMessage)

	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// Create k8s secret with transport URL
func (r *ServiceTransportReconciler) createServiceTransportSecret(
	transport *memcachedv1.ServiceTransport,
	memcached *memcachedv1.Memcached,
) *corev1.Secret {
	tlsEnabled := memcached.Status.TLSSupport
	servers := strings.Join(memcached.Status.ServerList, ",")
	serversWithInet := strings.Join(memcached.Status.ServerListWithInet, ",")
	osloCacheConfig := fmt.Sprintf(`enabled=true
tls_enabled=%t
backend=dogpile.cache.pymemcache
memcache_servers=%s`, tlsEnabled, servers)
	keystoneMiddlewareConfig := fmt.Sprintf(`memcached_servers=%s`, serversWithInet)

	// Create a new secret with the transport URL for this CR
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-transport-" + transport.Name,
			Namespace: transport.Namespace,
		},
		Data: map[string][]byte{
			"OsloCacheConfig":          []byte(osloCacheConfig),
			"KeystoneMiddlewareConfig": []byte(keystoneMiddlewareConfig),
		},
	}
}

// fields to index to reconcile when change
const (
	memcachedNameField = ".spec.ServiceName"
)

var allWatchFieldsTransport = []string{
	memcachedNameField,
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceTransportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// index caSecretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &memcachedv1.ServiceTransport{}, memcachedNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*memcachedv1.ServiceTransport)
		if cr.Spec.ServiceName == "" {
			return nil
		}
		return []string{cr.Spec.ServiceName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&memcachedv1.ServiceTransport{}).
		Owns(&corev1.Secret{}).
		Watches(
			&memcachedv1.Memcached{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

func (r *ServiceTransportReconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	for _, field := range allWatchFieldsTransport {
		crList := &memcachedv1.ServiceTransportList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(field, src.GetName()),
			Namespace:     src.GetNamespace(),
		}
		err := r.List(ctx, crList, listOps)
		if err != nil {
			return []reconcile.Request{}
		}

		for _, item := range crList.Items {
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

// getMemcachedInstance - get Memcached object in namespace
func getMemcachedInstance(
	ctx context.Context,
	h *helper.Helper,
	instance *memcachedv1.ServiceTransport,
) (*memcachedv1.Memcached, error) {
	memcached := &memcachedv1.Memcached{}

	err := h.GetClient().Get(ctx, types.NamespacedName{Name: instance.Spec.ServiceName, Namespace: instance.Namespace}, memcached)

	return memcached, err
}
