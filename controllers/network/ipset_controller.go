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
	"net/netip"
	"sort"
	"strings"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8snet "k8s.io/utils/net"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	ipam "github.com/openstack-k8s-operators/infra-operator/pkg/ipam"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

const CtlPlaneNetwork = "ctlplane"

// IPSetReconciler reconciles a IPSet object
type IPSetReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *IPSetReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("IPSet")
}

//+kubebuilder:rbac:groups=network.openstack.org,resources=ipsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.openstack.org,resources=ipsets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=network.openstack.org,resources=ipsets/finalizers,verbs=update;patch
//+kubebuilder:rbac:groups=network.openstack.org,resources=netconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=network.openstack.org,resources=reservations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=network.openstack.org,resources=reservations/finalizers,verbs=update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *IPSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)
	// Fetch the IPSet instance
	instance := &networkv1.IPSet{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
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

	// initialize status
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		condition.UnknownCondition(networkv1.ReservationReadyCondition, condition.InitReason, networkv1.ReservationInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, err
	}

	instance.Status.Reservation = []networkv1.IPSetReservation{}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted ipsets
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *IPSetReconciler) SetupWithManager(_ context.Context, mgr ctrl.Manager) error {
	ipsetFN := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		Log := r.GetLogger(ctx)
		result := []reconcile.Request{}

		// For each NetConfig update event get the list of all
		// IPSet to trigger reconcile for the one in the same namespace
		ipsets := &networkv1.IPSetList{}

		listOpts := []client.ListOption{
			client.InNamespace(o.GetNamespace()),
		}
		if err := r.Client.List(ctx, ipsets, listOpts...); err != nil {
			Log.Error(err, "Unable to retrieve IPSetList")
			return nil
		}

		// For each ipsets instance create a reconcile request
		for _, i := range ipsets.Items {
			name := client.ObjectKey{
				Namespace: o.GetNamespace(),
				Name:      i.Name,
			}
			result = append(result, reconcile.Request{NamespacedName: name})
		}
		if len(result) > 0 {
			Log.Info("Reconcile request for:", "result", result)

			return result
		}
		return nil
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.IPSet{}).
		Owns(&networkv1.Reservation{}).
		Watches(&networkv1.NetConfig{}, ipsetFN).
		Complete(r)
}

func (r *IPSetReconciler) reconcileDelete(ctx context.Context, instance *networkv1.IPSet, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Remove finalizer from reservation
	res, err := r.getReservation(ctx, instance)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if !k8s_errors.IsNotFound(err) && controllerutil.RemoveFinalizer(res, helper.GetFinalizer()) {
		if err := helper.GetClient().Update(ctx, res); err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

func (r *IPSetReconciler) reconcileNormal(ctx context.Context, instance *networkv1.IPSet, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service")

	opts := &client.ListOptions{
		Namespace: instance.Namespace,
	}

	// check if NetConfig is available
	netcfgs := &networkv1.NetConfigList{}
	err := r.List(ctx, netcfgs, opts)
	if err != nil {
		instance.Status.Conditions.MarkFalse(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			networkv1.NetConfigErrorMessage,
			err.Error())
		return ctrl.Result{}, err
	}

	if len(netcfgs.Items) > 0 {
		netcfg := &netcfgs.Items[0]

		// get list of Reservation objects in the namespace
		reservations := &networkv1.ReservationList{}
		err = r.List(ctx, reservations, opts)
		if err != nil {
			instance.Status.Conditions.MarkFalse(
				condition.InputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				networkv1.ReservationListErrorMessage,
				err.Error())

			return ctrl.Result{}, err
		}

		instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, condition.InputReadyMessage)

		// TODO: add validation, we expect only one netcfg in a namespace
		ipSetRes, err := r.ensureReservation(ctx, instance, netcfg, helper, reservations)
		if err != nil {
			instance.Status.Conditions.MarkFalse(
				networkv1.ReservationReadyCondition,
				condition.ErrorReason,
				condition.SeverityError,
				networkv1.ReservationErrorMessage,
				err.Error())

			return ctrl.Result{}, err
		}
		if len(ipSetRes.Spec.Reservation) != len(instance.Spec.Networks) {
			instance.Status.Conditions.MarkFalse(
				networkv1.ReservationReadyCondition,
				condition.ErrorReason,
				condition.SeverityError,
				networkv1.ReservationMisMatchErrorMessage,
				len(instance.Status.Reservation),
				len(instance.Spec.Networks))

			return ctrl.Result{}, err
		}

		// sort instance.Status.Reservations
		sort.Slice(instance.Status.Reservation, func(i, j int) bool {
			return (strings.EqualFold(string(instance.Status.Reservation[i].ServiceNetwork),
				CtlPlaneNetwork) && !strings.EqualFold(string(instance.Status.Reservation[j].ServiceNetwork),
				CtlPlaneNetwork))
		})

		instance.Status.Conditions.MarkTrue(networkv1.ReservationReadyCondition, networkv1.ReservationReadyMessage)

		Log.Info("IPSet is ready:", "instance", instance.Name, "ipSetRes", ipSetRes.Spec.Reservation)
	} else {
		instance.Status.Conditions.MarkFalse(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityError,
			networkv1.NetConfigMissingMessage,
			instance.Namespace)
		return ctrl.Result{}, err
	}

	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

func (r *IPSetReconciler) getReservation(ctx context.Context, instance *networkv1.IPSet) (*networkv1.Reservation, error) {
	// get reservation
	res := &networkv1.Reservation{}
	resName := types.NamespacedName{
		Name:      instance.GetName(),
		Namespace: instance.GetNamespace(),
	}

	err := r.Get(ctx, resName, res)
	if err != nil {
		// Error reading the object - requeue the request.
		return res, err
	}

	return res, nil
}

func (r *IPSetReconciler) patchReservation(
	ctx context.Context,
	helper *helper.Helper,
	name types.NamespacedName,
	labels map[string]string,
	spec networkv1.ReservationSpec,
) (*networkv1.Reservation, error) {
	Log := r.GetLogger(ctx)
	res := &networkv1.Reservation{
		ObjectMeta: v1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
		},
	}

	// create or update the Reservation
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, res, func() error {
		res.Labels = util.MergeStringMaps(res.Labels, labels)
		res.Spec = spec

		controllerutil.AddFinalizer(res, helper.GetFinalizer())

		// Set controller reference to the IPSet object
		err := controllerutil.SetControllerReference(helper.GetBeforeObject(), res, r.Scheme)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error create/updating Reservation: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("reservation %s operation %s", res.Name, string(op)))
	}

	return res, nil
}

