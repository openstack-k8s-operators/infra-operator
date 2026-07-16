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
	"time"

	common_webhook "github.com/openstack-k8s-operators/lib-common/modules/common/webhook"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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

	if r.Name == "" || r.Namespace == "" {
		r.Spec.Default(true)
		return
	}

	// Determine if this is a new or existing CR to choose the right QueueType default
	isNew := true

	if k8sClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// If user explicitly set QueueType, preserve it
		if r.Spec.QueueType != nil && *r.Spec.QueueType != "" {
			rabbitmqlog.Info("preserving user-specified QueueType", "name", r.Name, "queueType", *r.Spec.QueueType)
			isNew = false // skip defaulting
		} else {
			// Look up existing CR to determine if this is adoption or new creation
			existingRabbitMq := &RabbitMq{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name: r.Name, Namespace: r.Namespace,
			}, existingRabbitMq)

			if err != nil && !apierrors.IsNotFound(err) {
				rabbitmqlog.Error(err, "failed to get existing RabbitMq CR, defaulting QueueType to Mirrored for safety",
					"name", r.Name, "namespace", r.Namespace)
				queueType := QueueTypeMirrored
				r.Spec.QueueType = &queueType
				isNew = false
			} else if err == nil {
				// Existing CR found - preserve its QueueType
				isNew = false
				if existingRabbitMq.Spec.QueueType != nil && *existingRabbitMq.Spec.QueueType != "" {
					queueType := *existingRabbitMq.Spec.QueueType
					r.Spec.QueueType = &queueType
					rabbitmqlog.Info("preserving QueueType from existing CR", "name", r.Name, "queueType", queueType)
				} else if existingRabbitMq.Status.QueueType != "" {
					statusQueueType := existingRabbitMq.Status.QueueType
					r.Spec.QueueType = &statusQueueType
					rabbitmqlog.Info("preserving QueueType from existing CR status", "name", r.Name, "queueType", statusQueueType)
				} else {
					// Existing deployment without QueueType: assume Mirrored
					queueType := QueueTypeMirrored
					r.Spec.QueueType = &queueType
					rabbitmqlog.Info("existing CR without QueueType, defaulting to Mirrored", "name", r.Name)
				}
			}
			// err is NotFound → isNew stays true, will default to Quorum
		}
	}

	r.Spec.Default(isNew)
}

// Default - set defaults for this RabbitMq spec
func (spec *RabbitMqSpec) Default(isNew bool) {
	if spec.ContainerImage == "" {
		spec.ContainerImage = rabbitMqDefaults.ContainerImageURL
	}
	spec.RabbitMqSpecCore.Default(isNew)
}

// Default - set defaults for this RabbitMqSpecCore and migrate from old format
func (spec *RabbitMqSpecCore) Default(isNew bool) {
	// TLS defaulting - set CaSecretName to SecretName if not explicitly set
	if spec.TLS.SecretName != "" && spec.TLS.CaSecretName == "" {
		spec.TLS.CaSecretName = spec.TLS.SecretName
	}

	// TLS defaulting - set DisableNonTLSListeners to true when TLS is enabled
	if spec.TLS.SecretName != "" {
		spec.TLS.DisableNonTLSListeners = true
	}

	// QueueType defaulting: Quorum for new clusters, preserved for existing
	if isNew && (spec.QueueType == nil || *spec.QueueType == "") {
		queueType := QueueTypeQuorum
		spec.QueueType = &queueType
	}

	// Migrate terminationGracePeriodSeconds from old default (604800) to new (60).
	// The old value was inherited from rabbitmq-cluster-operator and causes pods
	// to get stuck in Terminating state for up to a week.
	oldGracePeriod := int64(604800)
	newGracePeriod := int64(60)
	if spec.TerminationGracePeriodSeconds != nil && *spec.TerminationGracePeriodSeconds == oldGracePeriod {
		spec.TerminationGracePeriodSeconds = &newGracePeriod
		rabbitmqlog.Info("Migrating terminationGracePeriodSeconds from old default 604800 to 60")
	}

	// Force Mirrored → Quorum when upgrading to RabbitMQ 4.x+.
	// Mirrored queues are not supported in 4.x, so the migration is mandatory.
	if spec.QueueType != nil && *spec.QueueType == QueueTypeMirrored &&
		spec.TargetVersion != nil && *spec.TargetVersion != "" &&
		IsVersion4OrLater(*spec.TargetVersion) {
		queueType := QueueTypeQuorum
		spec.QueueType = &queueType
		rabbitmqlog.Info("Forcing QueueType from Mirrored to Quorum for RabbitMQ 4.x upgrade",
			"targetVersion", *spec.TargetVersion)
	}
}

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

	allErrs = append(allErrs, r.Spec.ValidateQueueType(basePath)...)

	if oldRabbitMq, ok := old.(*RabbitMq); ok {
		// Prevent version downgrades
		allErrs = append(allErrs, r.validateVersionChange(oldRabbitMq, basePath)...)

		// Reject scale-down: RabbitMQ does not support automatic node removal.
		// Scaling down requires manual steps (rabbitmqctl forget_cluster_node,
		// queue shrinking) and risks data loss or cluster failure.
		// See: https://github.com/rabbitmq/cluster-operator/issues/223
		allErrs = append(allErrs, r.validateScaleDown(oldRabbitMq, basePath)...)
	}

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMq"},
			r.Name, allErrs,
		)
	}

	return allWarn, nil
}

