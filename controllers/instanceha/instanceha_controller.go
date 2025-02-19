/*
Copyright 2024.

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

package instanceha

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"

	"github.com/openstack-k8s-operators/lib-common/modules/common"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"

	commondeployment "github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	instanceha "github.com/openstack-k8s-operators/infra-operator/pkg/instanceha"
)

// InstanceHaReconciler reconciles a InstanceHa object
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Kclient kubernetes.Interface
}

// GetLog returns a logger object with a prefix of "conroller.name" and aditional controller context fields
func (r *Reconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("InstanceHa")
}

// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=topology.openstack.org,resources=topologies,verbs=get;list;watch;update

// Reconcile -
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {

	Log := r.GetLogger(ctx)

	instance := &instancehav1.InstanceHa{}
	err := r.Client.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			Log.Info("InstanceHa CR not found")
			return ctrl.Result{}, nil
		}
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

	//
	// initialize status
	//
	isNewInstance := instance.Status.Conditions == nil
	if isNewInstance {
		instance.Status.Conditions = condition.Conditions{}
	}

	// Save a copy of the condtions so that we can restore the LastTransitionTime
	// when a condition's state doesn't change.
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)
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

	cl := condition.CreateList(
		condition.UnknownCondition(instancehav1.InstanceHaReadyCondition, condition.InitReason, instancehav1.InstanceHaReadyInitMessage),
		// service account, role, rolebinding conditions
		condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
		condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
		condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
		condition.UnknownCondition(condition.NetworkAttachmentsReadyCondition, condition.InitReason, condition.NetworkAttachmentsReadyInitMessage),
	)
	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	//// mark
	if instance.Status.NetworkAttachments == nil {
		instance.Status.NetworkAttachments = map[string][]string{}
	}

	// Init Topology condition if there's a reference
	if instance.Spec.TopologyRef != nil {
		c := condition.UnknownCondition(condition.TopologyReadyCondition, condition.InitReason, condition.TopologyReadyInitMessage)
		cl.Set(c)
	}

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

	Labels := map[string]string{
		common.AppSelector: "instanceha",
	}

	configVars := make(map[string]env.Setter)

	_, configMapHash, err := configmap.GetConfigMapAndHashWithName(ctx, helper, instance.Spec.OpenStackConfigMap, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				instancehav1.InstanceHaReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				instancehav1.InstanceHaOpenStackConfigMapWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			instancehav1.InstanceHaReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			instancehav1.InstanceHaReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configVars[instance.Spec.OpenStackConfigMap] = env.SetValue(configMapHash)

	_, secretHash, err := secret.GetSecret(ctx, helper, instance.Spec.OpenStackConfigSecret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				instancehav1.InstanceHaReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				instancehav1.InstanceHaOpenStackConfigSecretWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			instancehav1.InstanceHaReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			instancehav1.InstanceHaReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configVars[instance.Spec.OpenStackConfigSecret] = env.SetValue(secretHash)

	_, secretHash, err = secret.GetSecret(ctx, helper, instance.Spec.FencingSecret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				instancehav1.InstanceHaReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				instancehav1.InstanceHaFencingSecretWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			instancehav1.InstanceHaReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			instancehav1.InstanceHaReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configVars[instance.Spec.FencingSecret] = env.SetValue(secretHash)

	// Check if instance.Spec.InstanceHaConfigMap is present, if not create it.
	// The first time this will produce an error b/c GetConfigMapAndHashWithName doesn't support retries.
	_, configMapHash, err = configmap.GetConfigMapAndHashWithName(ctx, helper, instance.Spec.InstanceHaConfigMap, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			cmLabels := labels.GetLabels(instance, labels.GetGroupLabel("instanceha"), map[string]string{})
			envVars := make(map[string]env.Setter)
			cms := []util.Template{
				{
					Name:               instance.Spec.InstanceHaConfigMap,
					Namespace:          instance.Namespace,
					Type:               util.TemplateTypeConfig,
					InstanceType:       instance.Kind,
					AdditionalTemplate: map[string]string{},
					Labels:             cmLabels,
				},
			}

			err = configmap.EnsureConfigMaps(ctx, helper, instance, cms, &envVars)
			if err != nil {
				return ctrl.Result{}, err
			}

			_, configMapHash, err = configmap.GetConfigMapAndHashWithName(ctx, helper, instance.Spec.InstanceHaConfigMap, instance.Namespace)
			if err != nil {
				if k8s_errors.IsNotFound(err) {
					instance.Status.Conditions.Set(condition.FalseCondition(
						instancehav1.InstanceHaReadyCondition,
						condition.RequestedReason,
						condition.SeverityInfo,
						instancehav1.InstanceHaConfigMapWaitingMessage))
					return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
				}
				instance.Status.Conditions.Set(condition.FalseCondition(
					instancehav1.InstanceHaReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					instancehav1.InstanceHaReadyErrorMessage,
					err.Error()))
				return ctrl.Result{}, err
			}

			configVars[instance.Spec.InstanceHaConfigMap] = env.SetValue(configMapHash)
		} else {
			// Catch and log generic error fetching the configmap
			Log.Error(err, fmt.Sprintf("could not fetch configmap %s", instance.Spec.InstanceHaConfigMap))
			return ctrl.Result{}, err
		}
	}
	configVars[instance.Spec.InstanceHaConfigMap] = env.SetValue(configMapHash)

	if instance.Spec.CaBundleSecretName != "" {
		secretHash, err := tls.ValidateCACertSecret(
			ctx,
			helper.GetClient(),
			types.NamespacedName{
				Name:      instance.Spec.CaBundleSecretName,
				Namespace: instance.Namespace,
			},
		)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.TLSInputReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					fmt.Sprintf(condition.TLSInputReadyWaitingMessage, instance.Spec.CaBundleSecretName)))
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

		configVars[instance.Spec.CaBundleSecretName] = env.SetValue(secretHash)
	}

	// all cert input checks out so report InputReady
	instance.Status.Conditions.MarkTrue(condition.TLSInputReadyCondition, condition.InputReadyMessage)

	configVarsHash, err := util.HashOfInputHashes(configVars)
	if err != nil {
		return ctrl.Result{}, err
	}

	// begin custom script inject
	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel("instanceha"), map[string]string{})
	envVars := make(map[string]env.Setter)
	cms := []util.Template{
		// ScriptsConfigMap
		{
			Name:               instance.Name + "-sh",
			Namespace:          instance.Namespace,
			Type:               util.TemplateTypeScripts,
			InstanceType:       instance.Kind,
			AdditionalTemplate: map[string]string{},
			Labels:             cmLabels,
		},
	}

	err = configmap.EnsureConfigMaps(ctx, helper, instance, cms, &envVars)
	if err != nil {
		return ctrl.Result{}, err
	}

	// end custom script inject

	// Create netattachment
	nadList := []networkv1.NetworkAttachmentDefinition{}
	for _, netAtt := range instance.Spec.NetworkAttachments {
		nad, err := nad.GetNADWithName(ctx, helper, netAtt, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				Log.Info(fmt.Sprintf("network-attachment-definition %s not found", netAtt))
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.NetworkAttachmentsReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.NetworkAttachmentsReadyWaitingMessage,
					netAtt))
				return ctrl.Result{RequeueAfter: time.Second * 10}, nil
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.NetworkAttachmentsReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.NetworkAttachmentsReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}

		if nad != nil {
			nadList = append(nadList, *nad)
		}
	}

	serviceAnnotations, err := nad.EnsureNetworksAnnotation(nadList)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create network annotation from %s: %w",
			instance.Spec.NetworkAttachments, err)
	}

	// TODO add check to make sure there is only a single copy of instanceha using the same OpenStackCloud
	cloud := instance.Spec.OpenStackCloud

	containerImage, err := r.GetContainerImage(ctx, instance.Spec.ContainerImage, instance)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to fetch containerImage from ConfigMap: %w", err)
	}

	// Handle Topology
	//
	topology, err := topologyv1.EnsureServiceTopology(
		ctx,
		helper,
		instance.Spec.TopologyRef,
		instance.GetLastAppliedTopologyRef(),
		instance.Name,
		labels.GetAppLabelSelector("instanceha"),
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

	// If TopologyRef is present and ensureServiceTopology returned a valid
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
	commondeployment := commondeployment.NewDeployment(instanceha.Deployment(instance, Labels, serviceAnnotations, cloud, configVarsHash, containerImage, topology), time.Duration(5)*time.Second)
	sfres, sferr := commondeployment.CreateOrPatch(ctx, helper)
	if sferr != nil {
		return sfres, sferr
	}
	deployment := commondeployment.GetDeployment()

	// END deployment

	// NetworkAttachments
	networkReady, networkAttachmentStatus, err := nad.VerifyNetworkStatusFromAnnotation(ctx, helper, instance.Spec.NetworkAttachments, Labels, 1)
	if err != nil {
		return ctrl.Result{}, err
	}
	instance.Status.NetworkAttachments = networkAttachmentStatus

	if networkReady {
		instance.Status.Conditions.MarkTrue(condition.NetworkAttachmentsReadyCondition, condition.NetworkAttachmentsReadyMessage)
	} else {
		err := fmt.Errorf("not all pods have interfaces with ips as configured in NetworkAttachments: %s", instance.Spec.NetworkAttachments)
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.NetworkAttachmentsReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.NetworkAttachmentsReadyErrorMessage,
			err.Error()))

		return ctrl.Result{}, err
	}

	if deployment.Status.ReadyReplicas > 0 {
		instance.Status.Conditions.MarkTrue(instancehav1.InstanceHaReadyCondition, instancehav1.InstanceHaReadyMessage)
	}

	return ctrl.Result{}, nil

}

// fields to index to reconcile when change
const (
	caBundleSecretNameField    = ".spec.caBundleSecretName"
	openStackConfigMapField    = ".spec.openStackConfigMap"
	openStackConfigSecretField = ".spec.openStackConfigSecret"
	fencingSecretField         = ".spec.fencingSecret"
	instanceHaConfigMapField   = ".spec.instanceHaConfigMap"
	topologyField              = ".spec.topologyRef.Name"
)

var (
	allWatchFields = []string{
		caBundleSecretNameField,
		openStackConfigMapField,
		openStackConfigSecretField,
		fencingSecretField,
		instanceHaConfigMapField,
		topologyField,
	}
)

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {

	// index caBundleSecretNameField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, caBundleSecretNameField, func(rawObj client.Object) []string {
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.CaBundleSecretName == "" {
			return nil
		}
		return []string{cr.Spec.CaBundleSecretName}
	}); err != nil {
		return err
	}
	// index openStackConfigMap
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, openStackConfigMapField, func(rawObj client.Object) []string {
		// Extract the configmap name from the spec, if one is provided
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.OpenStackConfigMap == "" {
			return nil
		}
		return []string{cr.Spec.OpenStackConfigMap}
	}); err != nil {
		return err
	}
	// index openStackConfigSecret
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, openStackConfigSecretField, func(rawObj client.Object) []string {
		// Extract the configmap name from the spec, if one is provided
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.OpenStackConfigSecret == "" {
			return nil
		}
		return []string{cr.Spec.OpenStackConfigSecret}
	}); err != nil {
		return err
	}
	// index fencingSecret
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, fencingSecretField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.FencingSecret == "" {
			return nil
		}
		return []string{cr.Spec.FencingSecret}
	}); err != nil {
		return err
	}
	// index instanceHaConfigMap
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, instanceHaConfigMapField, func(rawObj client.Object) []string {
		// Extract the configmap name from the spec, if one is provided
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.InstanceHaConfigMap == "" {
			return nil
		}
		return []string{cr.Spec.InstanceHaConfigMap}
	}); err != nil {
		return err
	}
	// index topologyField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, topologyField, func(rawObj client.Object) []string {
		// Extract the topology name from the spec, if one is provided
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.TopologyRef == nil {
			return nil
		}
		return []string{cr.Spec.TopologyRef.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&instancehav1.InstanceHa{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&topologyv1.Topology{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForSrc),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *Reconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	l := log.FromContext(context.Background()).WithName("Controllers").WithName("InstanceHa")

	for _, field := range allWatchFields {
		crList := &instancehav1.InstanceHaList{}
		listOps := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(field, src.GetName()),
			Namespace:     src.GetNamespace(),
		}
		err := r.List(ctx, crList, listOps)
		if err != nil {
			l.Error(err, fmt.Sprintf("listing %s for field: %s - %s", crList.GroupVersionKind().Kind, field, src.GetNamespace()))
			return requests
		}

		for _, item := range crList.Items {
			l.Info(fmt.Sprintf("input source %s changed, reconcile: %s - %s", src.GetName(), item.GetName(), item.GetNamespace()))

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

func (r *Reconciler) GetContainerImage(
	ctx context.Context,
	containerImage string,
	src client.Object,
) (string, error) {
	cm := &corev1.ConfigMap{}
	instanceHaConfigMapName := "infra-instanceha-config"

	if len(containerImage) > 0 {
		return containerImage, nil
	}

	objectKey := client.ObjectKey{Namespace: src.GetNamespace(), Name: instanceHaConfigMapName}
	err := r.Client.Get(ctx, objectKey, cm)
	if err != nil {
		return "", err
	}

	if cm.Data == nil {
		return util.GetEnvVar("RELATED_IMAGE_INFRA_INSTANCE_HA_IMAGE_URL_DEFAULT", ""), nil
	}

	if cmImage, exists := cm.Data["instanceha-image"]; exists {
		return cmImage, nil
	}

	return "", nil
}
