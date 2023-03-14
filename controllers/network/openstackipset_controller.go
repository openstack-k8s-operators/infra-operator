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

package network

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/exp/maps"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	networkv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	ipam "github.com/openstack-k8s-operators/infra-operator/pkg/network/ipam"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
)

// OpenStackIPSetReconciler reconciles a OpenStackIPSet object
type OpenStackIPSetReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
	Log     logr.Logger
}

//+kubebuilder:rbac:groups=network.openstack.org,resources=openstackipsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.openstack.org,resources=openstacknets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.openstack.org,resources=openstackipsets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.openstack.org,resources=openstackipsets/finalizers,verbs=update
//+kubebuilder:rbac:groups=network.openstack.org,resources=openstacknets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OpenStackIPSet object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *OpenStackIPSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	_ = log.FromContext(ctx)
	result = ctrl.Result{}
	// TODO(user): your logic here
	r.Log.Info("Reconciling IPSet")

	instance := &networkv1beta1.OpenStackIPSet{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
			return result, nil
		}
		return result, err
	}

	h, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		r.Log,
	)

	if err != nil {
		// helper might be nil, so can't use util.LogErrorForObject since it requires helper as first arg
		r.Log.Error(err, fmt.Sprintf("unable to acquire helper for OpenStackIPSet %s", instance.Name))
		return result, err
	}

	defer func() {
		if err == nil {
			instance.Status.Conditions.MarkTrue(networkv1beta1.OpenStackIPSetReadyCondition, networkv1beta1.OpenStackIPSetReadyMessage)
		}
		// update the overall status condition if service is ready
		err := h.PatchInstance(ctx, instance)
		if err != nil {
			r.Log.Error(err, "Can't patch Instance")
			_err = err
			return
		}
	}()

	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, h.GetFinalizer()) {
		return result, err
	}

	if !instance.DeletionTimestamp.IsZero() {
		return r.ReconcileDelete(ctx, instance, h, req)
	}

	instance.InitCondition()

	// Build AssignIPDetails
	networks := instance.Spec.Networks

	if instance.Status.IPAddresses == nil {
		instance.Status.IPAddresses = make(map[string]networkv1beta1.IPReservation)
	}

	//find the the openstacknet objects
	for _, network := range networks {
		if _, ok := instance.Status.IPAddresses[network.Name]; ok {
			continue
		}
		osnet := &networkv1beta1.OpenStackNet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      network.Name,
				Namespace: instance.Namespace,
			},
		}
		osnetHelper, err := helper.NewHelper(
			osnet,
			r.Client,
			r.Kclient,
			r.Scheme,
			r.Log)

		if err != nil {
			r.Log.Error(err, fmt.Sprintf("Unable to acquire helper for OpenStackNet %s", osnet.Name))
			return result, err
		}
		err = r.Get(ctx, types.NamespacedName{Name: network.Name, Namespace: instance.Namespace}, osnet)
		if err != nil {
			r.Log.Error(err, "Network Not Found")
			return result, err
		}
		aid := ipam.AssignIPDetails{}
		for i, subnet := range osnet.Status.Subnets {
			if subnet.Name == network.SubnetName {
				if _, ok := subnet.Reservations[instance.Name]; ok {
					instance.Status.IPAddresses[network.Name] = subnet.Reservations[instance.Name]
					instance.Status.Allocated = true
					break
				}
				_, cidr, err := net.ParseCIDR(subnet.Cidr)
				if err != nil {
					return result, err
				}
				aid.IPNet = *cidr

				aid.Reservelist = maps.Values(subnet.Reservations)
				aid.RangeStart = net.ParseIP(subnet.AllocationRange.AllocationStart)
				aid.RangeEnd = net.ParseIP(subnet.AllocationRange.AllocationEnd)
				ipReservation, updatedReservations, err := ipam.AssignIP(aid, net.ParseIP(network.FixedIP))
				if err != nil {
					return result, err
				}
				ipReservation.Gateway = subnet.Gateway
				ipReservation.Routes = subnet.Routes

				aid.Reservelist = updatedReservations

				if osnet.Spec.Subnets[i].Reservations == nil {
					osnet.Spec.Subnets[i].Reservations = make(map[string]networkv1beta1.IPReservation)
				}
				osnet.Spec.Subnets[i].Reservations[instance.Name] = ipReservation
				controllerutil.AddFinalizer(osnet, fmt.Sprintf("%s-%s", h.GetFinalizer(), instance.Name))
				err = osnetHelper.PatchInstance(ctx, osnet)
				if err != nil {

					r.Log.Error(err, "Can't patch osnet instance")
					return result, err
				}

				instance.Status.IPAddresses[network.Name] = ipReservation
				instance.Status.Allocated = true
				break
			}
		}
	}
	r.Log.Info("Reconciled OpenStackIPSet successfully")
	return result, nil
}

// ReconcileDelete reconciles when resource is deleted
func (r *OpenStackIPSetReconciler) ReconcileDelete(
	ctx context.Context,
	instance *networkv1beta1.OpenStackIPSet,
	h *helper.Helper,
	req ctrl.Request) (ctrl.Result, error) {
	r.Log.Info("Reconciling IPDataSet delete")
	networks := instance.Spec.Networks

	for _, network := range networks {
		osnet := &networkv1beta1.OpenStackNet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      network.Name,
				Namespace: instance.Namespace,
			},
		}
		err := r.Get(ctx, types.NamespacedName{Name: network.Name, Namespace: instance.Namespace}, osnet)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				// Request object not found, could have been deleted after reconcile request.
				// Owned objects are automatically garbage collected.
				// For additional cleanup logic use finalizers. Return and don't requeue.
				continue
			}
			return ctrl.Result{}, err
		}
		osnetHelper, err := helper.NewHelper(
			osnet,
			r.Client,
			r.Kclient,
			r.Scheme,
			r.Log)
		if err != nil {
			r.Log.Error(err, fmt.Sprintf("Unable to acquire helper for OpenStackNet %s", osnet.Name))
			return ctrl.Result{}, err
		}

		subIndex := -1
		for i, subnet := range osnet.Status.Subnets {
			if subnet.Name == network.SubnetName {
				subIndex = i
				break
			}
		}

		if subIndex >= 0 {
			delete(osnet.Spec.Subnets[subIndex].Reservations, instance.Name)
		}

		controllerutil.RemoveFinalizer(osnet, fmt.Sprintf("%s-%s", h.GetFinalizer(), instance.Name))
		err = osnetHelper.PatchInstance(ctx, osnet)
		if err != nil {
			return ctrl.Result{}, err
		}

	}

	controllerutil.RemoveFinalizer(instance, h.GetFinalizer())
	r.Log.Info("Reconciled OpenStackIPSet delete successfully")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OpenStackIPSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1beta1.OpenStackIPSet{}).
		Complete(r)
}
