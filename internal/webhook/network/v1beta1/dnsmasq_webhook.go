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

// Package v1beta1 contains API Schema definitions for the network v1beta1 API group
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
var dnsmasqlog = logf.Log.WithName("dnsmasq-resource")

// SetupDNSMasqWebhookWithManager registers the webhook for DNSMasq in the manager.
func SetupDNSMasqWebhookWithManager(mgr ctrl.Manager) error {
	// Set the webhook client for use in validation functions
	if err := networkv1beta1.SetWebhookClient(mgr.GetClient()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&networkv1beta1.DNSMasq{}).
		WithValidator(&DNSMasqCustomValidator{}).
		WithDefaulter(&DNSMasqCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-network-openstack-org-v1beta1-dnsmasq,mutating=true,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=dnsmasqs,verbs=create;update,versions=v1beta1,name=mdnsmasq-v1beta1.kb.io,admissionReviewVersions=v1

// DNSMasqCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind DNSMasq when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type DNSMasqCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &DNSMasqCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind DNSMasq.
func (d *DNSMasqCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	dnsmasq, ok := obj.(*networkv1beta1.DNSMasq)

	if !ok {
		return fmt.Errorf("expected an DNSMasq object but got %T", obj)
	}
	dnsmasqlog.Info("Defaulting for DNSMasq", "name", dnsmasq.GetName())

	dnsmasq.Default()

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-dnsmasq,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=dnsmasqs,verbs=create;update,versions=v1beta1,name=vdnsmasq-v1beta1.kb.io,admissionReviewVersions=v1

// DNSMasqCustomValidator struct is responsible for validating the DNSMasq resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type DNSMasqCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &DNSMasqCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type DNSMasq.
func (v *DNSMasqCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	dnsmasq, ok := obj.(*networkv1beta1.DNSMasq)
	if !ok {
		return nil, fmt.Errorf("expected a DNSMasq object but got %T", obj)
	}
	dnsmasqlog.Info("Validation for DNSMasq upon creation", "name", dnsmasq.GetName())

	return dnsmasq.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type DNSMasq.
func (v *DNSMasqCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	dnsmasq, ok := newObj.(*networkv1beta1.DNSMasq)
	if !ok {
		return nil, fmt.Errorf("expected a DNSMasq object for the newObj but got %T", newObj)
	}
	dnsmasqlog.Info("Validation for DNSMasq upon update", "name", dnsmasq.GetName())

	return dnsmasq.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type DNSMasq.
func (v *DNSMasqCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	dnsmasq, ok := obj.(*networkv1beta1.DNSMasq)
	if !ok {
		return nil, fmt.Errorf("expected a DNSMasq object but got %T", obj)
	}
	dnsmasqlog.Info("Validation for DNSMasq upon deletion", "name", dnsmasq.GetName())

	return dnsmasq.ValidateDelete()
}
