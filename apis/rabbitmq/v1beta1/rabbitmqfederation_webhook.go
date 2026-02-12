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

	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var rabbitmqfederationlog = logf.Log.WithName("rabbitmqfederation-resource")

//+kubebuilder:webhook:path=/mutate-rabbitmq-openstack-org-v1beta1-rabbitmqfederation,mutating=true,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqfederations,verbs=create;update,versions=v1beta1,name=mrabbitmqfederation.kb.io,admissionReviewVersions=v1

// Default implements defaulting for RabbitMQFederation
func (r *RabbitMQFederation) Default(k8sClient client.Client) {
	rabbitmqfederationlog.Info("default", "name", r.Name)

	// Set default AckMode if not specified
	if r.Spec.AckMode == "" {
		r.Spec.AckMode = "on-confirm"
	}

	// Set default Expires if not specified (30 minutes in milliseconds)
	if r.Spec.Expires == 0 {
		r.Spec.Expires = 1800000
	}

	// Set default MaxHops if not specified
	if r.Spec.MaxHops == 0 {
		r.Spec.MaxHops = 1
	}

	// Set default PrefetchCount if not specified
	if r.Spec.PrefetchCount == 0 {
		r.Spec.PrefetchCount = 1000
	}

	// Set default ReconnectDelay if not specified (5 seconds)
	if r.Spec.ReconnectDelay == 0 {
		r.Spec.ReconnectDelay = 5
	}

	// Set default PolicyPattern if not specified
	if r.Spec.PolicyPattern == "" {
		r.Spec.PolicyPattern = ".*"
	}
}

//+kubebuilder:webhook:path=/validate-rabbitmq-openstack-org-v1beta1-rabbitmqfederation,mutating=false,failurePolicy=fail,sideEffects=None,groups=rabbitmq.openstack.org,resources=rabbitmqfederations,verbs=create;update,versions=v1beta1,name=vrabbitmqfederation.kb.io,admissionReviewVersions=v1

// ValidateCreate validates the RabbitMQFederation on creation
func (r *RabbitMQFederation) ValidateCreate(k8sClient client.Client) (admission.Warnings, error) {
	rabbitmqfederationlog.Info("validate create", "name", r.Name)

	var allErrs field.ErrorList

	// Validate mutually exclusive upstream configuration
	if err := r.validateUpstreamConfig(); err != nil {
		return nil, err
	}

	// Validate upstream name format
	if err := validateRabbitMQName(r.Spec.UpstreamName, "upstreamName"); err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "upstreamName"), r.Spec.UpstreamName, err.Error()))
	}

	// Validate exchange/queue names if specified
	if r.Spec.Exchange != "" {
		if err := validateRabbitMQName(r.Spec.Exchange, "exchange"); err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "exchange"), r.Spec.Exchange, err.Error()))
		}
	}

	if r.Spec.Queue != "" {
		if err := validateRabbitMQName(r.Spec.Queue, "queue"); err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("spec", "queue"), r.Spec.Queue, err.Error()))
		}
	}

	// Validate PolicyPattern regex
	if _, err := regexp.Compile(r.Spec.PolicyPattern); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "policyPattern"),
			r.Spec.PolicyPattern,
			fmt.Sprintf("invalid regex pattern: %v", err),
		))
	}

	// Validate UpstreamSecretRef exists if specified
	if r.Spec.UpstreamSecretRef != nil {
		if err := r.validateUpstreamSecret(k8sClient); err != nil {
			return nil, err
		}
	}

	// Validate UpstreamClusterName references exist if specified
	if r.Spec.UpstreamClusterName != "" {
		if err := r.validateUpstreamCluster(k8sClient); err != nil {
			return nil, err
		}
	}

	// Validate vhost reference if specified
	if r.Spec.VhostRef != "" {
		if err := r.validateVhostRef(k8sClient); err != nil {
			return nil, err
		}
	}

	if len(allErrs) != 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			allErrs,
		)
	}

	return nil, nil
}

