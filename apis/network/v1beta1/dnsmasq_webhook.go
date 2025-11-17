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
	"k8s.io/apimachinery/pkg/runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// DNSMasqDefaults -
type DNSMasqDefaults struct {
	ContainerImageURL string
}

var dnsMasqDefaults DNSMasqDefaults

// log is for logging in this package.
var dnsmasqlog = logf.Log.WithName("dnsmasq-resource")

// SetupDNSMasqDefaults - initialize DNSMasq spec defaults for use with either internal or external webhooks
func SetupDNSMasqDefaults(defaults DNSMasqDefaults) {
	dnsMasqDefaults = defaults
	dnsmasqlog.Info("DNSMasq defaults initialized", "defaults", defaults)
}

var _ webhook.Defaulter = &DNSMasq{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *DNSMasq) Default() {
	dnsmasqlog.Info("default", "name", r.Name)

	r.Spec.Default()
}

// Default - set defaults for this DNSMasq spec
func (spec *DNSMasqSpec) Default() {
	if spec.ContainerImage == "" {
		spec.ContainerImage = dnsMasqDefaults.ContainerImageURL
	}
	spec.DNSMasqSpecCore.Default()
}

// Default - common defaults go here (for the OpenStackControlplane which uses this one)
func (spec *DNSMasqSpecCore) Default() {
	// nothing here
}

var _ webhook.Validator = &DNSMasq{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *DNSMasq) ValidateCreate() (admission.Warnings, error) {
	dnsmasqlog.Info("validate create", "name", r.Name)

	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "network.openstack.org", Kind: "DNSMasq"},
			r.Name, allErrs)
	}
	return allWarn, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *DNSMasq) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	dnsmasqlog.Info("validate update", "name", r.Name)

	var allErrs field.ErrorList
	var allWarn []string
	basePath := field.NewPath("spec")

	// When a TopologyRef CR is referenced, fail if a different Namespace is
	// referenced because is not supported
	allErrs = append(allErrs, r.Spec.ValidateTopology(basePath, r.Namespace)...)

	if len(allErrs) != 0 {
		return allWarn, apierrors.NewInvalid(
			schema.GroupKind{Group: "network.openstack.org", Kind: "DNSMasq"},
			r.Name, allErrs)
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *DNSMasq) ValidateDelete() (admission.Warnings, error) {
	dnsmasqlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
