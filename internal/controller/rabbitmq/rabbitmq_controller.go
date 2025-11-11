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

// Package rabbitmq implements the RabbitMQ controller for managing RabbitMQ cluster instances
package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq/impl"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	"github.com/openstack-k8s-operators/lib-common/modules/common/ocp"
	"github.com/openstack-k8s-operators/lib-common/modules/common/pdb"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// GetLogger returns a logger object with a prefix of "controller.name" and additional controller context fields
func (r *Reconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("RabbitMq")
}

// fields to index to reconcile on CR change
const (
	serviceSecretNameField = ".spec.tls.SecretName"
	caSecretNameField      = ".spec.tls.CASecretName"
	topologyField          = ".spec.topologyRef.Name"
)

var rmqAllWatchFields = []string{
	serviceSecretNameField,
	caSecretNameField,
	topologyField,
}

// Reconciler reconciles a RabbitMq object
type Reconciler struct {
	client.Client
	Kclient kubernetes.Interface
	config  *rest.Config
	Scheme  *runtime.Scheme
}

// +kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=rabbitmqs/finalizers,verbs=update

// +kubebuilder:rbac:groups=rabbitmq.com,resources=rabbitmqclusters,verbs=get;list;watch;create;update;patch;delete

// Required to determine IPv6 and FIPS
// +kubebuilder:rbac:groups=config.openshift.io,resources=networks,verbs=get;list;watch;

// Required to exec into pods
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete;
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create

// Required to manage PodDisruptionBudgets for multi-replica deployments
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

