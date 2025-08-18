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

// Package memcached implements the Memcached controller for managing Memcached instances
package memcached

import (
	"context"
	"fmt"
	"time"

	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	configmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	commonservice "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	commonstatefulset "github.com/openstack-k8s-operators/lib-common/modules/common/statefulset"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"

	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	memcached "github.com/openstack-k8s-operators/infra-operator/pkg/memcached"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// fields to index to reconcile on CR change
const (
	serviceSecretNameField = ".spec.tls.genericService.SecretName"
	caSecretNameField      = ".spec.tls.ca.caBundleSecretName"
	topologyField          = ".spec.topologyRef.Name"
)

var allWatchFields = []string{
	serviceSecretNameField,
	caSecretNameField,
	topologyField,
}

// Reconciler reconciles a Memcached object
type Reconciler struct {
	client.Client
	Kclient kubernetes.Interface
	config  *rest.Config
	Scheme  *runtime.Scheme
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *Reconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("memcached")
}

// RBAC for memcached resources
// +kubebuilder:rbac:groups=memcached.openstack.org,resources=memcacheds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=memcached.openstack.org,resources=memcacheds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=memcached.openstack.org,resources=memcacheds/finalizers,verbs=update;patch

// RBAC for statefulsets and their pods
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;

// RBAC for services
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete;

// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=topology.openstack.org,resources=topologies,verbs=get;list;watch;update

// Reconcile - Memcached
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the Memcached instance
	instance := &memcachedv1.Memcached{}
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

	// initialize conditions used later as Status=Unknown
	cl := condition.CreateList(
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		// TLS cert secrets
		condition.UnknownCondition(condition.TLSInputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		// endpoint for adoption redirect
		condition.UnknownCondition(condition.CreateServiceReadyCondition, condition.InitReason, condition.CreateServiceReadyInitMessage),
		// configmap generation
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
		// memcache pods ready
		condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		// service account, role, rolebinding conditions
		condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
		condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
		condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	if instance.Spec.TLS.MTLS.SslVerifyMode == "Request" || instance.Spec.TLS.MTLS.SslVerifyMode == "Require" {
		// MTLS cert secrets
		cl = append(cl, *condition.UnknownCondition(memcachedv1.MTLSInputReadyCondition, condition.InitReason, condition.InputReadyInitMessage))
	} else {
		instance.Status.MTLSCert = ""
	}

	if instance.Status.ServerList == nil {
		instance.Status.ServerList = []string{}
	}
	if instance.Status.ServerListWithInet == nil {
		instance.Status.ServerListWithInet = []string{}
	}

	// Init Topology condition if there's a reference
	if instance.Spec.TopologyRef != nil {
		c := condition.UnknownCondition(condition.TopologyReadyCondition, condition.InitReason, condition.TopologyReadyInitMessage)
		cl.Set(c)
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	//
	// Create/Update all the resources associated to this Memcached instance
	//

	// Service account, role, binding
	rbacRules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{"security.openshift.io"},
			ResourceNames: []string{"anyuid"},
			Resources:     []string{"securitycontextconstraints"},
			Verbs:         []string{"use"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"create", "get", "list", "watch", "update", "patch", "delete"},
		},
	}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	// Hash of all resources that may cause a service restart
	inputHashEnv := make(map[string]env.Setter)

	//
	// TLS input validation
	//
	// Validate the CA cert secret if provided
	if instance.Spec.TLS.CaBundleSecretName != "" {
		hash, err := tls.ValidateCACertSecret(
			ctx,
			helper.GetClient(),
			types.NamespacedName{
				Name:      instance.Spec.TLS.CaBundleSecretName,
				Namespace: instance.Namespace,
			},
		)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.TLSInputReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.TLSInputReadyWaitingMessage, instance.Spec.TLS.CaBundleSecretName))
				return ctrl.Result{}, nil
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.TLSInputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.TLSInputErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}

		if hash != "" {
			inputHashEnv["CA"] = env.SetValue(hash)
		}
	}

	// Validate service cert secret
	if instance.Spec.TLS.Enabled() {
		hash, err := instance.Spec.TLS.ValidateCertSecret(ctx, helper, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.TLSInputReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.TLSInputReadyWaitingMessage, err.Error()))
				return ctrl.Result{}, nil
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.TLSInputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.TLSInputErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		inputHashEnv["Cert"] = env.SetValue(hash)
	}

	// Validate client cert secret
	if instance.Spec.TLS.MTLS.SslVerifyMode == "Request" || instance.Spec.TLS.MTLS.SslVerifyMode == "Require" {
		if instance.Spec.TLS.MTLS.AuthCertSecret.SecretName != nil {
			hash, err := instance.Spec.TLS.MTLS.AuthCertSecret.ValidateCertSecret(ctx, helper, instance.Namespace)
			if err != nil {
				if k8s_errors.IsNotFound(err) {
					instance.Status.Conditions.Set(condition.FalseCondition(
						memcachedv1.MTLSInputReadyCondition,
						condition.RequestedReason,
						condition.SeverityInfo,
						condition.TLSInputReadyWaitingMessage, err.Error()))
					return ctrl.Result{}, nil
				}
				instance.Status.Conditions.Set(condition.FalseCondition(
					memcachedv1.MTLSInputReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.TLSInputErrorMessage,
					err.Error()))
				return ctrl.Result{}, err
			}
			inputHashEnv["ClientCert"] = env.SetValue(hash)
			instance.Status.MTLSCert = *instance.Spec.TLS.MTLS.AuthCertSecret.SecretName
			instance.Status.Conditions.MarkTrue(memcachedv1.MTLSInputReadyCondition, condition.InputReadyMessage)
		} else {
			instance.Status.Conditions.Set(condition.FalseCondition(
				memcachedv1.MTLSInputReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				"%s", fmt.Sprintf(memcachedv1.MTLSInputReadyWaitingMessage)))
			return ctrl.Result{}, nil
		}
	}

	// all cert input checks out so report InputReady
	instance.Status.Conditions.MarkTrue(condition.TLSInputReadyCondition, condition.InputReadyMessage)

	// Memcached config maps
	err = r.generateConfigMaps(ctx, helper, instance, &inputHashEnv)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error calculating configmap hash: %w", err)
	}
	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	//
	// create hash over all the different input resources to identify if any those changed
	// and a restart/recreate is required.
	//
	hashOfHashes, err := util.HashOfInputHashes(inputHashEnv)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hashMap, changed := util.SetHash(instance.Status.Hash, common.InputHashName, hashOfHashes); changed {
		// Hash changed and instance status should be updated (which will be done by main defer func),
		// so update all the input hashes and return to reconcile again
		instance.Status.Hash = hashMap
		for k, s := range inputHashEnv {
			var envVar corev1.EnvVar
			s(&envVar)
			instance.Status.Hash[k] = envVar.Value
		}
		Log.Info(fmt.Sprintf("Input hash changed %s", hashOfHashes), "instance", instance)
		return ctrl.Result{}, nil
	}

	// Service to expose Memcached pods
	commonsvc, err := commonservice.NewService(memcached.HeadlessService(instance), time.Duration(5)*time.Second, nil)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.CreateServiceReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	sres, serr := commonsvc.CreateOrPatch(ctx, helper)
	if serr != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.CreateServiceReadyErrorMessage,
			serr.Error()))
		return sres, serr
	}

	// TODO: We have to make sure this works properly in dual stack env (if we support it)
	ipFamily := commonsvc.GetIPFamilies()[0]
	serverList, serverListWithInet := r.GetServerLists(instance, ipFamily)
	instance.Status.ServerList = serverList
	instance.Status.ServerListWithInet = serverListWithInet

	instance.Status.Conditions.MarkTrue(condition.CreateServiceReadyCondition, condition.CreateServiceReadyMessage)
	serviceLabels := map[string]string{
		"app":                instance.Name,
		common.AppSelector:   instance.Name,
		"cr":                 instance.Name,
		common.OwnerSelector: "infra-operator",
	}

	//
	// Handle Topology
	//
	topology, err := topologyv1.EnsureServiceTopology(
		ctx,
		helper,
		instance.Spec.TopologyRef,
		instance.Status.LastAppliedTopology,
		instance.Name,
		labels.GetLabelSelector(serviceLabels),
	)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.TopologyReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.TopologyReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("waiting for Topology requirements: %w", err)
	}

	// If TopologyRef is present and EnsureServiceTopology returned a valid
	// topology object, set .Status.LastAppliedTopology to the referenced one
	// and mark the condition as true
	if instance.Spec.TopologyRef != nil {
		// update the Status with the last retrieved Topology name
		instance.Status.LastAppliedTopology = instance.Spec.TopologyRef
		// update the TopologyRef associated condition
		instance.Status.Conditions.MarkTrue(condition.TopologyReadyCondition, condition.TopologyReadyMessage)
	} else {
		// remove LastAppliedTopology from the .Status
		instance.Status.LastAppliedTopology = nil
	}
	// Statefulset for stable names
	ss := commonstatefulset.NewStatefulSet(
		memcached.StatefulSet(instance, hashOfHashes, topology), time.Duration(5)*time.Second)
	sfres, sferr := ss.CreateOrPatch(ctx, helper)
	if sferr != nil {
		return sfres, sferr
	}

	//
	// Reconstruct the state of the memcached resource based on the statefulset and its pods
	//
	deploy := ss.GetStatefulSet()
	if deploy.Generation == deploy.Status.ObservedGeneration {
		instance.Status.ReadyCount = deploy.Status.ReadyReplicas
	}
	if commonstatefulset.IsReady(deploy) {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		// It is OK to return success as we are watching for StatefulSet changes
		return ctrl.Result{}, nil
	}

	// We reached the end of the Reconcile, update the Ready condition based on
	// the sub conditions
	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	return ctrl.Result{}, nil
}

