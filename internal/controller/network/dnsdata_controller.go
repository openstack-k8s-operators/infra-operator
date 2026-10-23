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
	"errors"
	"fmt"
	"sort"
	"strings"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	configmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"

	dnsmasq "github.com/openstack-k8s-operators/infra-operator/internal/dnsmasq"
	corev1 "k8s.io/api/core/v1"
)

// ErrInvalidDNSData indicates invalid DNS names or CNAME configuration in the
// DNSData spec. These are permanent errors that need a spec change to fix.
var ErrInvalidDNSData = errors.New("invalid DNSData spec")

func validateDNSName(fieldPath, name string) error {
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return fmt.Errorf("%w: %s %q: %s", ErrInvalidDNSData, fieldPath, name, strings.Join(problems, ", "))
	}
	return nil
}

// DNSDataReconciler reconciles a DNSData object
type DNSDataReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *DNSDataReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("DNSData")
}

// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdata,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdata/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=dnsdata/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete;

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *DNSDataReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the DNSData instance
	instance := &networkv1.DNSData{}
	err := r.Get(ctx, req.NamespacedName, instance)
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
		// Don't update the status, if Reconciler Panics
		if r := recover(); r != nil {
			Log.Info(fmt.Sprintf("Panic during reconcile %v\n", r))
			panic(r)
		}
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
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	// initialize status
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, err
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DNSDataReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkv1.DNSData{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *DNSDataReconciler) reconcileDelete(ctx context.Context, instance *networkv1.DNSData, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service delete")

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

func (r *DNSDataReconciler) reconcileNormal(ctx context.Context, instance *networkv1.DNSData, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service")

	configMapVars := make(map[string]env.Setter)

	//
	// create Configmap with hosts file
	//
	err := r.generateServiceConfigMaps(ctx, helper, instance, &configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		if errors.Is(err, ErrInvalidDNSData) {
			// Permanent error from an invalid CR spec: requeuing via a
			// returned error (exponential backoff) won't help until the
			// spec changes, which already triggers its own reconcile.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	hash, err := util.ObjectHash(env.MergeEnvs([]corev1.EnvVar{}, configMapVars))
	if err != nil {
		return ctrl.Result{}, err
	}
	if hash != instance.Status.Hash {
		instance.Status.Hash = hash
		Log.Info("Input maps hash", "HashName", common.InputHashName, "Hash", hash)
	}

	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.InputReadyMessage)

	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// generateServiceConfigMaps - create configmap hosts file
func (r *DNSDataReconciler) generateServiceConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *networkv1.DNSData,
	envVars *map[string]env.Setter,
) error {
	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(dnsmasq.ServiceName), map[string]string{networkv1.DNSDataLabelSelectorKey: strings.ToLower(instance.Spec.DNSDataLabelSelectorValue)})

	configMapData := map[string]string{}

	var configData string
	var cnameData string
	var validationErr error
	cnameTargets := map[string]string{}
	invalidCNAMEs := map[string]struct{}{}
	recordValidationError := func(err error) {
		if validationErr == nil {
			validationErr = err
		}
	}
	for hostIndex, host := range instance.Spec.Hosts {
		hostnames := make([]string, 0, len(host.Hostnames))
		for hostnameIndex, hostname := range host.Hostnames {
			fieldPath := fmt.Sprintf("spec.hosts[%d].hostnames[%d]", hostIndex, hostnameIndex)
			if err := validateDNSName(fieldPath, hostname); err != nil {
				recordValidationError(err)
				continue
			}
			hostnames = append(hostnames, hostname)
		}
		if len(host.Hostnames) > 0 && len(hostnames) == 0 {
			continue
		}

		sort.Strings(hostnames)
		configData += host.IP + " " + strings.Join(hostnames, " ") + "\n"

		if len(host.CNAMEs) > 0 {
			if len(host.Hostnames) != 1 {
				// Ambiguous which hostname is canonical: surface the error via
				// the ServiceConfigReadyCondition below but keep generating
				// config for the other, valid hosts instead of aborting the
				// whole ConfigMap.
				recordValidationError(fmt.Errorf(
					"%w: spec.hosts[%d].cnames requires exactly one hostname as its canonical name", ErrInvalidDNSData, hostIndex))
				continue
			}
			if len(hostnames) != 1 {
				continue
			}
			for cnameIndex, cname := range host.CNAMEs {
				fieldPath := fmt.Sprintf("spec.hosts[%d].cnames[%d]", hostIndex, cnameIndex)
				if err := validateDNSName(fieldPath, cname); err != nil {
					recordValidationError(err)
					continue
				}
				canonical := hostnames[0]
				if cname == canonical {
					delete(cnameTargets, cname)
					invalidCNAMEs[cname] = struct{}{}
					recordValidationError(fmt.Errorf(
						"%w: %s alias %q cannot target itself", ErrInvalidDNSData, fieldPath, cname))
					continue
				}
				if _, invalid := invalidCNAMEs[cname]; invalid {
					continue
				}
				if existingCanonical, exists := cnameTargets[cname]; exists {
					if existingCanonical != canonical {
						delete(cnameTargets, cname)
						invalidCNAMEs[cname] = struct{}{}
						recordValidationError(fmt.Errorf(
							"%w: CNAME alias %q has conflicting canonical names %q and %q",
							ErrInvalidDNSData, cname, existingCanonical, canonical))
					}
					continue
				}
				cnameTargets[cname] = canonical
			}
		}
	}

	cnameNames := make([]string, 0, len(cnameTargets))
	for cname := range cnameTargets {
		cnameNames = append(cnameNames, cname)
	}
	sort.Strings(cnameNames)
	for _, cname := range cnameNames {
		cnameData += fmt.Sprintf("cname=%s,%s\n", cname, cnameTargets[cname])
	}

	configMapData[instance.Name] = configData
	if cnameData != "" {
		configMapData[instance.Name+"-cnames"] = cnameData
	}

	cms := []util.Template{
		{
			Name:         strings.ToLower(instance.Name),
			Namespace:    instance.Namespace,
			Type:         util.TemplateTypeNone,
			InstanceType: instance.Kind,
			CustomData:   configMapData,
			Labels:       cmLabels,
		},
	}

	if err := configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars); err != nil {
		return err
	}

	return validationErr
}
