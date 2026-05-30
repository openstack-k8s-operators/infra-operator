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

// Package instanceha implements the InstanceHA controller for managing high availability instances
package instanceha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"

	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"

	commondeployment "github.com/openstack-k8s-operators/lib-common/modules/common/deployment"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	commonservice "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	instanceha "github.com/openstack-k8s-operators/infra-operator/internal/instanceha"
)

// Reconciler reconciles a InstanceHa object
type Reconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Kclient kubernetes.Interface
}

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *Reconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("InstanceHa")
}

// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=instancehas/finalizers,verbs=update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update;patch
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=topology.openstack.org,resources=topologies,verbs=get;list;watch;update

// Reconcile -
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	instance := &instancehav1.InstanceHa{}
	err := r.Get(ctx, req.NamespacedName, instance)
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

	// Patch the instance status when exiting this function so we can persist any changes,
	// unless a panic occurred during reconciliation.
	defer func() {
		if panicVal := recover(); panicVal != nil {
			Log.Error(fmt.Errorf("panic: %v", panicVal), "panic during reconcile, not updating status")
			panic(panicVal)
		}
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
		condition.UnknownCondition(condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage),
		condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		condition.UnknownCondition(condition.TLSInputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
		condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		// service account, role, rolebinding conditions
		condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
		condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
		condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
		condition.UnknownCondition(condition.NetworkAttachmentsReadyCondition, condition.InitReason, condition.NetworkAttachmentsReadyInitMessage),
		condition.UnknownCondition(condition.CreateServiceReadyCondition, condition.InitReason, condition.CreateServiceReadyInitMessage),
	)
	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) || isNewInstance {
		return ctrl.Result{}, nil
	}

	//// mark
	if instance.Status.NetworkAttachments == nil {
		instance.Status.NetworkAttachments = map[string][]string{}
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
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create", "patch"},
		},
	}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	deploymentLabels := map[string]string{
		common.AppSelector: instance.Name,
	}

	configVars := make(map[string]env.Setter)

	_, configMapHash, err := configmap.GetConfigMapAndHashWithName(ctx, helper, instance.Spec.OpenStackConfigMap, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				instancehav1.InstanceHaOpenStackConfigMapWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configVars[instance.Spec.OpenStackConfigMap] = env.SetValue(configMapHash)

	_, secretHash, err := secret.GetSecret(ctx, helper, instance.Spec.OpenStackConfigSecret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Since the OpenStack config secret should have been manually created by the user and referenced in the spec,
			// we treat this as a warning because it means that the service will not be able to start.
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				instancehav1.InstanceHaOpenStackConfigSecretWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	configVars[instance.Spec.OpenStackConfigSecret] = env.SetValue(secretHash)

	_, secretHash, err = secret.GetSecret(ctx, helper, instance.Spec.FencingSecret, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Since the fencing secret should have been manually created by the user and referenced in the spec,
			// we treat this as a warning because it means that the service will not be able to start.
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				instancehav1.InstanceHaFencingSecretWaitingMessage))
			return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
		}
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.InputReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.InputReadyErrorMessage,
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
					// Since the instance HA config map should have been manually created by the user
					// or automatically created by another operator and referenced in the spec, we
					// treat this as a warning because it means that the service will not be able
					// to start.
					instance.Status.Conditions.Set(condition.FalseCondition(
						condition.InputReadyCondition,
						condition.ErrorReason,
						condition.SeverityWarning,
						instancehav1.InstanceHaConfigMapWaitingMessage))
					return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
				}
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.InputReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.InputReadyErrorMessage,
					err.Error()))
				return ctrl.Result{}, err
			}

			configVars[instance.Spec.InstanceHaConfigMap] = env.SetValue(configMapHash)
		} else {
			// Catch and log generic error fetching the configmap
			Log.Error(err, fmt.Sprintf("could not fetch configmap %s", instance.Spec.InstanceHaConfigMap))
			return ctrl.Result{}, err
		}
	} else {
		configVars[instance.Spec.InstanceHaConfigMap] = env.SetValue(configMapHash)
	}

	// Application Credential handling (passive mode: user provides the AC secret)
	acSecretName := ""
	if instance.Spec.Auth.ApplicationCredentialSecret != "" {
		acSecretName = instance.Spec.Auth.ApplicationCredentialSecret
		_, acSecretHash, err := secret.GetSecret(ctx, helper, acSecretName, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.InputReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					instancehav1.InstanceHaACSecretWaitingMessage))
				return ctrl.Result{RequeueAfter: time.Duration(10) * time.Second}, nil
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.InputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.InputReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		configVars[acSecretName] = env.SetValue(acSecretHash)
	}

	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, condition.InputReadyMessage)

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
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.TLSInputReadyWaitingMessage, instance.Spec.CaBundleSecretName))
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

	metricsTLSExplicit := instance.Spec.MetricsTLS.Enabled()
	metricsTLS := instance.Spec.MetricsTLS.DeepCopy()
	if !metricsTLSExplicit {
		certName := instanceha.DefaultMetricsCertSecret
		metricsTLS.SecretName = &certName
	}

	hash, err := metricsTLS.ValidateCertSecret(ctx, helper, instance.Namespace)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			if metricsTLSExplicit {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.TLSInputReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.TLSInputReadyWaitingMessage, err.Error()))
				return ctrl.Result{}, nil
			}
			// Auto-detect: default cert not found, proceed without TLS
			metricsTLS.SecretName = nil
		} else {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.TLSInputReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.TLSInputErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
	} else {
		configVars[tls.TLSHashName+"_metrics"] = env.SetValue(hash)
	}

	// all cert input checks out so report InputReady
	instance.Status.Conditions.MarkTrue(condition.TLSInputReadyCondition, condition.InputReadyMessage)

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

	// Add the script ConfigMap hash to configVars to trigger pod recreation on script changes
	scriptConfigMapName := instance.Name + "-sh"
	_, scriptConfigMapHash, err := configmap.GetConfigMapAndHashWithName(ctx, helper, scriptConfigMapName, instance.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	configVars[scriptConfigMapName] = env.SetValue(scriptConfigMapHash)

	// end custom script inject

	// Ensure heartbeat HMAC key secret exists (auto-generated)
	heartbeatHMACSecretName := instance.Name + "-heartbeat-hmac"
	hmacSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: heartbeatHMACSecretName, Namespace: instance.Namespace}, hmacSecret)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			key := make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to generate heartbeat HMAC key: %w", err)
			}
			hmacSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      heartbeatHMACSecretName,
					Namespace: instance.Namespace,
					Labels: labels.GetLabels(instance, labels.GetGroupLabel("instanceha"), map[string]string{
						"backup.openstack.org/category":      "controlplane",
						"backup.openstack.org/restore":       "true",
						"backup.openstack.org/restore-order": "10",
					}),
				},
				Data: map[string][]byte{
					"hmac-key":          []byte(hex.EncodeToString(key)),
					"hmac-key-previous": {},
				},
			}
			if err = controllerutil.SetControllerReference(instance, hmacSecret, r.Scheme); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set owner reference on HMAC secret: %w", err)
			}
			if err = r.Create(ctx, hmacSecret); err != nil {
				if !k8s_errors.IsAlreadyExists(err) {
					return ctrl.Result{}, fmt.Errorf("failed to create heartbeat HMAC secret: %w", err)
				}
				if err = r.Get(ctx, types.NamespacedName{Name: heartbeatHMACSecretName, Namespace: instance.Namespace}, hmacSecret); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to get existing heartbeat HMAC secret: %w", err)
				}
			} else {
				Log.Info("Created heartbeat HMAC key secret", "secret", heartbeatHMACSecretName)
			}
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get heartbeat HMAC secret: %w", err)
		}
	}
	instance.Status.HeartbeatHMACSecret = heartbeatHMACSecretName

	// Handle annotation-based key rotation.
	//
	// The annotation value is used as an idempotency guard:
	// - "true" (or any non-ResourceVersion value) = rotation requested
	// - "<resourceVersion>" = rotation completed, waiting for annotation removal
	//
	// This prevents double-rotation if the annotation removal Patch fails:
	// the next reconcile sees the value matches the secret's ResourceVersion
	// and skips straight to removing the annotation.
	const rotateAnnotation = "instanceha.openstack.org/rotate-hmac-key"
	if instance.Annotations[rotateAnnotation] != "" {
		alreadyRotated := instance.Annotations[rotateAnnotation] == hmacSecret.ResourceVersion

		if !alreadyRotated {
			currentKey := hmacSecret.Data["hmac-key"]
			newKey := make([]byte, 32)
			if _, err = rand.Read(newKey); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to generate new heartbeat HMAC key: %w", err)
			}
			hmacSecret.Data["hmac-key-previous"] = currentKey
			hmacSecret.Data["hmac-key"] = []byte(hex.EncodeToString(newKey))
			if err = r.Update(ctx, hmacSecret); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to rotate heartbeat HMAC key: %w", err)
			}
			Log.Info("Rotated heartbeat HMAC key", "secret", heartbeatHMACSecretName)

			// Stamp the annotation with the new secret ResourceVersion so a
			// requeue after a failed removal doesn't re-rotate.
			stampBase := client.MergeFrom(instance.DeepCopy())
			instance.Annotations[rotateAnnotation] = hmacSecret.ResourceVersion
			if err = r.Patch(ctx, instance, stampBase); err != nil {
				Log.Info("Failed to stamp rotation version, will retry", "error", err)
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
		} else {
			Log.Info("HMAC key rotation already applied, removing annotation")
		}

		// Remove the annotation via MergeFrom Patch to avoid conflicts
		// with the deferred PatchInstance.
		metaBase := client.MergeFrom(instance.DeepCopy())
		delete(instance.Annotations, rotateAnnotation)
		if err = r.Patch(ctx, instance, metaBase); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove rotate annotation: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	_, hmacSecretHash, err := secret.GetSecret(ctx, helper, heartbeatHMACSecretName, instance.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to hash heartbeat HMAC secret: %w", err)
	}
	configVars[heartbeatHMACSecretName] = env.SetValue(hmacSecretHash)

	// Calculate the config hash AFTER adding the script ConfigMap hash
	configVarsHash, err := util.HashOfInputHashes(configVars)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create netattachment
	nadList := []networkv1.NetworkAttachmentDefinition{}
	for _, netAtt := range instance.Spec.NetworkAttachments {
		nadObj, err := nad.GetNADWithName(ctx, helper, netAtt, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				// Since the net-attach-def CR should have been manually created by the user and referenced in the spec,
				// we treat this as a warning because it means that the service will not be able to start.
				Log.Info(fmt.Sprintf("network-attachment-definition %s not found", netAtt))
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.NetworkAttachmentsReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
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

		if nadObj != nil {
			nadList = append(nadList, *nadObj)
		}
	}

	serviceAnnotations, err := nad.EnsureNetworksAnnotation(nadList)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create network annotation from %s: %w",
			instance.Spec.NetworkAttachments, err)
	}

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
		labels.GetLabelSelector(deploymentLabels),
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
	commonsvc, err := commonservice.NewService(instanceha.MetricsService(instance), time.Duration(5)*time.Second, nil)
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
	instance.Status.Conditions.MarkTrue(condition.CreateServiceReadyCondition, condition.CreateServiceReadyMessage)

	// Delete existing Deployment if its selector doesn't match the desired labels.
	// Deployment selectors are immutable after creation, so a mismatch (e.g. from
	// upgrading to instance-scoped labels) requires a delete+recreate.
	existingDep := &appsv1.Deployment{}
	if err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, existingDep); err == nil {
		if existingDep.Spec.Selector != nil {
			if val, ok := existingDep.Spec.Selector.MatchLabels[common.AppSelector]; ok && val != deploymentLabels[common.AppSelector] {
				Log.Info("Deleting Deployment with stale selector for recreation",
					"oldSelector", val, "newSelector", deploymentLabels[common.AppSelector])
				if err = r.Delete(ctx, existingDep); err != nil && !k8s_errors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("failed to delete Deployment with stale selector: %w", err)
				}
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
		}
	}

	deployment := commondeployment.NewDeployment(instanceha.Deployment(instance, deploymentLabels, serviceAnnotations, cloud, configVarsHash, containerImage, topology, acSecretName, heartbeatHMACSecretName, metricsTLS), time.Duration(5)*time.Second)
	sfres, sferr := deployment.CreateOrPatch(ctx, helper)
	if sferr != nil {
		return sfres, sferr
	}

	// END deployment

	// NetworkAttachments
	networkReady, networkAttachmentStatus, err := nad.VerifyNetworkStatusFromAnnotation(ctx, helper, instance.Spec.NetworkAttachments, deploymentLabels, deployment.GetDeployment().Status.ReadyReplicas)
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

	if commondeployment.IsReady(deployment.GetDeployment()) {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		// It is OK to return success as we are watching for Deployment changes
		return ctrl.Result{}, nil
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
	acSecretField              = ".spec.auth.applicationCredentialSecret" // #nosec G101
	metricsTLSField            = ".spec.metricsTLS.secretName"            // #nosec G101
)

