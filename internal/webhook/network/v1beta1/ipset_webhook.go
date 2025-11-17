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

package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	networkv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var ipsetlog = logf.Log.WithName("ipset-resource")

// SetupIPSetWebhookWithManager registers the webhook for IPSet in the manager.
func SetupIPSetWebhookWithManager(mgr ctrl.Manager) error {
	// Set the webhook client for use in validation functions
	if err := networkv1beta1.SetWebhookClient(mgr.GetClient()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&networkv1beta1.IPSet{}).
		WithValidator(&IPSetCustomValidator{}).
		WithDefaulter(&IPSetCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-network-openstack-org-v1beta1-ipset,mutating=true,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=ipsets,verbs=create;update,versions=v1beta1,name=mipset-v1beta1.kb.io,admissionReviewVersions=v1

// IPSetCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind IPSet when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type IPSetCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &IPSetCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind IPSet.
func (d *IPSetCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	ipset, ok := obj.(*networkv1beta1.IPSet)

	if !ok {
		return fmt.Errorf("expected an IPSet object but got %T", obj)
	}
	ipsetlog.Info("Defaulting for IPSet", "name", ipset.GetName())

	ipset.Default()

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-ipset,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=ipsets,verbs=create;update,versions=v1beta1,name=vipset-v1beta1.kb.io,admissionReviewVersions=v1

// IPSetCustomValidator struct is responsible for validating the IPSet resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type IPSetCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &IPSetCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type IPSet.
func (v *IPSetCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	ipset, ok := obj.(*networkv1beta1.IPSet)
	if !ok {
		return nil, fmt.Errorf("expected a IPSet object but got %T", obj)
	}
	ipsetlog.Info("Validation for IPSet upon creation", "name", ipset.GetName())

	return ipset.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type IPSet.
func (v *IPSetCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	ipset, ok := newObj.(*networkv1beta1.IPSet)
	if !ok {
		return nil, fmt.Errorf("expected a IPSet object for the newObj but got %T", newObj)
	}
	ipsetlog.Info("Validation for IPSet upon update", "name", ipset.GetName())

	return ipset.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type IPSet.
func (v *IPSetCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	ipset, ok := obj.(*networkv1beta1.IPSet)
	if !ok {
		return nil, fmt.Errorf("expected a IPSet object but got %T", obj)
	}
	ipsetlog.Info("Validation for IPSet upon deletion", "name", ipset.GetName())

	return ipset.ValidateDelete()
}
