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
	"fmt"
	"strconv"
	"strings"

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

// parseVersionMajor extracts the major version number from a version string
// Returns the major version and an error if parsing fails
func parseVersionMajor(version string) (int, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid version format: %s", version)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid major version: %s", parts[0])
	}

	return major, nil
}

// Default sets default values for the RabbitMq using the provided Kubernetes client
// to check if the cluster already exists
func (r *RabbitMq) Default(k8sClient client.Client) {
	rabbitmqlog.Info("default", "name", r.Name)

	isNew := true

	if k8sClient != nil {
		// First check if existing RabbitMq CR has QueueType set - preserve it across updates
		existingRabbitMq := &RabbitMq{}
		err := k8sClient.Get(context.Background(), types.NamespacedName{
			Name: r.Name, Namespace: r.Namespace,
		}, existingRabbitMq)

		if err != nil && !apierrors.IsNotFound(err) {
			// Fail the webhook to avoid making incorrect defaulting decisions
			rabbitmqlog.Error(err, "failed to get existing RabbitMq CR", "name", r.Name, "namespace", r.Namespace)
			panic("cannot determine if RabbitMq resource is new or existing due to API error")
		}

		if err == nil && existingRabbitMq.Spec.QueueType != nil {
			// Check if we should override Mirrored to Quorum on RabbitMQ 4.2+
			// Only override if the queueType is NOT being explicitly CHANGED to Mirrored
			shouldOverride := false
			if *existingRabbitMq.Spec.QueueType == "Mirrored" {
				// Kubernetes fills in all spec fields during updates, so we need to detect
				// if the user is explicitly CHANGING to Mirrored (from something else)
				// vs just preserving the existing Mirrored value
				userChangingToMirrored := r.Spec.QueueType != nil &&
					*r.Spec.QueueType == "Mirrored" &&
					*existingRabbitMq.Spec.QueueType != "Mirrored"

				if !userChangingToMirrored {
					// User is not explicitly changing TO Mirrored, so we can auto-override
					// Check the target version (annotation) to see if upgrading to 4.2+
					if r.Annotations != nil {
						if targetVersion, hasTarget := r.Annotations[AnnotationTargetVersion]; hasTarget {
							// Parse version to check if major version is 4 or higher
							majorVersion, err := parseVersionMajor(targetVersion)
							if err == nil && majorVersion >= 4 {
								shouldOverride = true
								queueType := "Quorum"
								r.Spec.QueueType = &queueType
								rabbitmqlog.Info("overriding Mirrored to Quorum on RabbitMQ 4.2+",
									"name", r.Name,
									"targetVersion", targetVersion,
									"queueType", "Quorum")
							}
						}
					}
				}
			}

			// Only preserve existing queueType if we didn't override above
			if !shouldOverride {
				// Preserve existing queueType if the incoming request doesn't specify one
				// This allows operators to explicitly change the queueType for migration purposes
				if r.Spec.QueueType == nil || *r.Spec.QueueType == "" {
					r.Spec.QueueType = existingRabbitMq.Spec.QueueType
					rabbitmqlog.Info("preserving QueueType from existing CR", "name", r.Name, "queueType", *r.Spec.QueueType)
				} else if *r.Spec.QueueType != *existingRabbitMq.Spec.QueueType {
					// User is explicitly changing queueType - allow it to proceed to validation
					rabbitmqlog.Info("allowing queueType change",
						"name", r.Name,
						"oldQueueType", *existingRabbitMq.Spec.QueueType,
						"newQueueType", *r.Spec.QueueType)
				}
			}

			isNew = false
		} else {
			// Check if RabbitMQCluster exists (upgrade scenario: cluster exists but CR is new)
			cluster := &rabbitmqv2.RabbitmqCluster{}
			err = k8sClient.Get(context.Background(), types.NamespacedName{
				Name: r.Name, Namespace: r.Namespace,
			}, cluster)

			if err != nil && !apierrors.IsNotFound(err) {
				// Fail the webhook to avoid making incorrect defaulting decisions
				rabbitmqlog.Error(err, "failed to get existing RabbitMQCluster", "name", r.Name, "namespace", r.Namespace)
				panic("cannot determine if RabbitMQCluster is new or existing due to API error")
			}

			if err == nil && !cluster.CreationTimestamp.IsZero() {
				isNew = false
				rabbitmqlog.Info("found existing RabbitMQCluster", "name", r.Name, "creationTimestamp", cluster.CreationTimestamp.String())
			}
		}
	}

	r.Spec.Default(isNew)

	// For updates (isNew == false), also apply version-aware defaults
	// This handles cases where existing CRs don't have queueType set
	if !isNew {
		if r.Annotations != nil {
			if targetVersion, hasTarget := r.Annotations[AnnotationTargetVersion]; hasTarget {
				r.Spec.RabbitMqSpecCore.DefaultForUpdate(targetVersion)
			}
		}
	}
}

