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

// Package v1beta1 contains the webhook implementation for Redis resources.
package v1beta1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var redislog = logf.Log.WithName("redis-resource")

// SetupRedisWebhookWithManager registers the webhook for Redis in the manager.
func SetupRedisWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&redisv1beta1.Redis{}).
		WithValidator(&RedisCustomValidator{}).
		WithDefaulter(&RedisCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-redis-openstack-org-v1beta1-redis,mutating=true,failurePolicy=fail,sideEffects=None,groups=redis.openstack.org,resources=redises,verbs=create;update,versions=v1beta1,name=mredis-v1beta1.kb.io,admissionReviewVersions=v1

// RedisCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Redis when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type RedisCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &RedisCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Redis.
func (d *RedisCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	redis, ok := obj.(*redisv1beta1.Redis)

	if !ok {
		return fmt.Errorf("expected an Redis object but got %T", obj)
	}
	redislog.Info("Defaulting for Redis", "name", redis.GetName())

	redis.Default()

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-redis-openstack-org-v1beta1-redis,mutating=false,failurePolicy=fail,sideEffects=None,groups=redis.openstack.org,resources=redises,verbs=create;update,versions=v1beta1,name=vredis-v1beta1.kb.io,admissionReviewVersions=v1

// RedisCustomValidator struct is responsible for validating the Redis resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type RedisCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &RedisCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Redis.
func (v *RedisCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	redis, ok := obj.(*redisv1beta1.Redis)
	if !ok {
		return nil, fmt.Errorf("expected a Redis object but got %T", obj)
	}
	redislog.Info("Validation for Redis upon creation", "name", redis.GetName())

	return redis.ValidateCreate()
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Redis.
func (v *RedisCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	redis, ok := newObj.(*redisv1beta1.Redis)
	if !ok {
		return nil, fmt.Errorf("expected a Redis object for the newObj but got %T", newObj)
	}
	redislog.Info("Validation for Redis upon update", "name", redis.GetName())

	return redis.ValidateUpdate(oldObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Redis.
func (v *RedisCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	redis, ok := obj.(*redisv1beta1.Redis)
	if !ok {
		return nil, fmt.Errorf("expected a Redis object but got %T", obj)
	}
	redislog.Info("Validation for Redis upon deletion", "name", redis.GetName())

	return redis.ValidateDelete()
}
