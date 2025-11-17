/*
Copyright 2025.

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

// Package v1beta1 contains the webhook implementation for InstanceHa resources.
package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	instancehav1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var instancehalog = logf.Log.WithName("instanceha-resource")

// SetupInstanceHaWebhookWithManager registers the webhook for InstanceHa in the manager.
func SetupInstanceHaWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&instancehav1beta1.InstanceHa{}).
		WithValidator(&InstanceHaCustomValidator{}).
		WithDefaulter(&InstanceHaCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-instanceha-openstack-org-v1beta1-instanceha,mutating=true,failurePolicy=fail,sideEffects=None,groups=instanceha.openstack.org,resources=instancehas,verbs=create;update,versions=v1beta1,name=minstanceha-v1beta1.kb.io,admissionReviewVersions=v1

// InstanceHaCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind InstanceHa when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type InstanceHaCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &InstanceHaCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind InstanceHa.
func (d *InstanceHaCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	instanceha, ok := obj.(*instancehav1beta1.InstanceHa)

	if !ok {
		return fmt.Errorf("expected an InstanceHa object but got %T", obj)
	}
	instancehalog.Info("Defaulting for InstanceHa", "name", instanceha.GetName())

	instanceha.Default()

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-instanceha-openstack-org-v1beta1-instanceha,mutating=false,failurePolicy=fail,sideEffects=None,groups=instanceha.openstack.org,resources=instancehas,verbs=create;update,versions=v1beta1,name=vinstanceha-v1beta1.kb.io,admissionReviewVersions=v1

// InstanceHaCustomValidator struct is responsible for validating the InstanceHa resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type InstanceHaCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &InstanceHaCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type InstanceHa.
func (v *InstanceHaCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	instanceha, ok := obj.(*instancehav1beta1.InstanceHa)
	if !ok {
		return nil, fmt.Errorf("expected a InstanceHa object but got %T", obj)
	}
	instancehalog.Info("Validation for InstanceHa upon creation", "name", instanceha.GetName())

	return instanceha.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type InstanceHa.
func (v *InstanceHaCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	instanceha, ok := newObj.(*instancehav1beta1.InstanceHa)
	if !ok {
		return nil, fmt.Errorf("expected a InstanceHa object for the newObj but got %T", newObj)
	}
	instancehalog.Info("Validation for InstanceHa upon update", "name", instanceha.GetName())

	return instanceha.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type InstanceHa.
func (v *InstanceHaCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	instanceha, ok := obj.(*instancehav1beta1.InstanceHa)
	if !ok {
		return nil, fmt.Errorf("expected a InstanceHa object but got %T", obj)
	}
	instancehalog.Info("Validation for InstanceHa upon deletion", "name", instanceha.GetName())

	return instanceha.ValidateDelete()
}
