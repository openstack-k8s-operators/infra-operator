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

// InstanceHa Condition Types used by API objects.
const (
	// InstanceHaReadyCondition Status=True condition which indicates if InstanceHa is configured and operational
	InstanceHaReadyCondition condition.Type = "InstanceHaReady"
)

// InstanceHa Reasons used by API objects.
const ()

// Common Messages used by API objects.
const (
	// InstanceHaReadyInitMessage
	InstanceHaReadyInitMessage = "Instance HA not started"

	// InstanceHaOpenStackConfigMapWaitingMessage
	InstanceHaOpenStackConfigMapWaitingMessage = "Instance HA waiting for OpenStack ConfigMap"

	// InstanceHaOpenStackConfigSecretWaitingMessage
	InstanceHaOpenStackConfigSecretWaitingMessage = "Instance HA waiting for OpenStack Config Secret"

	// InstanceHaConfigMapWaitingMessage)
	InstanceHaConfigMapWaitingMessage = "Instance HA waiting for InstanceHA ConfigMap"

	// InstanceHaFencingSecretWaitingMessage
	InstanceHaFencingSecretWaitingMessage = "Instance HA waiting for Fencing secret"

	// InstanceHaInputReady
	InstanceHaInputReady = "Instance HA input ready"

	// InstanceHaReadyMessage
	InstanceHaReadyMessage = "Instance HA created"

	// InstanceHaReadyErrorMessage
	InstanceHaReadyErrorMessage = "Instance HA error occured %s"
)
