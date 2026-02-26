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

package v1beta1

import (
	"context"
	"time"

	common_webhook "github.com/openstack-k8s-operators/lib-common/modules/common/webhook"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RabbitMqDefaults -
type RabbitMqDefaults struct {
	ContainerImageURL string
}

var rabbitMqDefaults RabbitMqDefaults

// log is for logging in this package.
var rabbitmqlog = logf.Log.WithName("rabbitmq-resource")

// SetupRabbitMqDefaults - initialize RabbitMq spec defaults for use with either internal or external webhooks
func SetupRabbitMqDefaults(defaults RabbitMqDefaults) {
	rabbitMqDefaults = defaults
	rabbitmqlog.Info("RabbitMq defaults initialized", "defaults", defaults)
}

// Default sets default values for the RabbitMq using the provided Kubernetes client
// to check if the cluster already exists
//
// NOTE: This function has a potential race condition (TOCTOU - Time Of Check Time Of Use).
// Between reading the existing resources and applying defaults, the state could change.
// We try to mitigate the risk by relying on the controller reconciliation loop and
// by checking multiple sources (CR spec, status, cluster).
// Complete prevention would require distributed locking, which is not practical for webhooks
func (r *RabbitMq) Default(k8sClient client.Client) {
	rabbitmqlog.Info("default", "name", r.Name, "namespace", r.Namespace)

	// Defensive checks - these should be guaranteed by the API server
	if r.Name == "" || r.Namespace == "" {
		rabbitmqlog.Error(nil, "invalid RabbitMq resource: name or namespace is empty",
			"name", r.Name, "namespace", r.Namespace)
		// Apply other defaults without QueueType logic
		r.Spec.Default(false)
		return
	}

	// shouldDefaultQueueType determines whether we should set QueueType to the default value (Quorum)
	// false means: either the user set it explicitly, or we found an existing deployment and must preserve its state
	shouldDefaultQueueType := true

	if k8sClient != nil {
		// Create a context with timeout to prevent hanging on slow API servers
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// First, check if the user has explicitly set QueueType in the incoming request
		// If so, preserve it and don't override with any default value
		if r.Spec.QueueType != nil && *r.Spec.QueueType != "" {
			rabbitmqlog.Info("preserving user-specified QueueType", "name", r.Name, "queueType", *r.Spec.QueueType)
			shouldDefaultQueueType = false
		} else {
			// User didn't set QueueType - check if existing RabbitMq CR has it set
			existingRabbitMq := &RabbitMq{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: r.Name, Namespace: r.Namespace,
			}, existingRabbitMq)

			if err != nil && !apierrors.IsNotFound(err) {
				// Error fetching existing RabbitMq CR (not a NotFound error)
				// Fail safe: don't default QueueType to avoid breaking existing deployments
				rabbitmqlog.Error(err, "failed to get existing RabbitMq CR, not defaulting QueueType for safety",
					"name", r.Name, "namespace", r.Namespace)
				shouldDefaultQueueType = false
				r.Spec.Default(shouldDefaultQueueType)
				return
			}

			// Check if we found an existing RabbitMq CR
			rabbitMqCRExists := (err == nil)

			if rabbitMqCRExists {
				// Existing RabbitMq CR found - check if it has QueueType set
				if existingRabbitMq.Spec.QueueType != nil && *existingRabbitMq.Spec.QueueType != "" {
					// Existing CR has QueueType set in Spec - preserve it
					// Create a new pointer to avoid aliasing
					queueType := *existingRabbitMq.Spec.QueueType
					r.Spec.QueueType = &queueType
					rabbitmqlog.Info("preserving QueueType from existing CR spec", "name", r.Name, "queueType", queueType)
					shouldDefaultQueueType = false
				} else if existingRabbitMq.Status.QueueType != "" {
					// Existing CR has QueueType set in Status but not in Spec
					// This can happen when:
					// - Old deployment where Spec.QueueType was never set (webhook didn't exist)
					// - Status.QueueType was set by controller based on actual cluster state
					// Preserve the Status.QueueType to avoid breaking existing deployments
					// by changing queue type (which would cause PRECONDITION_FAILED errors)
					statusQueueType := existingRabbitMq.Status.QueueType
					r.Spec.QueueType = &statusQueueType
					rabbitmqlog.Info("preserving QueueType from existing CR status (spec was empty)",
						"name", r.Name,
						"queueType", statusQueueType,
						"reason", "preventing queue type change on existing deployment")
					shouldDefaultQueueType = false
				}
				// If we get here with rabbitMqCRExists=true but no QueueType, fall through to check RabbitmqCluster
			}

			// If we haven't set QueueType yet (either no RabbitMq CR, or it exists but has no QueueType),
			// check if RabbitmqCluster exists (upgrade scenario)
			if shouldDefaultQueueType {
				rabbitmqlog.Info("checking for existing RabbitmqCluster to determine if this is an upgrade",
					"name", r.Name, "namespace", r.Namespace, "rabbitMqCRExists", rabbitMqCRExists)
				cluster := &rabbitmqv2.RabbitmqCluster{}
				clusterErr := k8sClient.Get(ctx, types.NamespacedName{
					Name: r.Name, Namespace: r.Namespace,
				}, cluster)

				if clusterErr != nil && !apierrors.IsNotFound(clusterErr) {
					// Error fetching RabbitmqCluster (not a NotFound error)
					// Fail safe: don't default QueueType to avoid breaking existing deployments
					rabbitmqlog.Error(clusterErr, "error checking for existing RabbitMQCluster, not defaulting QueueType for safety",
						"name", r.Name, "namespace", r.Namespace)
					shouldDefaultQueueType = false
				} else if clusterErr == nil && !cluster.CreationTimestamp.IsZero() {
					// RabbitmqCluster exists - this is an existing deployment, don't default
					shouldDefaultQueueType = false
					rabbitmqlog.Info("found existing RabbitMQCluster, not defaulting QueueType",
						"name", r.Name,
						"clusterCreationTimestamp", cluster.CreationTimestamp.String(),
						"reason", "existing rabbitmq clusters should never be touched")
				} else {
					// No existing RabbitmqCluster - this is truly a new deployment
					// shouldDefaultQueueType stays true, will default to Quorum in spec.Default()
					if clusterErr != nil {
						rabbitmqlog.Info("no existing RabbitmqCluster found, will default QueueType to Quorum",
							"name", r.Name, "namespace", r.Namespace, "error", clusterErr.Error())
					} else {
						rabbitmqlog.Info("new RabbitMq deployment, will default QueueType to Quorum", "name", r.Name)
					}
				}
			}
		}
	}

	// Apply other defaults (ContainerImage, etc.)
	r.Spec.Default(shouldDefaultQueueType)
}

