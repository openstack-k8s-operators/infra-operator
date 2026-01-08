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
	"time"

	corev1 "k8s.io/api/core/v1"
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
func (r *RabbitMQUser) Default(k8sClient client.Client) {
	rabbitmquserlog.Info("default", "name", r.Name)

	// If using a secret, extract and set username from secret
	if r.Spec.Secret != nil && *r.Spec.Secret != "" {
		// Default credential selectors if not provided (only needed when using secrets)
		if r.Spec.CredentialSelectors == nil {
			r.Spec.CredentialSelectors = &CredentialSelectors{
				Username: "username",
				Password: "password",
			}
		}

		secret := &corev1.Secret{}
		if err := k8sClient.Get(context.TODO(),
			client.ObjectKey{Name: *r.Spec.Secret, Namespace: r.Namespace},
			secret); err == nil {
			// Extract username from secret and set in spec
			usernameKey := r.Spec.CredentialSelectors.Username
			if usernameBytes, ok := secret.Data[usernameKey]; ok {
				r.Spec.Username = string(usernameBytes)
			}
		}
		// If username is still empty (secret fetch failed or username key not found),
		// default to CR name. The validation webhook will catch if the secret is truly missing.
		if r.Spec.Username == "" {
			r.Spec.Username = r.Name
		}
	} else if r.Spec.Username == "" {
		// No secret - default username to CR name
		r.Spec.Username = r.Name
	}
}

//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmquser,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqusers,verbs=create;update,versions=v1beta1,name=vrabbitmquser.kb.io,admissionReviewVersions=v1

// ValidateCreate validates the RabbitMQUser on creation
func (r *RabbitMQUser) ValidateCreate(k8sClient client.Client) (admission.Warnings, error) {
	rabbitmquserlog.Info("validate create", "name", r.Name)

	// Validate secret and credentials
	if err := r.validateSecretAndExtractCredentials(k8sClient); err != nil {
		return nil, err
	}

	// Validate permission regex patterns
	if err := r.validatePermissions(); err != nil {
		return nil, err
	}

	// Validate vhost reference if specified
	if r.Spec.VhostRef != "" {
		vhost := &RabbitMQVhost{}
		// Use a timeout context to prevent webhook from hanging
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := k8sClient.Get(ctx,
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

	return nil, r.validateUniqueUsername(k8sClient, r.Spec.Username)
}

// ValidateUpdate validates the RabbitMQUser on update
func (r *RabbitMQUser) ValidateUpdate(k8sClient client.Client, old runtime.Object) (admission.Warnings, error) {
	rabbitmquserlog.Info("validate update", "name", r.Name)

	oldUser, ok := old.(*RabbitMQUser)
	if !ok {
		return nil, fmt.Errorf("expected RabbitMQUser but got %T", old)
	}

	// Validate secret and credentials
	if err := r.validateSecretAndExtractCredentials(k8sClient); err != nil {
		return nil, err
	}

	// Prevent changing the username after creation
	// Check against status.Username if available (ground truth from RabbitMQ),
	// otherwise fall back to spec.Username
	oldUsername := oldUser.Status.Username
	if oldUsername == "" {
		oldUsername = oldUser.Spec.Username
	}

	if r.Spec.Username != oldUsername {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "username"),
					fmt.Sprintf("username cannot be changed (was %q, now %q)", oldUsername, r.Spec.Username),
				),
			},
		)
	}

	// Prevent changing VhostRef during deletion to avoid race conditions with finalizer cleanup
	if !r.DeletionTimestamp.IsZero() && r.Spec.VhostRef != oldUser.Spec.VhostRef {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Forbidden(
					field.NewPath("spec", "vhostRef"),
					"vhostRef cannot be changed while resource is being deleted",
				),
			},
		)
	}

	// Defensive validation: even though username can't change, validate format and length
	// This protects against bypasses or bugs in the immutability check
	if err := r.validateUsername(); err != nil {
		return nil, err
	}

	// Validate permission regex patterns
	if err := r.validatePermissions(); err != nil {
		return nil, err
	}

	// Validate vhost reference if specified (skip during deletion)
	// During deletion, the vhost may already be deleted, and we don't need to validate it
	if r.Spec.VhostRef != "" && r.DeletionTimestamp.IsZero() {
		vhost := &RabbitMQVhost{}
		// Use a timeout context to prevent webhook from hanging
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := k8sClient.Get(ctx,
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

	return nil, r.validateUniqueUsername(k8sClient, r.Spec.Username)
}

// ValidateDelete validates the RabbitMQUser on deletion
func (r *RabbitMQUser) ValidateDelete(client.Client) (admission.Warnings, error) {
	return nil, nil
}

// validateSecretAndExtractCredentials validates that the secret exists, has required keys,
// and validates the username format and length. This is used by both ValidateCreate and ValidateUpdate.
func (r *RabbitMQUser) validateSecretAndExtractCredentials(k8sClient client.Client) error {
	if r.Spec.Secret == nil || *r.Spec.Secret == "" {
		// When not using a secret, validate the username directly
		if err := r.validateUsername(); err != nil {
			return err
		}
		return nil
	}

	secretName := *r.Spec.Secret

	// Validate secret name doesn't use reserved pattern
	reservedPattern := fmt.Sprintf("rabbitmq-user-%s", r.Name)
	if secretName == reservedPattern {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "secret"), secretName,
					fmt.Sprintf("secret name %q is reserved for auto-generated secrets", secretName)),
			},
		)
	}

	secret := &corev1.Secret{}
	if err := k8sClient.Get(context.TODO(),
		client.ObjectKey{Name: secretName, Namespace: r.Namespace},
		secret); err != nil {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "secret"), secretName,
					fmt.Sprintf("referenced secret does not exist: %v", err)),
			},
		)
	}

	// Validate username and password keys exist
	usernameKey := r.Spec.CredentialSelectors.Username
	passwordKey := r.Spec.CredentialSelectors.Password

	if _, ok := secret.Data[usernameKey]; !ok {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "credentialSelectors", "username"),
					usernameKey,
					fmt.Sprintf("key %q not found in secret %s", usernameKey, secretName)),
			},
		)
	}

	if _, ok := secret.Data[passwordKey]; !ok {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "credentialSelectors", "password"),
					passwordKey,
					fmt.Sprintf("key %q not found in secret %s", passwordKey, secretName)),
			},
		)
	}

	// Validate username format and length (username should already be set by Default webhook)
	if err := r.validateUsername(); err != nil {
		return err
	}

	return nil
}

