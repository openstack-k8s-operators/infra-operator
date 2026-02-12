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
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
)

var federationlog = logf.Log.WithName("rabbitmqfederation-resource")

// SetupRabbitMQFederationWebhookWithManager registers the webhook for RabbitMQFederation in the manager.
func SetupRabbitMQFederationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&rabbitmqv1beta1.RabbitMQFederation{}).
		WithDefaulter(&RabbitMQFederationCustomDefaulter{
			Client: mgr.GetClient(),
		}).
		WithValidator(&RabbitMQFederationCustomValidator{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqfederation,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqfederations,verbs=create;update,versions=v1beta1,name=mrabbitmqfederation-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQFederationCustomDefaulter struct is responsible for setting default values on the RabbitMQFederation resource
// when it is created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQFederationCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &RabbitMQFederationCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type RabbitMQFederation.
func (d *RabbitMQFederationCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rabbitmqfederation, ok := obj.(*rabbitmqv1beta1.RabbitMQFederation)
	if !ok {
		return fmt.Errorf("expected a RabbitMQFederation object but got %T", obj)
	}
	federationlog.Info("Defaulting for RabbitMQFederation", "name", rabbitmqfederation.GetName())

	rabbitmqfederation.Default(d.Client)
	return nil
}

// +kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqfederation,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqfederations,verbs=create;update,versions=v1beta1,name=vrabbitmqfederation-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQFederationCustomValidator struct is responsible for validating the RabbitMQFederation resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQFederationCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &RabbitMQFederationCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQFederation.
func (v *RabbitMQFederationCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqfederation, ok := obj.(*rabbitmqv1beta1.RabbitMQFederation)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQFederation object but got %T", obj)
	}
	federationlog.Info("Validation for RabbitMQFederation upon creation", "name", rabbitmqfederation.GetName())

	return rabbitmqfederation.ValidateCreate(v.Client)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQFederation.
func (v *RabbitMQFederationCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rabbitmqfederation, ok := newObj.(*rabbitmqv1beta1.RabbitMQFederation)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQFederation object for the newObj but got %T", newObj)
	}
	federationlog.Info("Validation for RabbitMQFederation upon update", "name", rabbitmqfederation.GetName())

	return rabbitmqfederation.ValidateUpdate(v.Client, oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQFederation.
func (v *RabbitMQFederationCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqfederation, ok := obj.(*rabbitmqv1beta1.RabbitMQFederation)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQFederation object but got %T", obj)
	}
	federationlog.Info("Validation for RabbitMQFederation upon deletion", "name", rabbitmqfederation.GetName())

	return rabbitmqfederation.ValidateDelete(v.Client)
}