var allWatchFields = []string{
	caBundleSecretNameField,
	openStackConfigMapField,
	openStackConfigSecretField,
	fencingSecretField,
	instanceHaConfigMapField,
	topologyField,
	acSecretField,
	metricsTLSField,
}

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

	// index acSecretField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, acSecretField, func(rawObj client.Object) []string {
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.Auth.ApplicationCredentialSecret == "" {
			return nil
		}
		return []string{cr.Spec.Auth.ApplicationCredentialSecret}
	}); err != nil {
		return err
	}

	// index metricsTLSField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &instancehav1.InstanceHa{}, metricsTLSField, func(rawObj client.Object) []string {
		cr := rawObj.(*instancehav1.InstanceHa)
		if cr.Spec.MetricsTLS.SecretName == nil {
			return []string{instanceha.DefaultMetricsCertSecret}
		}
		return []string{*cr.Spec.MetricsTLS.SecretName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&instancehav1.InstanceHa{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
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

	Log := r.GetLogger(ctx)

	for _, field := range allWatchFields {
		crList := &instancehav1.InstanceHaList{}
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

func (r *Reconciler) reconcileDelete(ctx context.Context, instance *instancehav1.InstanceHa, helper *helper.Helper) (ctrl.Result, error) {
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

// GetContainerImage returns the container image to use for the instance, either from
// the provided containerImage parameter or from the infra-instanceha-config ConfigMap
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
	err := r.Get(ctx, objectKey, cm)
	if err != nil {
		return "", err
	}

	if cm.Data == nil {
		return util.GetEnvVar("RELATED_IMAGE_INFRA_INSTANCE_HA_IMAGE_URL_DEFAULT", ""), nil
	}

	if cmImage, exists := cm.Data["instanceha-image"]; exists {
		return cmImage, nil
	}

	return "", fmt.Errorf("no container image found: key 'instanceha-image' not in ConfigMap %s and RELATED_IMAGE_INFRA_INSTANCE_HA_IMAGE_URL_DEFAULT not set", instanceHaConfigMapName)
}
