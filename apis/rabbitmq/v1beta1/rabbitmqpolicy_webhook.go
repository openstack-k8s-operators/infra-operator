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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var rabbitmqpolicylog = logf.Log.WithName("rabbitmqpolicy-resource")

//+kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqpolicy,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqpolicies,verbs=create;update,versions=v1beta1,name=mrabbitmqpolicy.kb.io,admissionReviewVersions=v1

// Default implements defaulting for RabbitMQPolicy
func (r *RabbitMQPolicy) Default(_ client.Client) {
	rabbitmqpolicylog.Info("default", "name", r.Name)

	// Default the policy name to the CR name if not specified
	if r.Spec.Name == "" {
		r.Spec.Name = r.Name
	}
}

//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqpolicies,verbs=create;update,versions=v1beta1,name=vrabbitmqpolicy.kb.io,admissionReviewVersions=v1

// ValidateCreate validates the RabbitMQPolicy on creation
func (r *RabbitMQPolicy) ValidateCreate(_ client.Client) (admission.Warnings, error) {
	rabbitmqpolicylog.Info("validate create", "name", r.Name)

	if err := validateRabbitMQName(r.Spec.Name, "policy"); err != nil {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQPolicy"},
			r.Name,
			field.ErrorList{field.Invalid(field.NewPath("spec", "name"), r.Spec.Name, err.Error())},
		)
	}

	return nil, nil
}

// ValidateUpdate validates the RabbitMQPolicy on update
func (r *RabbitMQPolicy) ValidateUpdate(_ client.Client, old runtime.Object) (admission.Warnings, error) {
	rabbitmqpolicylog.Info("validate update", "name", r.Name)

	oldPolicy, ok := old.(*RabbitMQPolicy)
	if !ok {
		return nil, fmt.Errorf("expected RabbitMQPolicy but got %T", old)
	}

	// Prevent changing the policy name after creation
	if r.Spec.Name != oldPolicy.Spec.Name {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQPolicy"},
			r.Name,
			field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "name"),
					"policy name cannot be changed after creation",
				),
			},
		)
	}

	return nil, nil
}

// ValidateDelete validates the RabbitMQPolicy on deletion
func (r *RabbitMQPolicy) ValidateDelete(_ client.Client) (admission.Warnings, error) {
	return nil, nil
}