// Required to create per-pod LoadBalancer services
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile - RabbitMq
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	Log := r.GetLogger(ctx)

	// Fetch the RabbitMq instance
	instance := &rabbitmqv1beta1.RabbitMq{}
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
		// configmap generation
		condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
		// rabbitmq pods ready
		condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		// PDB ready
		condition.UnknownCondition(condition.PDBReadyCondition, condition.InitReason, condition.PDBReadyInitMessage),
		// per-pod services ready
		condition.UnknownCondition(condition.CreateServiceReadyCondition, condition.InitReason, condition.CreateServiceReadyInitMessage),
	)

	instance.Status.Conditions.Init(&cl)
	instance.Status.ObservedGeneration = instance.Generation

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
	// TLS input validation
	//
	// Validate service cert secret
	if instance.Spec.TLS.SecretName != "" {
		// Create a fake service to validate
		srv := tls.Service{
			SecretName: instance.Spec.TLS.SecretName,
		}
		if instance.Spec.TLS.CaSecretName == instance.Spec.TLS.SecretName {
			srv.CaMount = ptr.To("/dev/null")
		}
		_, err := srv.ValidateCertSecret(ctx, helper, instance.Namespace)
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
	}
	// all cert input checks out so report InputReady
	instance.Status.Conditions.MarkTrue(condition.TLSInputReadyCondition, condition.InputReadyMessage)

	IPv6Enabled, err := ocp.FirstClusterNetworkIsIPv6(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error getting cluster IPv6 config: %w", err)
	}

	fipsEnabled, err := ocp.IsFipsCluster(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error getting cluster FIPS config: %w", err)
	}

	// NOTE(dciabrin) OSPRH-20331 reported RabbitMQ partitionning during
	// key update events, so until this can be resolved, revert to the
	// same configuration scheme as OSP17 (see OSPRH-13633)
	var tlsVersions string
	if fipsEnabled {
		tlsVersions = "['tlsv1.2','tlsv1.3']"
	} else {
		tlsVersions = "['tlsv1.2']"
	}
	// RabbitMq config maps
	cms := []util.Template{
		{
			Name:         fmt.Sprintf("%s-config-data", instance.Name),
			Namespace:    instance.Namespace,
			Type:         util.TemplateTypeConfig,
			InstanceType: "rabbitmq",
			Labels:       map[string]string{},
			CustomData: map[string]string{
				"inter_node_tls.config": fmt.Sprintf(`[
  {server, [
    {cacertfile,"/etc/rabbitmq-tls/ca.crt"},
    {certfile,"/etc/rabbitmq-tls/tls.crt"},
    {keyfile,"/etc/rabbitmq-tls/tls.key"},
    {secure_renegotiate, true},
    {fail_if_no_peer_cert, true},
    {verify, verify_peer},
    {versions, %s}
  ]},
  {client, [
    {cacertfile,"/etc/rabbitmq-tls/ca.crt"},
    {certfile,"/etc/rabbitmq-tls/tls.crt"},
    {keyfile,"/etc/rabbitmq-tls/tls.key"},
    {secure_renegotiate, true},
    {verify, verify_peer},
    {versions, %s}
  ]}
].
`, tlsVersions, tlsVersions),
			},
		},
	}

	err = configmap.EnsureConfigMaps(ctx, helper, instance, cms, nil)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error calculating configmap hash: %w", err)
	}

	rabbitmqCluster := &rabbitmqv2.RabbitmqCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	err = instance.Spec.MarshalInto(&rabbitmqCluster.Spec)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error creating RabbitmqCluster Spec: %w", err)
	}

	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	//
	// Handle Topology
	//
	topology, err := topologyv1.EnsureServiceTopology(
		ctx,
		helper,
		instance.Spec.TopologyRef,
		instance.Status.LastAppliedTopology,
		instance.Name,
		labels.GetLabelSelector(
			map[string]string{
				labels.K8sAppName:      instance.Name,
				labels.K8sAppComponent: "rabbitmq",
				labels.K8sAppPartOf:    "rabbitmq",
			},
		),
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

	// If TopologyRef is present and ensureHeatTopology returned a valid
	// topology object, set .Status.LastAppliedTopology to the referenced one
	// and mark the condition as true
	if instance.Spec.TopologyRef != nil {
		// update the Status with the last retrieved TopologyRef
		instance.Status.LastAppliedTopology = instance.Spec.TopologyRef
		// update the TopologyRef associated condition
		instance.Status.Conditions.MarkTrue(condition.TopologyReadyCondition, condition.TopologyReadyMessage)
	} else {
		// remove LastAppliedTopology from the .Status
		instance.Status.LastAppliedTopology = nil
	}

	err = rabbitmq.ConfigureCluster(rabbitmqCluster, IPv6Enabled, fipsEnabled, topology, instance.Spec.NodeSelector, instance.Spec.Override)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error configuring RabbitmqCluster: %w", err)
	}

	rabbitmqImplCluster := impl.NewRabbitMqCluster(rabbitmqCluster, 5)
	rmqres, rmqerr := rabbitmqImplCluster.CreateOrPatch(ctx, helper)
	if rmqerr != nil {
		return rmqres, rmqerr
	}
	rabbitmqClusterInstance := rabbitmqImplCluster.GetRabbitMqCluster()

	clusterReady := false
	if rabbitmqClusterInstance.Status.ObservedGeneration == rabbitmqClusterInstance.Generation {
		for _, oldCond := range rabbitmqClusterInstance.Status.Conditions {
			// Forced to hardcode "ClusterAvailable" here because linter will not allow
			// us to import "github.com/rabbitmq/cluster-operator/internal/status"
			if string(oldCond.Type) == "ClusterAvailable" && oldCond.Status == corev1.ConditionTrue {
				clusterReady = true
			}
		}
	}

	if clusterReady {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)

		labelMap := map[string]string{
			labels.K8sAppName:      instance.Name,
			labels.K8sAppComponent: "rabbitmq",
			labels.K8sAppPartOf:    "rabbitmq",
		}

		if instance.Spec.Replicas != nil && *instance.Spec.Replicas > 1 {
			// Apply PDB for multi-replica deployments
			pdbSpec := pdb.MaxUnavailablePodDisruptionBudget(
				instance.Name,
				instance.Namespace,
				intstr.FromInt(1),
				labelMap,
			)
			pdbInstance := pdb.NewPDB(pdbSpec, 5*time.Second)

			_, err := pdbInstance.CreateOrPatch(ctx, helper)
			if err != nil {
				Log.Error(err, "Could not apply PDB")
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.PDBReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.PDBReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
		}
		instance.Status.Conditions.MarkTrue(condition.PDBReadyCondition, condition.PDBReadyMessage)

		// Create per-pod services when podOverride is configured
		if instance.Spec.Replicas != nil && *instance.Spec.Replicas > 0 &&
			instance.Spec.PodOverride != nil && len(instance.Spec.PodOverride.Services) > 0 {
			ctrlResult, err := r.reconcilePerPodServices(ctx, instance, helper, labelMap)
			if err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.CreateServiceReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.CreateServiceReadyErrorMessage, err.Error()))
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.CreateServiceReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.CreateServiceReadyRunningMessage))
				return ctrlResult, nil
			}
		} else if len(instance.Status.ServiceHostnames) > 0 {
			// PodOverride was removed, clean up per-pod services
			if err := r.deletePerPodServices(ctx, instance); err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.CreateServiceReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.CreateServiceReadyErrorMessage, err.Error()))
				return ctrl.Result{}, err
			}
			instance.Status.ServiceHostnames = nil
		}
		instance.Status.Conditions.MarkTrue(condition.CreateServiceReadyCondition, condition.CreateServiceReadyMessage)

		// Let's wait DeploymentReadyCondition=True to apply the policy
		// QueueType should never be nil due to webhook defaulting, but add safety check
		if instance.Spec.QueueType != nil {
			if *instance.Spec.QueueType == rabbitmqv1beta1.QueueTypeMirrored && *instance.Spec.Replicas > 1 && instance.Status.QueueType != rabbitmqv1beta1.QueueTypeMirrored {
				Log.Info("ha-all policy not present. Applying.")
				err := ensureMirroredPolicy(ctx, helper, instance)
				if err != nil {
					Log.Error(err, "Could not apply ha-all policy")
					instance.Status.Conditions.Set(condition.FalseCondition(
						condition.DeploymentReadyCondition,
						condition.ErrorReason,
						condition.SeverityWarning,
						condition.DeploymentReadyErrorMessage, err.Error()))
					return ctrl.Result{}, err
				}
				instance.Status.QueueType = rabbitmqv1beta1.QueueTypeMirrored
			} else if *instance.Spec.QueueType != rabbitmqv1beta1.QueueTypeMirrored && instance.Status.QueueType == rabbitmqv1beta1.QueueTypeMirrored {
				Log.Info("QueueType changed from Mirrored. Removing ha-all policy")
				err := deleteMirroredPolicy(ctx, helper, instance)
				if err != nil {
					Log.Error(err, "Could not remove ha-all policy")
					instance.Status.Conditions.Set(condition.FalseCondition(
						condition.DeploymentReadyCondition,
						condition.ErrorReason,
						condition.SeverityWarning,
						condition.DeploymentReadyErrorMessage, err.Error()))
					return ctrl.Result{}, err
				}
				instance.Status.QueueType = ""
			}

			// Update status for Quorum queue type
			if *instance.Spec.QueueType == rabbitmqv1beta1.QueueTypeQuorum && instance.Status.QueueType != rabbitmqv1beta1.QueueTypeQuorum {
				Log.Info("Setting queue type status to quorum")
				instance.Status.QueueType = rabbitmqv1beta1.QueueTypeQuorum
			} else if *instance.Spec.QueueType != rabbitmqv1beta1.QueueTypeQuorum && instance.Status.QueueType == rabbitmqv1beta1.QueueTypeQuorum {
				Log.Info("Removing quorum queue type status")
				instance.Status.QueueType = ""
			}

			// Update status for None queue type
			if *instance.Spec.QueueType == rabbitmqv1beta1.QueueTypeNone && instance.Status.QueueType != "" {
				Log.Info("Setting queue type status to None (clearing)")
				instance.Status.QueueType = ""
			}
		}
	}

	if instance.Status.Conditions.AllSubConditionIsTrue() {
		instance.Status.Conditions.MarkTrue(
			condition.ReadyCondition, condition.ReadyMessage)
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) reconcilePerPodServices(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq, helper *helper.Helper, labelMap map[string]string) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	if instance.Spec.PodOverride == nil || len(instance.Spec.PodOverride.Services) == 0 {
		Log.Info("PodOverride not configured, skipping per-pod service creation")
		instance.Status.ServiceHostnames = nil
		return ctrl.Result{}, nil
	}

	replicas := int(*instance.Spec.Replicas)

	if len(instance.Spec.PodOverride.Services) != replicas {
		return ctrl.Result{}, fmt.Errorf("number of services in podOverride (%d) must match number of replicas (%d)", len(instance.Spec.PodOverride.Services), replicas)
	}

	Log.Info("Creating per-pod services using podOverride configuration")

	var serviceHostnames []string
	var requeueNeeded bool
	for i := 0; i < replicas; i++ {
		podName := fmt.Sprintf("%s-server-%d", instance.Name, i)
		svcName := podName

		svc, err := service.NewService(
			service.GenericService(&service.GenericServiceDetails{
				Name:      svcName,
				Namespace: instance.Namespace,
				Labels:    labelMap,
				Selector: map[string]string{
					appsv1.StatefulSetPodNameLabel: podName,
				},
				Ports: []corev1.ServicePort{
					{Name: "amqp", Port: 5672, TargetPort: intstr.FromInt(5672)},
					{Name: "amqps", Port: 5671, TargetPort: intstr.FromInt(5671)},
				},
			}),
			5,
			&instance.Spec.PodOverride.Services[i],
		)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.CreateServiceReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.CreateServiceReadyErrorMessage, err.Error()))
			return ctrl.Result{}, err
		}

		if svc.GetServiceType() == corev1.ServiceTypeLoadBalancer {
			svc.AddAnnotation(map[string]string{
				service.AnnotationHostnameKey: svc.GetServiceHostname(),
			})
		}

		ctrlResult, err := svc.CreateOrPatch(ctx, helper)
		if err != nil {
			// Check if this is a LoadBalancer IP pending error - if so, continue with other services
			if k8s_errors.IsServiceUnavailable(err) || strings.Contains(err.Error(), "LoadBalancer IP still pending") {
				requeueNeeded = true
			} else {
				// Real error, return immediately
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.CreateServiceReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.CreateServiceReadyErrorMessage, err.Error()))
				return ctrlResult, err
			}
		} else if (ctrlResult != ctrl.Result{}) {
			requeueNeeded = true
		}

		serviceHostnames = append(serviceHostnames, svc.GetServiceHostname())
		instance.Status.ServiceHostnames = serviceHostnames
	}

	if requeueNeeded {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.CreateServiceReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.CreateServiceReadyRunningMessage))
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) deletePerPodServices(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq) error {
	// List all services owned by this RabbitMq instance
	serviceList := &corev1.ServiceList{}
	listOpts := []client.ListOption{
		client.InNamespace(instance.Namespace),
	}

	if err := r.List(ctx, serviceList, listOpts...); err != nil {
		return err
	}

	// Delete services that are owned by this RabbitMq instance
	for _, svc := range serviceList.Items {
		for _, ownerRef := range svc.GetOwnerReferences() {
			if ownerRef.UID == instance.UID {
				if err := r.Delete(ctx, &svc); err != nil && !k8s_errors.IsNotFound(err) {
					return err
				}
				break
			}
		}
	}
	return nil
}

