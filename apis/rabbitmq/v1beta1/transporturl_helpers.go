/*
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
	"strings"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/object"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ManageTransportSecretFinalizer ensures consumerFinalizer is present on the
// transport secret identified by secretName. It never removes the finalizer
// from a previous secret — that is the consumer's responsibility after it has
// confirmed its deployment is running with the new credentials (typically via
// RemoveTransportSecretConsumerFinalizer). This ensures the TransportURL
// controller waits for all consumers before releasing the old RabbitMQ user.
func ManageTransportSecretFinalizer(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	secretName string,
	consumerFinalizer string,
) error {
	if secretName == "" {
		return nil
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: secretName, Namespace: namespace}
	if err := h.GetClient().Get(ctx, key, secret); err != nil {
		return fmt.Errorf("failed to get transport secret %s: %w", secretName, err)
	}

	return object.AddConsumerFinalizer(ctx, h, secret, consumerFinalizer)
}

// RemoveTransportSecretConsumerFinalizer removes consumerFinalizer from the
// transport secret identified by secretName. It is a no-op when secretName
// is empty or the secret no longer exists.
func RemoveTransportSecretConsumerFinalizer(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	secretName string,
	consumerFinalizer string,
) error {
	if secretName == "" {
		return nil
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: secretName, Namespace: namespace}
	if err := h.GetClient().Get(ctx, key, secret); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return object.RemoveConsumerFinalizer(ctx, h, secret, consumerFinalizer)
}

// HasTransportConsumerFinalizer returns true if the secret has any finalizer
// matching the transport consumer pattern (openstack.org/*-transport-consumer).
func HasTransportConsumerFinalizer(secret *corev1.Secret) bool {
	for _, f := range secret.Finalizers {
		if strings.HasSuffix(f, TransportSecretConsumerSuffix) &&
			strings.HasPrefix(f, "openstack.org/") {
			return true
		}
	}
	return false
}

// HasSpecificTransportConsumerFinalizer returns true if the secret has the
// given consumer finalizer.
func HasSpecificTransportConsumerFinalizer(secret *corev1.Secret, consumerFinalizer string) bool {
	return controllerutil.ContainsFinalizer(secret, consumerFinalizer)
}