// validateUsername validates the username format and length
func (r *RabbitMQUser) validateUsername() error {
	var allErrs field.ErrorList

	// Validate username format
	if err := validateRabbitMQName(r.Spec.Username, "username"); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "username"), r.Spec.Username, err.Error()))
	}

	// Validate username length to ensure it fits within vhost finalizer length limits
	// The username will be used in the vhost finalizer: UserVhostFinalizerPrefix + username
	if len(r.Spec.Username) > MaxUsernameLength {
		allErrs = append(allErrs, field.TooLong(
			field.NewPath("spec", "username"),
			r.Spec.Username,
			MaxUsernameLength,
		))
	}

	if len(allErrs) != 0 {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			allErrs,
		)
	}

	return nil
}

// validateUniqueUsername checks that no other RabbitMQUser exists with the same username, vhost, and cluster
func (r *RabbitMQUser) validateUniqueUsername(k8sClient client.Client, username string) error {
	// List all RabbitMQUsers in the same namespace
	// Use a timeout context to prevent webhook from hanging
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	userList := &RabbitMQUserList{}
	if err := k8sClient.List(ctx, userList, client.InNamespace(r.Namespace)); err != nil {
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

		// Get the other user's username from spec.Username
		// (webhook ensures spec.Username is always populated, either from user input or from secret)
		otherUsername := user.Spec.Username

		// If usernames match, reject
		if username == otherUsername {
			return apierrors.NewInvalid(
				schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
				r.Name,
				field.ErrorList{
					field.Duplicate(
						field.NewPath("spec", "username"),
						fmt.Sprintf("username %q already exists in vhost %q on cluster %q (existing RabbitMQUser: %s)",
							username, r.Spec.VhostRef, r.Spec.RabbitmqClusterName, user.Name),
					),
				},
			)
		}
	}

	return nil
}

// validatePermissions validates that permission regex patterns are valid
func (r *RabbitMQUser) validatePermissions() error {
	var allErrs field.ErrorList

	// Validate configure permission regex
	if _, err := regexp.Compile(r.Spec.Permissions.Configure); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "permissions", "configure"),
			r.Spec.Permissions.Configure,
			fmt.Sprintf("invalid regex pattern: %v", err),
		))
	}

	// Validate write permission regex
	if _, err := regexp.Compile(r.Spec.Permissions.Write); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "permissions", "write"),
			r.Spec.Permissions.Write,
			fmt.Sprintf("invalid regex pattern: %v", err),
		))
	}

	// Validate read permission regex
	if _, err := regexp.Compile(r.Spec.Permissions.Read); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "permissions", "read"),
			r.Spec.Permissions.Read,
			fmt.Sprintf("invalid regex pattern: %v", err),
		))
	}

	if len(allErrs) != 0 {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQUser"},
			r.Name,
			allErrs,
		)
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