// validateVersionChange rejects version downgrades
func (r *RabbitMq) validateVersionChange(old *RabbitMq, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if r.Spec.TargetVersion == nil || *r.Spec.TargetVersion == "" {
		return allErrs
	}

	// Determine the current running version from old status or old spec
	currentVersion := old.Status.CurrentVersion
	if currentVersion == "" && old.Spec.TargetVersion != nil {
		currentVersion = *old.Spec.TargetVersion
	}
	if currentVersion == "" {
		return allErrs
	}

	current, err := ParseRabbitMQVersion(currentVersion)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("targetVersion"),
			*r.Spec.TargetVersion,
			fmt.Sprintf("cannot parse current version %q: %v", currentVersion, err),
		))
		return allErrs
	}
	target, err := ParseRabbitMQVersion(*r.Spec.TargetVersion)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("targetVersion"),
			*r.Spec.TargetVersion,
			fmt.Sprintf("cannot parse target version: %v", err),
		))
		return allErrs
	}

	if target.Major < current.Major ||
		(target.Major == current.Major && target.Minor < current.Minor) ||
		(target.Major == current.Major && target.Minor == current.Minor && target.Patch < current.Patch) {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("targetVersion"),
			*r.Spec.TargetVersion,
			fmt.Sprintf("version downgrades are not supported (current: %s)", currentVersion),
		))
	}

	return allErrs
}

// validateScaleDown rejects reducing the replica count.
// RabbitMQ does not support automatic node removal from a cluster.
// Scaling down a StatefulSet deletes pods without running
// rabbitmqctl forget_cluster_node or shrinking quorum queues,
// which can cause data loss or leave the cluster in a broken state.
func (r *RabbitMq) validateScaleDown(old *RabbitMq, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	oldReplicas := int32(1)
	if old.Spec.Replicas != nil {
		oldReplicas = *old.Spec.Replicas
	}
	newReplicas := int32(1)
	if r.Spec.Replicas != nil {
		newReplicas = *r.Spec.Replicas
	}

	if newReplicas < oldReplicas {
		allErrs = append(allErrs, field.Forbidden(
			basePath.Child("replicas"),
			fmt.Sprintf("scaling down from %d to %d replicas is not supported; "+
				"RabbitMQ requires manual node removal (rabbitmqctl forget_cluster_node) "+
				"before a node can be safely decommissioned",
				oldReplicas, newReplicas),
		))
	}

	return allErrs
}

// ValidateCreate performs validation when creating a new RabbitMqSpecCore.
func (spec *RabbitMqSpecCore) ValidateCreate(basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	allErrs = append(allErrs, spec.ValidateTopology(basePath, namespace)...)
	warn, errs := spec.ValidateOverride(basePath, namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)
	allErrs = append(allErrs, spec.ValidateQueueType(basePath)...)

	return allWarn, allErrs
}