// Default - set defaults for this RabbitMq spec
// shouldDefaultQueueType: if true, sets QueueType to "Quorum" when not already set
func (spec *RabbitMqSpec) Default(shouldDefaultQueueType bool) {
	if spec.ContainerImage == "" {
		spec.ContainerImage = rabbitMqDefaults.ContainerImageURL
	}
	spec.RabbitMqSpecCore.Default(shouldDefaultQueueType)
}

// Default - set defaults for this RabbitMqSpecCore
// shouldDefaultQueueType: if true, sets QueueType to "Quorum" when not already set
func (spec *RabbitMqSpecCore) Default(shouldDefaultQueueType bool) {
	if shouldDefaultQueueType && (spec.QueueType == nil || *spec.QueueType == "") {
		queueType := "Quorum"
		spec.QueueType = &queueType
	}
}

var _ webhook.Validator = &RabbitMq{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *RabbitMq) ValidateCreate() (admission.Warnings, error) {
	rabbitmqlog.Info("validate create", "name", r.Name)
	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	warn, errs := r.Spec.ValidateOverride(basePath, r.Namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)

	allErrs = append(allErrs, common_webhook.ValidateDNS1123Label(
		field.NewPath("metadata").Child("name"),
		[]string{r.Name},
		CrMaxLengthCorrection,
	)...) // omit issue with  statefulset pod label "controller-revision-hash": "<statefulset_name>-<hash>"

	// Validate QueueType if specified
	allErrs = append(allErrs, r.Spec.ValidateQueueType(basePath)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMq"},
			r.Name, allErrs,
		)
	}

	return allWarn, nil

}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *RabbitMq) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	rabbitmqlog.Info("validate update", "name", r.Name)

	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	warn, errs := r.Spec.ValidateOverride(basePath, r.Namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)

	// Validate QueueType if specified
	allErrs = append(allErrs, r.Spec.ValidateQueueType(basePath)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMq"},
			r.Name, allErrs,
		)
	}

	return allWarn, nil
}

// ValidateCreate performs validation when creating a new RabbitMqSpecCore.
func (spec *RabbitMqSpecCore) ValidateCreate(basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, spec.ValidateTopology(basePath, namespace)...)
	warn, errs := spec.ValidateOverride(basePath, namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)

	return allWarn, allErrs
}

// ValidateUpdate performs validation when updating an existing RabbitMqSpecCore.
func (spec *RabbitMqSpecCore) ValidateUpdate(_ RabbitMqSpecCore, basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, spec.ValidateTopology(basePath, namespace)...)
	warn, errs := spec.ValidateOverride(basePath, namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)

	return allWarn, allErrs
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *RabbitMq) ValidateDelete() (admission.Warnings, error) {
	rabbitmqlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}

// ValidateQueueType validates that QueueType is one of the allowed values
func (spec *RabbitMqSpec) ValidateQueueType(basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.QueueType != nil {
		allowedValues := []string{"None", "Mirrored", "Quorum"}
		isValid := false
		for _, allowed := range allowedValues {
			if *spec.QueueType == allowed {
				isValid = true
				break
			}
		}
		if !isValid {
			allErrs = append(allErrs, field.NotSupported(
				basePath.Child("queueType"),
				*spec.QueueType,
				allowedValues,
			))
		}
	}

	return allErrs
}