// Default - set defaults for this RabbitMq spec
func (spec *RabbitMqSpec) Default(isNew bool) {
	if spec.ContainerImage == "" {
		spec.ContainerImage = rabbitMqDefaults.ContainerImageURL
	}
	spec.RabbitMqSpecCore.Default(isNew)
}

// Default - set defaults for this RabbitMqSpecCore
func (spec *RabbitMqSpecCore) Default(isNew bool) {
	// Default QueueType for new instances
	if isNew && (spec.QueueType == nil || *spec.QueueType == "") {
		queueType := "Quorum"
		spec.QueueType = &queueType
	}
}

// DefaultForUpdate - set defaults for RabbitMqSpecCore during updates
// This is called when updating existing CRs
// For RabbitMQ 4.2+, we enforce Quorum queues (override Mirrored if present)
func (spec *RabbitMqSpecCore) DefaultForUpdate(targetVersion string) {
	if targetVersion == "" {
		return
	}

	majorVersion, err := parseVersionMajor(targetVersion)
	if err != nil || majorVersion < 4 {
		return
	}

	// For RabbitMQ 4.2+:
	// 1. If queueType is not set, default to Quorum
	// 2. If queueType is Mirrored, override to Quorum (mirrored queues removed in 4.2)
	if spec.QueueType == nil || *spec.QueueType == "" || *spec.QueueType == "Mirrored" {
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
func (r *RabbitMq) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
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

	// Note: We don't block queueType: Mirrored on RabbitMQ 4.x here because:
	// 1. Default() function automatically overrides Mirrored → Quorum when target-version is 4.2+
	// 2. DefaultForUpdate() handles cases where queueType is not explicitly set
	// 3. Controller also enforces Quorum queues on RabbitMQ 4.x upgrades
	// 4. RabbitMQ server will reject mirrored queues on 4.2+ if they somehow get through
	oldRabbitMq := old.(*RabbitMq)

	// Block migrating TO Quorum on RabbitMQ 3.x (no server-side enforcement available)
	// Allow creating new clusters with Quorum on 3.x, only block migrations
	// UNLESS there's a concurrent version upgrade to 4.x (which will wipe storage)
	if r.Spec.QueueType != nil && *r.Spec.QueueType == "Quorum" {
		// Check if queueType is being changed TO Quorum (wasn't Quorum before)
		queueTypeChanged := oldRabbitMq.Spec.QueueType != nil && *oldRabbitMq.Spec.QueueType != "Quorum"

		if queueTypeChanged {
			// Check current running version from Status (controller-managed)
			currentVersion := oldRabbitMq.Status.CurrentVersion
			if currentVersion != "" {
				// Parse version - if major version is 3.x, check if upgrading
				majorVersion, err := parseVersionMajor(currentVersion)
				if err == nil && majorVersion == 3 {
					// Check if there's a concurrent version upgrade to 4.x via annotation
					isUpgradingTo4x := false
					if r.Annotations != nil {
						if targetVersion, hasTarget := r.Annotations[AnnotationTargetVersion]; hasTarget {
							targetMajor, err := parseVersionMajor(targetVersion)
							if err == nil && targetMajor >= 4 {
								isUpgradingTo4x = true
							}
						}
					}

					// Only block if NOT upgrading to 4.x
					if !isUpgradingTo4x {
						allErrs = append(allErrs, field.Forbidden(
							basePath.Child("queueType"),
							"Migrating to Quorum queues on RabbitMQ 3.x is not supported due to lack of server-side enforcement. "+
								"Upgrade to RabbitMQ 4.x first to enable automatic Quorum queue migration."))
					}
				}
			}
		}
	}

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
