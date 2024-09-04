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

// InstanceHA Condition Types used by API objects.
const (
	// InstanceHAReadyCondition Status=True condition which indicates if InstanceHA is configured and operational
	InstanceHAReadyCondition condition.Type = "InstanceHAReady"
)

// InstanceHA Reasons used by API objects.
const ()

// Common Messages used by API objects.
const (
	// InstanceHAReadyInitMessage
	InstanceHAReadyInitMessage = "Instance HA not started, waiting on keystone API"

	// InstanceHAKeystoneWaitingMessage
	InstanceHAKeystoneWaitingMessage = "Instance HA keystone API not yet ready"

	// InstanceHAConfigMapWaitingMessage
	InstanceHAConfigMapWaitingMessage = "Instance HA waiting for configmap"

	// InstanceHASecretWaitingMessage
	InstanceHASecretWaitingMessage = "Instance HA waiting for secret"

	// InstanceHAInputReady
	InstanceHAInputReady = "Instance HA input ready"

	// InstanceHAReadyMessage
	InstanceHAReadyMessage = "Instance HA created"

	// InstanceHAReadyErrorMessage
	InstanceHAReadyErrorMessage = "Instance HA error occured %s"
)
