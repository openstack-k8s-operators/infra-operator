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
	"regexp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var rabbitmquserlog = logf.Log.WithName("rabbitmquser-resource")

const rabbitMQVarNameFmt = "[-_.:A-Za-z0-9]+"

var rabbitMQVarNameFmtRegexp = regexp.MustCompile("^" + rabbitMQVarNameFmt + "$")

//+kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmquser,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=create;update,versions=v1beta1,name=mrabbitmquser.kb.io,admissionReviewVersions=v1

// Default implements defaulting for RabbitMQUser
func (r *RabbitMQUser) Default(_ client.Client) {
	rabbitmquserlog.Info("default", "name", r.Name)

	// Default the username to the CR name if not specified
	if r.Spec.Username == "" {
		r.Spec.Username = r.Name
	}
}

//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmquser,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=create;update,versions=v1beta1,name=vrabbitmquser.kb.io,admissionReviewVersions=v1

// ValidateCreate validates the RabbitMQUser on creation
func (r *RabbitMQUser) ValidateCreate(k8sClient client.Client) (admission.Warnings, error) {
	rabbitmquserlog.Info("validate create", "name", r.Name)

	// Validate username
	if err := validateRabbitMQName(r.Spec.Username, "username"); err != nil {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{field.Invalid(field.NewPath("spec", "username"), r.Spec.Username, err.Error())},
		)
	}

	// Validate vhost reference if specified
	if r.Spec.VhostRef != "" {
		vhost := &RabbitMQVhost{}
		if err := k8sClient.Get(context.TODO(),
			client.ObjectKey{Name: r.Spec.VhostRef, Namespace: r.Namespace},
			vhost); err != nil {
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
				r.Name,
				field.ErrorList{
					field.Invalid(field.NewPath("spec", "vhostRef"), r.Spec.VhostRef,
						fmt.Sprintf("referenced vhost does not exist: %v", err)),
				},
			)
		}
	}

	return nil, r.validateUniqueUsername(k8sClient)
}

// ValidateUpdate validates the RabbitMQUser on update
func (r *RabbitMQUser) ValidateUpdate(k8sClient client.Client, old runtime.Object) (admission.Warnings, error) {
	rabbitmquserlog.Info("validate update", "name", r.Name)

	oldUser, ok := old.(*RabbitMQUser)
	if !ok {
		return nil, fmt.Errorf("expected RabbitMQUser but got %T", old)
	}

	// Prevent changing the username after creation
	if r.Spec.Username != oldUser.Spec.Username {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "username"),
					"username cannot be changed after creation",
				),
			},
		)
	}

	// Validate vhost reference if specified
	if r.Spec.VhostRef != "" {
		vhost := &RabbitMQVhost{}
		if err := k8sClient.Get(context.TODO(),
			client.ObjectKey{Name: r.Spec.VhostRef, Namespace: r.Namespace},
			vhost); err != nil {
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
				r.Name,
				field.ErrorList{
					field.Invalid(field.NewPath("spec", "vhostRef"), r.Spec.VhostRef,
						fmt.Sprintf("referenced vhost does not exist: %v", err)),
				},
			)
		}
	}

	return nil, r.validateUniqueUsername(k8sClient)
}

// ValidateDelete validates the RabbitMQUser on deletion
func (r *RabbitMQUser) ValidateDelete(client.Client) (admission.Warnings, error) {
	return nil, nil
}

// validateUniqueUsername checks that no other RabbitMQUser exists with the same username, vhost, and cluster
func (r *RabbitMQUser) validateUniqueUsername(k8sClient client.Client) error {
	// List all RabbitMQUsers in the same namespace
	userList := &RabbitMQUserList{}
	if err := k8sClient.List(context.TODO(), userList, client.InNamespace(r.Namespace)); err != nil {
		return apierrors.NewInternalError(fmt.Errorf("failed to list RabbitMQUsers: %w", err))
	}

	// Check for conflicts
	for _, user := range userList.Items {
		// Skip self
		if user.Name == r.Name {
			continue
		}

		// Check if same RabbitMQ cluster
		if user.Spec.RabbitmqClusterName != r.Spec.RabbitmqClusterName {
			continue
		}

		// Check if same vhost
		if user.Spec.VhostRef != r.Spec.VhostRef {
			continue
		}

		// If usernames match, reject
		if r.Spec.Username == user.Spec.Username {
			return apierrors.NewInvalid(
				schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
				r.Name,
				field.ErrorList{
					field.Duplicate(
						field.NewPath("spec", "username"),
						fmt.Sprintf("username %q already exists in vhost %q on cluster %q (existing RabbitMQUser: %s)",
							r.Spec.Username, r.Spec.VhostRef, r.Spec.RabbitmqClusterName, user.Name),
					),
				},
			)
		}
	}

	return nil
}

// validateRabbitMQName validates names for RabbitMQ resources
// RabbitMQ naming rules: letters, digits, hyphens, underscores, periods, colons
func validateRabbitMQName(name, resourceType string) error {
	if name == "" {
		return fmt.Errorf("%s name cannot be empty", resourceType)
	}

	// RabbitMQ allows: a-z A-Z 0-9 - _ . :
	if !rabbitMQVarNameFmtRegexp.MatchString(name) {
		return fmt.Errorf("%s name contains invalid characters, allowed: letters, digits, hyphens, underscores, periods, colons", resourceType)
	}

	return nil
}