func (r *IPSetReconciler) ensureReservation(
	ctx context.Context,
	ipset *networkv1.IPSet,
	netcfg *networkv1.NetConfig,
	helper *helper.Helper,
	reservations *networkv1.ReservationList,
) (reservation *networkv1.Reservation, _err error) {
	reservationName := types.NamespacedName{
		Namespace: ipset.Namespace,
		Name:      ipset.Name,
	}
	// Get existing reservation to preserve IP assignments
	reservation, err := r.getReservation(ctx, ipset)
	var reservationSpec = reservation.Spec

	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			return nil, err
		}
		reservationSpec.Reservation = map[string]networkv1.IPAddress{}
	}
	reservationLabels := map[string]string{}

	// always patch the Reservation
	defer func() {
		reservation, _err = r.patchReservation(
			ctx,
			helper,
			reservationName,
			reservationLabels,
			reservationSpec,
		)
		if _err != nil {
			_err = fmt.Errorf("failed to patch reservation %w", _err)
			return
		}
	}()

	// create IPs per requested Network and Subnet
	for _, ipsetNet := range ipset.Spec.Networks {
		netDef, subnetDef, err := netcfg.GetNetAndSubnet(ipsetNet.Name, ipsetNet.SubnetName)
		if err != nil {
			return nil, err
		}

		if netDef.ServiceNetwork == "" {
			netDef.ServiceNetwork = networkv1.ToDefaultServiceNetwork(netDef.Name)
		}

		// set net: subnet label
		reservationLabels = util.MergeStringMaps(reservationLabels,
			map[string]string{
				fmt.Sprintf("%s/%s", ipam.IPAMLabelKey, string(netDef.Name)): string(subnetDef.Name),
			})

		var ip *networkv1.IPAddress

		// Check if we already have an IP for this network
		if existingIP, exists := reservationSpec.Reservation[string(netDef.Name)]; exists {
			// Use existing IP assignment
			ip = &existingIP
		} else {
			// Need to assign a new IP
			ipDetails := ipam.AssignIPDetails{
				IPSet:       ipset.Name,
				NetName:     string(netDef.Name),
				SubNet:      subnetDef,
				Reservelist: reservations,
			}

			if ipsetNet.FixedIP != nil {
				ipDetails.FixedIP, err = netip.ParseAddr(string(*ipsetNet.FixedIP))
				if err != nil {
					return nil, fmt.Errorf("failed parse FixedIP %s", string(*ipsetNet.FixedIP))
				}
				if !ipDetails.FixedIP.IsValid() {
					return nil, fmt.Errorf("failed parse FixedIP %s", string(*ipsetNet.FixedIP))
				}
			}

			ip, err = ipDetails.AssignIP()
			if err != nil {
				return nil, fmt.Errorf("failed to do ip reservation: %w", err)
			}

			// Add the new IP to the reservation
			reservationSpec.Reservation[string(netDef.Name)] = *ip
		}
		ipsetRes := networkv1.IPSetReservation{
			Network:        netDef.Name,
			Subnet:         subnetDef.Name,
			Address:        ip.Address,
			MTU:            netDef.MTU,
			Cidr:           subnetDef.Cidr,
			Vlan:           subnetDef.Vlan,
			Gateway:        subnetDef.Gateway,
			Routes:         subnetDef.Routes,
			DNSDomain:      netDef.DNSDomain,
			ServiceNetwork: netDef.ServiceNetwork,
		}
		if ipsetNet.DefaultRoute != nil && *ipsetNet.DefaultRoute && subnetDef.Gateway != nil {
			ipsetRes.Gateway = subnetDef.Gateway
			if ipsetRes.Routes == nil {
				ipsetRes.Routes = []networkv1.Route{}
			}
			if k8snet.IsIPv6(net.ParseIP(ip.Address)) {
				ipsetRes.Routes = append(ipsetRes.Routes,
					networkv1.Route{Destination: "::/0", Nexthop: *subnetDef.Gateway})
			} else {
				ipsetRes.Routes = append(ipsetRes.Routes,
					networkv1.Route{Destination: "0.0.0.0/0", Nexthop: *subnetDef.Gateway})
			}
		}
		if subnetDef.DNSDomain != nil {
			ipsetRes.DNSDomain = *subnetDef.DNSDomain
		}
		ipset.Status.Reservation = append(ipset.Status.Reservation, ipsetRes)
	}

	return reservation, nil
}