// generateConfigMaps returns the config map resource for a memcached instance
func (r *Reconciler) generateConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *memcachedv1.Memcached,
	envVars *map[string]env.Setter,
) error {
	Log := h.GetLogger()

	customData := make(map[string]string)
	var memcachedTLSListen, memcachedTLSOptions, memcachedPort string
	if instance.Spec.TLS.Enabled() {
		memcachedTLSListen = "| sed 's/\\(.*\\)/\\1\\nnotls:\\1:11211/'"
		memcachedTLSOptions = "-Z " +
			"-o ssl_chain_cert=/etc/pki/tls/certs/memcached.crt " +
			"-o ssl_key=/etc/pki/tls/private/memcached.key " +
			"-o ssl_ca_cert=/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"

		switch instance.Spec.TLS.MTLS.SslVerifyMode {
		case "Request":
			memcachedTLSOptions = memcachedTLSOptions + " -o ssl_verify_mode=1"
		case "Require":
			memcachedTLSOptions = memcachedTLSOptions + " -o ssl_verify_mode=2"
		}

		memcachedPort = fmt.Sprint(memcached.MemcachedTLSPort)
		instance.Status.TLSSupport = true
	} else {
		memcachedTLSListen = ""
		memcachedTLSOptions = ""
		memcachedPort = fmt.Sprint(memcached.MemcachedPort)
		instance.Status.TLSSupport = false
	}
	templateParameters := map[string]interface{}{
		"memcachedTLSListen":  memcachedTLSListen,
		"memcachedTLSOptions": memcachedTLSOptions,
		"memcachedPort":       memcachedPort,
		"memcachedCacheSize":  instance.Spec.CacheSize,
		"memcachedMaxConn":    instance.Spec.MaxConn,
	}

	cms := []util.Template{
		// ConfigMap
		{
			Name:          fmt.Sprintf("%s-config-data", instance.Name),
			Namespace:     instance.Namespace,
			Type:          util.TemplateTypeConfig,
			InstanceType:  instance.Kind,
			CustomData:    customData,
			ConfigOptions: templateParameters,
			Labels:        map[string]string{},
		},
	}

	err := configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars)
	if err != nil {
		Log.Error(err, "Unable to retrieve or create config maps")
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.config = mgr.GetConfig()

	// Various CR fields need to be indexed to filter watch events
	// for the secret changes we want to be notified of
	// index caBundleSecretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &memcachedv1.Memcached{}, caSecretNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*memcachedv1.Memcached)
		tls := &cr.Spec.TLS
		if tls.CaBundleSecretName != "" {
			return []string{tls.CaBundleSecretName}
		}
		return nil
	}); err != nil {
		return err
	}
	// index secretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &memcachedv1.Memcached{}, serviceSecretNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*memcachedv1.Memcached)
		tls := &cr.Spec.TLS
		if tls.Enabled() {
			return []string{*tls.SecretName}
		}
		return nil
	}); err != nil {
		return err
	}

	// index topologyField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &memcachedv1.Memcached{}, topologyField, func(rawObj client.Object) []string {
		// Extract the topology name from the spec, if one is provided
		cr := rawObj.(*memcachedv1.Memcached)
		if cr.Spec.TopologyRef == nil {
			return nil
		}
		return []string{cr.Spec.TopologyRef.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&memcachedv1.Memcached{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&topologyv1.Topology{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

// findObjectsForSrc - returns a reconcile request if the object is referenced by a Memcached CR
func (r *Reconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	Log := r.GetLogger(ctx)

	for _, field := range allWatchFields {
		crList := &memcachedv1.MemcachedList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(field, src.GetName()),
			Namespace:     src.GetNamespace(),
		}
		err := r.List(context.TODO(), crList, listOps)
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

func (r *Reconciler) reconcileDelete(ctx context.Context, instance *memcachedv1.Memcached, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Remove finalizer on the Topology CR
	if ctrlResult, err := topologyv1.EnsureDeletedTopologyRef(
		ctx,
		helper,
		instance.Status.LastAppliedTopology,
		instance.Name,
	); err != nil {
		return ctrlResult, err
	}

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

// GetServerLists returns list of memcached server without/with inet prefix
func (r *Reconciler) GetServerLists(
	instance *memcachedv1.Memcached,
	ipFamily corev1.IPFamily,
) ([]string, []string) {
	var serverList []string
	var serverListWithInet []string

	prefix := "inet"
	if ipFamily == corev1.IPv6Protocol {
		prefix = "inet6"
	}
	var port int32
	if instance.Spec.TLS.Enabled() {
		port = memcached.MemcachedTLSPort
	} else {
		port = memcached.MemcachedPort
	}
	for i := int32(0); i < *(instance.Spec.Replicas); i++ {
		server := fmt.Sprintf("%s-%d.%s.%s.svc", instance.Name, i, instance.Name, instance.Namespace)
		serverList = append(serverList, fmt.Sprintf("%s:%d", server, port))

		// python-memcached requires inet(6) prefix according to the IP version
		// used by the memcached server.
		serverListWithInet = append(serverListWithInet, fmt.Sprintf("%s:[%s]:%d", prefix, server, memcached.MemcachedPort))
	}

	return serverList, serverListWithInet
}
