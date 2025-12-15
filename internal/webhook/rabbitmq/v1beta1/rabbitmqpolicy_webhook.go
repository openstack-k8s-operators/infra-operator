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

var policylog = logf.Log.WithName("rabbitmqpolicy-resource")

// SetupRabbitMQPolicyWebhookWithManager registers the webhook for RabbitMQPolicy in the manager.
func SetupRabbitMQPolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&rabbitmqv1beta1.RabbitMQPolicy{}).
		WithDefaulter(&RabbitMQPolicyCustomDefaulter{
			Client: mgr.GetClient(),
		}).
		WithValidator(&RabbitMQPolicyCustomValidator{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqpolicy,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqpolicies,verbs=create;update,versions=v1beta1,name=mrabbitmqpolicy-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQPolicyCustomDefaulter struct is responsible for setting default values on the RabbitMQPolicy resource
// when it is created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQPolicyCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &RabbitMQPolicyCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type RabbitMQPolicy.
func (d *RabbitMQPolicyCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rabbitmqpolicy, ok := obj.(*rabbitmqv1beta1.RabbitMQPolicy)
	if !ok {
		return fmt.Errorf("expected a RabbitMQPolicy object but got %T", obj)
	}
	policylog.Info("Defaulting for RabbitMQPolicy", "name", rabbitmqpolicy.GetName())

	rabbitmqpolicy.Default(d.Client)
	return nil
}

// +kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqpolicies,verbs=create;update,versions=v1beta1,name=vrabbitmqpolicy-v1beta1.kb.io,admissionReviewVersions=v1

// RabbitMQPolicyCustomValidator struct is responsible for validating the RabbitMQPolicy resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type RabbitMQPolicyCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &RabbitMQPolicyCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQPolicy.
func (v *RabbitMQPolicyCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqpolicy, ok := obj.(*rabbitmqv1beta1.RabbitMQPolicy)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQPolicy object but got %T", obj)
	}
	policylog.Info("Validation for RabbitMQPolicy upon creation", "name", rabbitmqpolicy.GetName())

	return rabbitmqpolicy.ValidateCreate(v.Client)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQPolicy.
func (v *RabbitMQPolicyCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rabbitmqpolicy, ok := newObj.(*rabbitmqv1beta1.RabbitMQPolicy)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQPolicy object for the newObj but got %T", newObj)
	}
	policylog.Info("Validation for RabbitMQPolicy upon update", "name", rabbitmqpolicy.GetName())

	return rabbitmqpolicy.ValidateUpdate(v.Client, oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type RabbitMQPolicy.
func (v *RabbitMQPolicyCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rabbitmqpolicy, ok := obj.(*rabbitmqv1beta1.RabbitMQPolicy)
	if !ok {
		return nil, fmt.Errorf("expected a RabbitMQPolicy object but got %T", obj)
	}
	policylog.Info("Validation for RabbitMQPolicy upon deletion", "name", rabbitmqpolicy.GetName())

	return rabbitmqpolicy.ValidateDelete(v.Client)
}