// ValidateUpdate validates the RabbitMQFederation on update
func (r *RabbitMQFederation) ValidateUpdate(k8sClient client.Client, old runtime.Object) (admission.Warnings, error) {
	rabbitmqfederationlog.Info("validate update", "name", r.Name)

	oldFederation, ok := old.(*RabbitMQFederation)
	if !ok {
		return nil, fmt.Errorf("expected RabbitMQFederation but got %T", old)
	}

	var allErrs field.ErrorList

	// Validate mutually exclusive upstream configuration
	if err := r.validateUpstreamConfig(); err != nil {
		return nil, err
	}

	// Prevent changing immutable fields
	if r.Spec.UpstreamName != oldFederation.Spec.UpstreamName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "upstreamName"),
			fmt.Sprintf("upstreamName cannot be changed (was %q, now %q)", oldFederation.Spec.UpstreamName, r.Spec.UpstreamName),
		))
	}

	if r.Spec.RabbitmqClusterName != oldFederation.Spec.RabbitmqClusterName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "rabbitmqClusterName"),
			fmt.Sprintf("rabbitmqClusterName cannot be changed (was %q, now %q)", oldFederation.Spec.RabbitmqClusterName, r.Spec.RabbitmqClusterName),
		))
	}

	// Prevent changing VhostRef during deletion to avoid race conditions
	if !r.DeletionTimestamp.IsZero() && r.Spec.VhostRef != oldFederation.Spec.VhostRef {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "vhostRef"),
			"vhostRef cannot be changed while resource is being deleted",
		))
	}

	// Prevent changing UpstreamClusterName during deletion to avoid race conditions
	if !r.DeletionTimestamp.IsZero() && r.Spec.UpstreamClusterName != oldFederation.Spec.UpstreamClusterName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "upstreamClusterName"),
			"upstreamClusterName cannot be changed while resource is being deleted",
		))
	}

	// Prevent changing upstream mode (ClusterName <-> SecretRef)
	oldHasClusterName := oldFederation.Spec.UpstreamClusterName != ""
	newHasClusterName := r.Spec.UpstreamClusterName != ""
	oldHasSecretRef := oldFederation.Spec.UpstreamSecretRef != nil
	newHasSecretRef := r.Spec.UpstreamSecretRef != nil

	if oldHasClusterName != newHasClusterName || oldHasSecretRef != newHasSecretRef {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec"),
			"cannot change between upstreamClusterName and upstreamSecretRef modes",
		))
	}

	if len(allErrs) != 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			allErrs,
		)
	}

	// Run create validations for updated values
	return r.ValidateCreate(k8sClient)
}

// ValidateDelete validates the RabbitMQFederation on deletion
func (r *RabbitMQFederation) ValidateDelete(client.Client) (admission.Warnings, error) {
	return nil, nil
}

// validateUpstreamConfig validates that exactly one upstream configuration method is specified
func (r *RabbitMQFederation) validateUpstreamConfig() error {
	hasClusterName := r.Spec.UpstreamClusterName != ""
	hasSecretRef := r.Spec.UpstreamSecretRef != nil

	if !hasClusterName && !hasSecretRef {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Required(
					field.NewPath("spec"),
					"must specify either upstreamClusterName or upstreamSecretRef",
				),
			},
		)
	}

	if hasClusterName && hasSecretRef {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Invalid(
					field.NewPath("spec"),
					"both fields set",
					"upstreamClusterName and upstreamSecretRef are mutually exclusive",
				),
			},
		)
	}

	return nil
}

// validateUpstreamSecret validates that the upstream secret exists and has required keys
func (r *RabbitMQFederation) validateUpstreamSecret(k8sClient client.Client) error {
	secretName := r.Spec.UpstreamSecretRef.Name

	secret := &corev1.Secret{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := k8sClient.Get(ctx,
		client.ObjectKey{Name: secretName, Namespace: r.Namespace},
		secret); err != nil {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "upstreamSecretRef", "name"), secretName,
					fmt.Sprintf("referenced secret does not exist: %v", err)),
			},
		)
	}

	// Validate that secret contains 'uri' key
	if _, ok := secret.Data["uri"]; !ok {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "upstreamSecretRef", "name"),
					secretName,
					"secret must contain 'uri' key with AMQP URI(s)"),
			},
		)
	}

	return nil
}

// validateUpstreamCluster validates that the upstream RabbitMQ cluster exists
func (r *RabbitMQFederation) validateUpstreamCluster(k8sClient client.Client) error {
	clusterName := r.Spec.UpstreamClusterName

	cluster := &rabbitmqclusterv2.RabbitmqCluster{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := k8sClient.Get(ctx,
		client.ObjectKey{Name: clusterName, Namespace: r.Namespace},
		cluster); err != nil {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "upstreamClusterName"), clusterName,
					fmt.Sprintf("referenced RabbitMQ cluster does not exist: %v", err)),
			},
		)
	}

	return nil
}

// validateVhostRef validates that the vhost reference exists
func (r *RabbitMQFederation) validateVhostRef(k8sClient client.Client) error {
	vhost := &RabbitMQVhost{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := k8sClient.Get(ctx,
		client.ObjectKey{Name: r.Spec.VhostRef, Namespace: r.Namespace},
		vhost); err != nil {
		return apierrors.NewInvalid(
			schema.GroupKind{Group: "rabbitmq.openstack.org", Kind: "RabbitMQFederation"},
			r.Name,
			field.ErrorList{
				field.Invalid(field.NewPath("spec", "vhostRef"), r.Spec.VhostRef,
					fmt.Sprintf("referenced vhost does not exist: %v", err)),
			},
		)
	}

	return nil
}
