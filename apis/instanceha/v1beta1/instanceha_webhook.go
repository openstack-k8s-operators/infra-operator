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

//
// Generated by:
//
// operator-sdk create webhook --group client --version v1beta1 --kind InstanceHa --programmatic-validation --defaulting
//

package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// InstanceHaDefaults -
type InstanceHaDefaults struct {
	ContainerImageURL string
}

var instanceHaDefaults InstanceHaDefaults

// log is for logging in this package.
var instancehalog = logf.Log.WithName("instanceha-resource")

// SetupInstanceHaDefaults - initialize InstanceHa spec defaults for use with either internal or external webhooks
func SetupInstanceHaDefaults(defaults InstanceHaDefaults) {
	instanceHaDefaults = defaults
	instancehalog.Info("InstanceHa defaults initialized", "defaults", defaults)
}

// SetupWebhookWithManager sets up the webhook with the Manager
func (r *InstanceHa) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-client-openstack-org-v1beta1-instanceha,mutating=true,failurePolicy=fail,sideEffects=None,groups=client.openstack.org,resources=instancehas,verbs=create;update,versions=v1beta1,name=minstanceha.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &InstanceHa{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *InstanceHa) Default() {
	instancehalog.Info("default", "name", r.Name)

	r.Spec.Default()
}

// Default - set defaults for this InstanceHa spec
func (spec *InstanceHaSpec) Default() {
	if spec.ContainerImage == "" {
		spec.ContainerImage = instanceHaDefaults.ContainerImageURL
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
//+kubebuilder:webhook:path=/validate-client-openstack-org-v1beta1-instanceha,mutating=false,failurePolicy=fail,sideEffects=None,groups=client.openstack.org,resources=instancehas,verbs=create;update,versions=v1beta1,name=vinstanceha.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &InstanceHa{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *InstanceHa) ValidateCreate() (admission.Warnings, error) {
	instancehalog.Info("validate create", "name", r.Name)

	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "instanceha.openstack.org", Kind: "InstanceHa"},
			r.Name, allErrs)
	}
	return allWarn, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *InstanceHa) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	instancehalog.Info("validate update", "name", r.Name)

	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "instanceha.openstack.org", Kind: "InstanceHa"},
			r.Name, allErrs)
	}
	return allWarn, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *InstanceHa) ValidateDelete() (admission.Warnings, error) {
	instancehalog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
