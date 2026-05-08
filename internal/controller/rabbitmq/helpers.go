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

package rabbitmq

import (
	"context"
	"fmt"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	corev1 "k8s.io/api/core/v1"
)

// getManagementURL constructs the RabbitMQ management API URL from RabbitMq spec and secret data
func getManagementURL(rabbit *rabbitmqv1.RabbitMq, rabbitSecret *corev1.Secret) string {
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	protocol := "http"
	managementPort := "15672"
	if tlsEnabled {
		protocol = "https"
		managementPort = "15671"
	}

	// Use explicit management-port/management-host if present (allows override for testing)
	if mgmtPortBytes, ok := rabbitSecret.Data["management-port"]; ok {
		managementPort = string(mgmtPortBytes)
	}

	host := string(rabbitSecret.Data["host"])
	if mgmtHostBytes, ok := rabbitSecret.Data["management-host"]; ok {
		host = string(mgmtHostBytes)
	}

	return fmt.Sprintf("%s://%s:%s", protocol, host, managementPort)
}

// getTLSCACert retrieves the CA certificate for RabbitMQ TLS if configured
func getTLSCACert(ctx context.Context, h *helper.Helper, rabbit *rabbitmqv1.RabbitMq, namespace string) ([]byte, error) {
	if rabbit.Spec.TLS.CaSecretName == "" {
		return nil, nil
	}

	caSecret, _, err := oko_secret.GetSecret(ctx, h, rabbit.Spec.TLS.CaSecretName, namespace)
	if err != nil {
		return nil, err
	}

	caCert, ok := caSecret.Data["ca.crt"]
	if !ok {
		return nil, fmt.Errorf("ca.crt not found in CA secret %s", rabbit.Spec.TLS.CaSecretName)
	}

	return caCert, nil
}
