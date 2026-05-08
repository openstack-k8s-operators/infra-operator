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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
)

// RabbitMQ Condition Types used by API objects.
const (
	// RabbitMQProxyActiveCondition indicates that the AMQP proxy sidecar is running.
	// Status=True means the proxy is active and must be cleared by setting the
	// clients-reconfigured annotation. Status=False means no proxy is running.
	RabbitMQProxyActiveCondition condition.Type = "RabbitMQProxyActive"

	// ClusterAvailableCondition indicates that the RabbitMQ cluster has quorum
	// and can serve traffic. True when ReadyCount >= ceil(Replicas/2).
	// It never blocks ReadyCondition since quorum is always met before all replicas are ready.
	ClusterAvailableCondition condition.Type = "ClusterAvailable"
)

// TransportURL Condition Types used by API objects.
const (
	// TransportURLReadyCondition Status=True condition which indicates if TransportURL is configured and operational
	TransportURLReadyCondition condition.Type = "TransportURLReady"

	// TransportURLFinalizer - finalizer to add to RabbitMQUsers owned by TransportURL
	TransportURLFinalizer = "transporturl.rabbitmq.openstack.org/finalizer"

	// RabbitMQUserCleanupBlockedFinalizer - temporary finalizer to block automatic cleanup of RabbitMQUsers
	// This finalizer prevents TransportURL from automatically deleting users during credential rotation.
	// It must be manually removed by an operator/admin to allow cleanup to proceed.
	// TODO: Replace with proper safe-to-delete logic, then remove this finalizer from existing users.
	RabbitMQUserCleanupBlockedFinalizer = "rabbitmq.openstack.org/cleanup-blocked"
)

// TransportURL Reasons used by API objects.
const ()

// Common Messages used by API objects.
const (
	//
	// RabbitMQProxyActive condition messages
	//

	// RabbitMQProxyActiveMessage is the message when the proxy is active
	RabbitMQProxyActiveMessage = "AMQP proxy sidecar is active for queue migration. " +
		"To remove it, set annotation '%s: \"true\"' on the RabbitMq CR after all clients have been reconfigured for quorum queues"

	// RabbitMQProxyInactiveMessage is the message when the proxy is not active
	RabbitMQProxyInactiveMessage = "AMQP proxy sidecar is not active"

	//
	// ClusterAvailable condition messages
	//

	// ClusterAvailableMessage
	ClusterAvailableMessage = "RabbitMQ cluster has quorum and can serve traffic"

	// ClusterNotAvailableMessage
	ClusterNotAvailableMessage = "RabbitMQ cluster does not have quorum (%d/%d replicas ready, need %d)"

	//
	// TransportURLReady condition messages
	//

	// TransportURLReadyErrorMessage
	TransportURLReadyErrorMessage = "TransportURL error occured %s"

	// TransportURLReadyInitMessage
	TransportURLReadyInitMessage = "TransportURL not configured"

	// TransportURLReadyMessage
	TransportURLReadyMessage = "TransportURL completed"

	// TransportURLInProgressMessage
	TransportURLInProgressMessage = "TransportURL in progress"
)
