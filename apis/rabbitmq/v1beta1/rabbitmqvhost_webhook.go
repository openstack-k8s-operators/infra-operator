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

var rabbitmqvhostlog = logf.Log.WithName("rabbitmqvhost-resource")

//+kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqvhost,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=create;update,versions=v1beta1,name=mrabbitmqvhost.kb.io,admissionReviewVersions=v1

// Default implements defaulting for RabbitMQVhost
func (r *RabbitMQVhost) Default(_ client.Client) {
	rabbitmqvhostlog.Info("default", "name", r.Name)

	// Default the vhost name to "/" if not specified
	if r.Spec.Name == "" {
		r.Spec.Name = "/"
	}
}

//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqvhost,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqvhosts,verbs=create;update,versions=v1beta1,name=vrabbitmqvhost.kb.io,admissionReviewVersions=v1

// ValidateCreate validates the RabbitMQVhost on creation
func (r *RabbitMQVhost) ValidateCreate(_ client.Client) (admission.Warnings, error) {
	rabbitmqvhostlog.Info("validate create", "name", r.Name)

	// "/" is the default vhost and is always valid
	if r.Spec.Name != "/" {
		if err := validateRabbitMQName(r.Spec.Name, "vhost"); err != nil {
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQVhost"},
				r.Name,
				field.ErrorList{field.Invalid(field.NewPath("spec", "name"), r.Spec.Name, err.Error())},
			)
		}
	}

	return nil, nil
}

// ValidateUpdate validates the RabbitMQVhost on update
func (r *RabbitMQVhost) ValidateUpdate(_ client.Client, old runtime.Object) (admission.Warnings, error) {
	rabbitmqvhostlog.Info("validate update", "name", r.Name)

	oldVhost, ok := old.(*RabbitMQVhost)
	if !ok {
		return nil, fmt.Errorf("expected RabbitMQVhost but got %T", old)
	}

	// Prevent changing the vhost name after creation
	if r.Spec.Name != oldVhost.Spec.Name {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQVhost"},
			r.Name,
			field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "name"),
					"vhost name cannot be changed after creation",
				),
			},
		)
	}

	return nil, nil
}

// ValidateDelete validates the RabbitMQVhost on deletion
func (r *RabbitMQVhost) ValidateDelete(_ client.Client) (admission.Warnings, error) {
	return nil, nil
}