// ValidateUpdate performs validation when updating an existing RabbitMqSpecCore.
func (spec *RabbitMqSpecCore) ValidateUpdate(old RabbitMqSpecCore, basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	allErrs = append(allErrs, spec.ValidateTopology(basePath, namespace)...)
	warn, errs := spec.ValidateOverride(basePath, namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)
	allErrs = append(allErrs, spec.ValidateQueueType(basePath)...)

	// Reject scale-down: RabbitMQ does not support automatic node removal.
	allErrs = append(allErrs, spec.validateScaleDown(old, basePath)...)

	// Reject version downgrades (spec-only check, uses old spec.TargetVersion as baseline)
	allErrs = append(allErrs, spec.validateVersionDowngrade(old, basePath)...)

	return allWarn, allErrs
}

// validateScaleDown rejects reducing the replica count at the SpecCore level.
func (spec *RabbitMqSpecCore) validateScaleDown(old RabbitMqSpecCore, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	oldReplicas := int32(1)
	if old.Replicas != nil {
		oldReplicas = *old.Replicas
	}
	newReplicas := int32(1)
	if spec.Replicas != nil {
		newReplicas = *spec.Replicas
	}

	if newReplicas < oldReplicas {
		allErrs = append(allErrs, field.Forbidden(
			basePath.Child("replicas"),
			fmt.Sprintf("scaling down from %d to %d replicas is not supported; "+
				"RabbitMQ requires manual node removal (rabbitmqctl forget_cluster_node) "+
				"before a node can be safely decommissioned",
				oldReplicas, newReplicas),
		))
	}

	return allErrs
}

// validateVersionDowngrade rejects version downgrades at the SpecCore level.
// This uses the old spec's TargetVersion as baseline since Status is not available.
func (spec *RabbitMqSpecCore) validateVersionDowngrade(old RabbitMqSpecCore, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if spec.TargetVersion == nil || *spec.TargetVersion == "" {
		return allErrs
	}

	// Use old spec's TargetVersion as the baseline
	if old.TargetVersion == nil || *old.TargetVersion == "" {
		return allErrs
	}

	current, err := ParseRabbitMQVersion(*old.TargetVersion)
	if err != nil {
		return allErrs // can't validate, allow
	}
	target, err := ParseRabbitMQVersion(*spec.TargetVersion)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("targetVersion"),
			*spec.TargetVersion,
			fmt.Sprintf("cannot parse target version: %v", err),
		))
		return allErrs
	}

	if target.Major < current.Major ||
		(target.Major == current.Major && target.Minor < current.Minor) ||
		(target.Major == current.Major && target.Minor == current.Minor && target.Patch < current.Patch) {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("targetVersion"),
			*spec.TargetVersion,
			fmt.Sprintf("version downgrades are not supported (current: %s)", *old.TargetVersion),
		))
	}

	return allErrs
}

// ValidateQueueType validates that QueueType is one of the allowed values
// and rejects Mirrored queues with RabbitMQ 4.x+ (which dropped mirrored queue support).
// The defaulting webhook forces Mirrored → Quorum for 4.x upgrades, so this
// validation acts as a safety net in case defaulting is bypassed.
func (spec *RabbitMqSpecCore) ValidateQueueType(basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.QueueType != nil {
		if *spec.QueueType != QueueTypeMirrored && *spec.QueueType != QueueTypeQuorum {
			allErrs = append(allErrs, field.NotSupported(
				basePath.Child("queueType"),
				string(*spec.QueueType),
				[]string{string(QueueTypeMirrored), string(QueueTypeQuorum)},
			))
		}
		if *spec.QueueType == QueueTypeMirrored && spec.TargetVersion != nil &&
			*spec.TargetVersion != "" && IsVersion4OrLater(*spec.TargetVersion) {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("queueType"),
				string(*spec.QueueType),
				fmt.Sprintf("Mirrored queues are not supported with RabbitMQ %s; use Quorum", *spec.TargetVersion),
			))
		}
	}
	return allErrs
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *RabbitMq) ValidateDelete() (admission.Warnings, error) {
	rabbitmqlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
