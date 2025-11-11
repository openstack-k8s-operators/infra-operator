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

var log = logf.Log.WithName("rabbitmquser-resource")

// SetupRabbitMQUserWebhookWithManager registers the webhook for RabbitMQUser in the manager.
func SetupRabbitMQUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&rabbitmqv1beta1.RabbitMQUser{}).
		WithDefaulter(&RabbitMQUserCustomDefaulter{
			Client: mgr.GetClient(),
		}).
		WithValidator(&RabbitMQUserCustomValidator{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmquser,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=create;update,versions=v1beta1,name=mrabbitmquser-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQUserCustomDefaulter struct is responsible for setting default values on the RabbitMQUser resource
// when it is created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQUserCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &RabbitMQUserCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type RabbitMQUser.
func (d *RabbitMQUserCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rabbitmquser, ok := obj.(*rabbitmqv1beta1.RabbitMQUser)
	if !ok {
		return fmt.Errorf("expected a RabbitMQUser object but got %T", obj)
	}
	log.Info("Defaulting for RabbitMQUser", "name", rabbitmquser.GetName())

	rabbitmquser.Default(d.Client)
	return nil
}

// +kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmquser,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=create;update,versions=v1beta1,name=vrabbitmquser-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQUserCustomValidator struct is responsible for validating the RabbitMQUser resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQUserCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &RabbitMQUserCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQUser.
func (v *RabbitMQUserCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmquser, ok := obj.(*rabbitmqv1beta1.RabbitMQUser)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQUser object but got %T", obj)
	}
	log.Info("Validation for RabbitMQUser upon creation", "name", rabbitmquser.GetName())

	return rabbitmquser.ValidateCreate(v.Client)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQUser.
func (v *RabbitMQUserCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rabbitmquser, ok := newObj.(*rabbitmqv1beta1.RabbitMQUser)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQUser object for the newObj but got %T", newObj)
	}
	log.Info("Validation for RabbitMQUser upon update", "name", rabbitmquser.GetName())

	return rabbitmquser.ValidateUpdate(v.Client, oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQUser.
func (v *RabbitMQUserCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmquser, ok := obj.(*rabbitmqv1beta1.RabbitMQUser)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQUser object but got %T", obj)
	}
	log.Info("Validation for RabbitMQUser upon deletion", "name", rabbitmquser.GetName())

	return rabbitmquser.ValidateDelete(v.Client)
}
