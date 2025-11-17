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
var netconfiglog = logf.Log.WithName("netconfig-resource")

// SetupNetConfigWebhookWithManager registers the webhook for NetConfig in the manager.
func SetupNetConfigWebhookWithManager(mgr ctrl.Manager) error {
	// Set the webhook client for use in validation functions
	if err := networkv1beta1.SetWebhookClient(mgr.GetClient()); err != nil {
		return err
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&networkv1beta1.NetConfig{}).
		WithValidator(&NetConfigCustomValidator{}).
		WithDefaulter(&NetConfigCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-network-openstack-org-v1beta1-netconfig,mutating=true,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=netconfigs,verbs=create;update,versions=v1beta1,name=mnetconfig-v1beta1.kb.io,admissionReviewVersions=v1

// NetConfigCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind NetConfig when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type NetConfigCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &NetConfigCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind NetConfig.
func (d *NetConfigCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	netconfig, ok := obj.(*networkv1beta1.NetConfig)

	if !ok {
		return fmt.Errorf("expected an NetConfig object but got %T", obj)
	}
	netconfiglog.Info("Defaulting for NetConfig", "name", netconfig.GetName())

	netconfig.Default()

	return nil
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-network-openstack-org-v1beta1-netconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=network.openstack.org,resources=netconfigs,verbs=create;update;delete,versions=v1beta1,name=vnetconfig-v1beta1.kb.io,admissionReviewVersions=v1

// NetConfigCustomValidator struct is responsible for validating the NetConfig resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NetConfigCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &NetConfigCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type NetConfig.
func (v *NetConfigCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	netconfig, ok := obj.(*networkv1beta1.NetConfig)
	if !ok {
		return nil, fmt.Errorf("expected a NetConfig object but got %T", obj)
	}
	netconfiglog.Info("Validation for NetConfig upon creation", "name", netconfig.GetName())

	return netconfig.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type NetConfig.
func (v *NetConfigCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	netconfig, ok := newObj.(*networkv1beta1.NetConfig)
	if !ok {
		return nil, fmt.Errorf("expected a NetConfig object for the newObj but got %T", newObj)
	}
	netconfiglog.Info("Validation for NetConfig upon update", "name", netconfig.GetName())

	return netconfig.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type NetConfig.
func (v *NetConfigCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	netconfig, ok := obj.(*networkv1beta1.NetConfig)
	if !ok {
		return nil, fmt.Errorf("expected a NetConfig object but got %T", obj)
	}
	netconfiglog.Info("Validation for NetConfig upon deletion", "name", netconfig.GetName())

	return netconfig.ValidateDelete()
}
