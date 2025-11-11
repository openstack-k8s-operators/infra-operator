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

var rabbitmqvhostlog = logf.Log.WithName("rabbitmqvhost-resource")

// SetupRabbitMQVhostWebhookWithManager registers the webhook for RabbitMQVhost in the manager.
func SetupRabbitMQVhostWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&rabbitmqv1beta1.RabbitMQVhost{}).
		WithDefaulter(&RabbitMQVhostCustomDefaulter{
			Client: mgr.GetClient(),
		}).
		WithValidator(&RabbitMQVhostCustomValidator{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqvhost,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=create;update,versions=v1beta1,name=mrabbitmqvhost-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQVhostCustomDefaulter struct is responsible for setting default values on the RabbitMQVhost resource
// when it is created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQVhostCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &RabbitMQVhostCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type RabbitMQVhost.
func (d *RabbitMQVhostCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rabbitmqvhost, ok := obj.(*rabbitmqv1beta1.RabbitMQVhost)
	if !ok {
		return fmt.Errorf("expected a RabbitMQVhost object but got %T", obj)
	}
	rabbitmqvhostlog.Info("Defaulting for RabbitMQVhost", "name", rabbitmqvhost.GetName())

	rabbitmqvhost.Default(d.Client)
	return nil
}

// +kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqvhost,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=create;update,versions=v1beta1,name=vrabbitmqvhost-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQVhostCustomValidator struct is responsible for validating the RabbitMQVhost resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQVhostCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &RabbitMQVhostCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQVhost.
func (v *RabbitMQVhostCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqvhost, ok := obj.(*rabbitmqv1beta1.RabbitMQVhost)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQVhost object but got %T", obj)
	}
	rabbitmqvhostlog.Info("Validation for RabbitMQVhost upon creation", "name", rabbitmqvhost.GetName())

	return rabbitmqvhost.ValidateCreate(v.Client)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQVhost.
func (v *RabbitMQVhostCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rabbitmqvhost, ok := newObj.(*rabbitmqv1beta1.RabbitMQVhost)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQVhost object for the newObj but got %T", newObj)
	}
	rabbitmqvhostlog.Info("Validation for RabbitMQVhost upon update", "name", rabbitmqvhost.GetName())

	return rabbitmqvhost.ValidateUpdate(v.Client, oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQVhost.
func (v *RabbitMQVhostCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqvhost, ok := obj.(*rabbitmqv1beta1.RabbitMQVhost)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQVhost object but got %T", obj)
	}
	rabbitmqvhostlog.Info("Validation for RabbitMQVhost upon deletion", "name", rabbitmqvhost.GetName())

	return rabbitmqvhost.ValidateDelete(v.Client)
}