func ensureMirroredPolicy(ctx context.Context, helper *helper.Helper, instance *rabbitmqv1beta1.RabbitMq) error {
	policyName := types.NamespacedName{
		Name:      instance.Name + "-ha-all",
		Namespace: instance.Namespace,
	}

	policy := &rabbitmqv1beta1.RabbitMQPolicy{}
	err := helper.GetClient().Get(ctx, policyName, policy)

	// Policy already exists
	if err == nil {
		return nil
	}

	// Return error if it's not NotFound
	if !k8s_errors.IsNotFound(err) {
		return err
	}

	// Create the policy CR
	definition := map[string]interface{}{
		"ha-mode":                "exactly",
		"ha-params":              2,
		"ha-promote-on-shutdown": "always",
	}
	definitionJSON, marshalErr := json.Marshal(definition)
	if marshalErr != nil {
		return marshalErr
	}

	policy = &rabbitmqv1beta1.RabbitMQPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName.Name,
			Namespace: policyName.Namespace,
		},
		Spec: rabbitmqv1beta1.RabbitMQPolicySpec{
			RabbitmqClusterName: instance.Name,
			Name:                "ha-all",
			Pattern:             "",
			Definition:          apiextensionsv1.JSON{Raw: definitionJSON},
			Priority:            0,
			ApplyTo:             "all",
		},
	}
	if err := controllerutil.SetControllerReference(instance, policy, helper.GetScheme()); err != nil {
		return err
	}
	return helper.GetClient().Create(ctx, policy)
}

