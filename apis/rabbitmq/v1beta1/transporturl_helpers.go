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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

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
