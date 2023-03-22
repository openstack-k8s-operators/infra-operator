/*
Copyright 2023.

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

package redis

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"

	redis "github.com/openstack-k8s-operators/infra-operator/pkg/redis"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	commondeployment "github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	commonservice "github.com/openstack-k8s-operators/lib-common/modules/common/service"
)

// Reconciler reconciles a Redis object
type Reconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// RBAC for redis resources
//+kubebuilder:rbac:groups=redis.openstack.org,resources=redises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=redis.openstack.org,resources=redises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=redis.openstack.org,resources=redises/finalizers,verbs=update

// RBAC for deployments and their pods
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;

// RBAC for services
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete;

// Reconcile - Redis
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	_ = log.FromContext(ctx)

	// Fetch the Redis instance
	instance := &redisv1beta1.Redis{}
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
		r.Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// Update the overall status condition if service is ready
		if instance.IsReady() {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)
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
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
		// initialize conditions used later as Status=Unknown
		cl := condition.CreateList(
			// endpoint for adoption redirect
			condition.UnknownCondition(condition.ExposeServiceReadyCondition, condition.InitReason, condition.ExposeServiceReadyInitMessage),
			// redis pods ready
			condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		return ctrl.Result{}, nil
	}

	//
	// Create/Update all the resources associated to this Redis instance
	//

	// Service to expose Redis pods
	commonsvc := commonservice.NewService(redis.Service(instance), map[string]string{}, time.Duration(5)*time.Second)
	sres, serr := commonsvc.CreateOrPatch(ctx, helper)
	if serr != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ExposeServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ExposeServiceReadyErrorMessage,
			err.Error()))
		return sres, serr
	}
	instance.Status.Conditions.MarkTrue(condition.ExposeServiceReadyCondition, condition.ExposeServiceReadyMessage)

	// Deployment
	commondeployment := commondeployment.NewDeployment(redis.Deployment(instance), time.Duration(5)*time.Second)
	sfres, sferr := commondeployment.CreateOrPatch(ctx, helper)
	if sferr != nil {
		return sfres, sferr
	}
	deployment := commondeployment.GetDeployment()

	//
	// Reconstruct the state of the redis resource based on the deployment and its pods
	//

	if deployment.Status.ReadyReplicas > 0 {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1beta1.Redis{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