func deleteMirroredPolicy(ctx context.Context, helper *helper.Helper, instance *rabbitmqv1beta1.RabbitMq) error {
	policyName := types.NamespacedName{
		Name:      instance.Name + "-ha-all",
		Namespace: instance.Namespace,
	}

	policy := &rabbitmqv1beta1.RabbitMQPolicy{}
	err := helper.GetClient().Get(ctx, policyName, policy)

	// Policy doesn't exist, nothing to delete
	if k8s_errors.IsNotFound(err) {
		return nil
	}

	// Return error if Get() failed for other reasons
	if err != nil {
		return err
	}

	// Delete the policy
	return helper.GetClient().Delete(ctx, policy)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)
	Log.Info("Reconciling Service delete")

	// Delete per-pod services if they exist
	if err := r.deletePerPodServices(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	rabbitmqCluster := impl.NewRabbitMqCluster(
		&rabbitmqv2.RabbitmqCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instance.Name,
				Namespace: instance.Namespace,
			},
		},
		5,
	)
	err := rabbitmqCluster.Delete(ctx, helper)
	if err != nil {
		return ctrl.Result{}, err
	}

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

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.config = mgr.GetConfig()

	// Various CR fields need to be indexed to filter watch events
	// for the secret changes we want to be notified of
	// index TLS secretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rabbitmqv1beta1.RabbitMq{}, serviceSecretNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*rabbitmqv1beta1.RabbitMq)
		tls := &cr.Spec.TLS
		if tls.SecretName != "" {
			return []string{tls.SecretName}
		}
		return nil
	}); err != nil {
		return err
	}

	// index TLS CA secretName
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rabbitmqv1beta1.RabbitMq{}, caSecretNameField, func(rawObj client.Object) []string {
		// Extract the secret name from the spec, if one is provided
		cr := rawObj.(*rabbitmqv1beta1.RabbitMq)
		tls := &cr.Spec.TLS
		if tls.CaSecretName != "" {
			return []string{tls.CaSecretName}
		}
		return nil
	}); err != nil {
		return err
	}

	// index topologyField
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &rabbitmqv1beta1.RabbitMq{}, topologyField, func(rawObj client.Object) []string {
		// Extract the topology name from the spec, if one is provided
		cr := rawObj.(*rabbitmqv1beta1.RabbitMq)
		if cr.Spec.TopologyRef == nil {
			return nil
		}
		return []string{cr.Spec.TopologyRef.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&rabbitmqv1beta1.RabbitMq{}).
		Owns(&rabbitmqv2.RabbitmqCluster{}).
		Owns(&corev1.Service{}).
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

// findObjectsForSrc - returns a reconcile request if the object is referenced by a Redis CR
func (r *Reconciler) findObjectsForSrc(ctx context.Context, src client.Object) []reconcile.Request {
	requests := []reconcile.Request{}

	Log := r.GetLogger(ctx)

	for _, field := range rmqAllWatchFields {
		crList := &rabbitmqv1beta1.RabbitMqList{}
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
