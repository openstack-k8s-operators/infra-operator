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
	"sort"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	"golang.org/x/exp/maps"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ServiceReconciler reconciles a Service object
type ServiceReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *ServiceReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("DNSData")
}

// +kubebuilder:rbac:groups=network.openstack.org,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.openstack.org,resources=services/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=services/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsmasqs,verbs=get;list;watch;
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdatas,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Service object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Get a list of DNSMasq CRs which are in the same namespace the Service
	// is in to add the Service to those DNSMasqs.
	dnsmasqs := &networkv1.DNSMasqList{}

	listOpts := []client.ListOption{
		client.InNamespace(req.Namespace),
	}
	if err := r.Client.List(ctx, dnsmasqs, listOpts...); err != nil {
		return ctrl.Result{}, fmt.Errorf("Unable to retrieve DNSMasqList %w", err)
	}

	dnsHosts, err := r.getServiceDNSData(ctx, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, dnsmasq := range dnsmasqs.Items {
		// sort entries for DNSData spec to reduce not required updates
		sortedDNSHosts := []networkv1.DNSHost{}

		keys := maps.Keys(dnsHosts)
		sort.Strings(keys)

		for _, key := range keys {
			sortedDNSHosts = append(sortedDNSHosts, dnsHosts[key])
		}

		err = r.createOrPatchDNSData(ctx, &dnsmasq, sortedDNSHosts)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Complete(r)
}

// getServiceDNSData -
func (r *ServiceReconciler) getServiceDNSData(
	ctx context.Context,
	namespace string,
) (map[string]networkv1.DNSHost, error) {
	svcDNSHosts := map[string]networkv1.DNSHost{}

	// get all services from the namespace triggered the reconcile
	svcList := &corev1.ServiceList{}

	if err := r.List(ctx, svcList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("error getting list of services %w", err)
	}

	for _, svc := range svcList.Items {
		if svc.Annotations != nil {
			// if the service has our networkv1.AnnotationHostnameKey get
			// the ips from its status if it is a LoadBalancer type
			if hostname, ok := svc.Annotations[networkv1.AnnotationHostnameKey]; ok && svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
				if len(svc.Status.LoadBalancer.Ingress) > 0 {
					for _, ingr := range svc.Status.LoadBalancer.Ingress {
						addr := net.ParseIP(ingr.IP)
						if addr == nil {
							return nil, fmt.Errorf(fmt.Sprintf("unrecognized address %s", ingr.IP))
						}

						if host, ok := svcDNSHosts[addr.String()]; !ok {
							svcDNSHosts[addr.String()] = networkv1.DNSHost{
								IP:        addr.String(),
								Hostnames: []string{hostname},
							}
						} else {
							host.Hostnames = append(host.Hostnames, hostname)
							sort.Strings(host.Hostnames)

							svcDNSHosts[addr.String()] = host
						}
					}
				}
			}
		}
	}

	return svcDNSHosts, nil
}

// createOrPatchDNSData -
func (r *ServiceReconciler) createOrPatchDNSData(
	ctx context.Context,
	dnsmasq *networkv1.DNSMasq,
	svcDNSHosts []networkv1.DNSHost,
) error {
	Log := r.GetLogger(ctx)

	svcDNSData := &networkv1.DNSData{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dnsmasq.GetName() + "-svc",
			Namespace: dnsmasq.GetNamespace(),
		},
	}

	// create or update the DNSData
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, svcDNSData, func() error {

		svcDNSData.Spec.DNSDataLabelSelectorValue = dnsmasq.Spec.DNSDataLabelSelectorValue
		svcDNSData.Spec.Hosts = svcDNSHosts

		err := controllerutil.SetControllerReference(dnsmasq, svcDNSData, r.Scheme)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("error create/updating service DNSData: %w", err)
	}

	if op != controllerutil.OperationResultNone {
		Log.Info("operation:", "svcDNSData name", svcDNSData.Name, "Operation", string(op))
	}

	return nil
}
