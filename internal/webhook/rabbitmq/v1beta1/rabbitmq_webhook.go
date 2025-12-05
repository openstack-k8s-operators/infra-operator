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

// Package v1beta1 contains API Schema definitions for the rabbitmq v1beta1 API group
package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var rabbitmqlog = logf.Log.WithName("rabbitmq-resource")

// SetupRabbitMqWebhookWithManager registers the webhook for RabbitMq in the manager.
func SetupRabbitMqWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&rabbitmqv1beta1.RabbitMq{}).
		WithValidator(&RabbitMqCustomValidator{}).
		WithDefaulter(&RabbitMqCustomDefaulter{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmq,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=create;update,versions=v1beta1,name=mrabbitmq-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMqCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind RabbitMq when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMqCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &RabbitMqCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind RabbitMq.
func (d *RabbitMqCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rabbitmq, ok := obj.(*rabbitmqv1beta1.RabbitMq)

	if !ok {
		return fmt.Errorf("expected an RabbitMq object but got %T", obj)
	}
	rabbitmqlog.Info("Defaulting for RabbitMq", "name", rabbitmq.GetName())

	rabbitmq.Default(d.Client)

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmq,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqs,verbs=create;update,versions=v1beta1,name=vrabbitmq-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMqCustomValidator struct is responsible for validating the RabbitMq resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMqCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &RabbitMqCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMq.
func (v *RabbitMqCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmq, ok := obj.(*rabbitmqv1beta1.RabbitMq)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMq object but got %T", obj)
	}
	rabbitmqlog.Info("Validation for RabbitMq upon creation", "name", rabbitmq.GetName())

	return rabbitmq.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMq.
func (v *RabbitMqCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rabbitmq, ok := newObj.(*rabbitmqv1beta1.RabbitMq)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMq object for the newObj but got %T", newObj)
	}
	rabbitmqlog.Info("Validation for RabbitMq upon update", "name", rabbitmq.GetName())

	return rabbitmq.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type RabbitMq.
func (v *RabbitMqCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmq, ok := obj.(*rabbitmqv1beta1.RabbitMq)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMq object but got %T", obj)
	}
	rabbitmqlog.Info("Validation for RabbitMq upon deletion", "name", rabbitmq.GetName())

	return rabbitmq.ValidateDelete()
}
