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
	common_webhook "github.com/openstack-k8s-operators/lib-common/modules/common/webhook"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
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

// SetupWebhookWithManager sets up the webhook with the Manager
func (r *RabbitMq) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmq,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=create;update,versions=v1beta1,name=mrabbitmq.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &RabbitMq{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *RabbitMq) Default() {
	rabbitmqlog.Info("default", "name", r.Name)

	r.Spec.Default()
}

// Default - set defaults for this RabbitMq spec
func (spec *RabbitMqSpec) Default() {
	if spec.ContainerImage == "" {
		spec.ContainerImage = rabbitMqDefaults.ContainerImageURL
	}
	spec.RabbitMqSpecCore.Default()
}

// Default - common validations go here (for the OpenStackControlplane which uses this one)
func (spec *RabbitMqSpecCore) Default() {
	// Set a sensible default for QueueType only when not explicitly provided
	if spec.QueueType == nil || *spec.QueueType == "" {
		queueType := "Quorum"
		spec.QueueType = &queueType
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmq,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=create;update,versions=v1beta1,name=vrabbitmq.kb.io,admissionReviewVersions=v1

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

func (r *RabbitMqSpecCore) ValidateCreate(basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.ValidateTopology(basePath, namespace)...)
	warn, errs := r.ValidateOverride(basePath, namespace)
	allWarn = append(allWarn, warn...)
	allErrs = append(allErrs, errs...)

	return allWarn, allErrs
}

func (r *RabbitMqSpecCore) ValidateUpdate(old RabbitMqSpecCore, basePath *field.Path, namespace string) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn []string

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.ValidateTopology(basePath, namespace)...)
	warn, errs := r.ValidateOverride(basePath, namespace)
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
