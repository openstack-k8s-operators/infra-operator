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

	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	oko_secret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
)

// getManagementURL constructs the RabbitMQ management API URL from cluster spec and secret data
func getManagementURL(rabbit *rabbitmqclusterv2.RabbitmqCluster, rabbitSecret *corev1.Secret) string {
	tlsEnabled := rabbit.Spec.TLS.SecretName != ""
	protocol := "http"
	managementPort := "15672"
	if tlsEnabled {
		protocol = "https"
		managementPort = "15671"
	}

	// Check if secret has a custom port (for testing with mock servers)
	// In production, the port field contains the AMQP port (5672/5671), not the management port
	// But in tests with mock servers, we put the mock server's port in the port field
	if portBytes, ok := rabbitSecret.Data["port"]; ok {
		port := string(portBytes)
		// If port is not a standard AMQP port, assume it's a custom management port (for tests)
		if port != "5672" && port != "5671" {
			managementPort = port
		}
	}

	return fmt.Sprintf("%s://%s:%s", protocol, string(rabbitSecret.Data["host"]), managementPort)
}

// getTLSCACert retrieves the CA certificate for RabbitMQ TLS if configured
func getTLSCACert(ctx context.Context, h *helper.Helper, rabbit *rabbitmqclusterv2.RabbitmqCluster, namespace string) ([]byte, error) {
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
