/*
Copyright 2022.

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

// Condition Types used by API objects.
const (
	// ReservationReadyCondition indicates if the IP reservation was successful
	ReservationReadyCondition condition.Type = "ReservationReady"
)

// Common Messages used by API objects.
const (
	// NetConfigErrorMessage
	NetConfigErrorMessage = "NetConfig error occured %s"

	// NetConfigMissingMessage
	NetConfigMissingMessage = "NetConfig missing in namespace %s"

	// ReservationListErrorMessage
	ReservationListErrorMessage = "Getting Reservations error occured %s"

	// ReservationInitMessage
	ReservationInitMessage = "Reservation create not started"

	// ReservationErrorMessage
	ReservationErrorMessage = "Reservation error occured %s"

	// ReservationMisMatchErrorMessage
	ReservationMisMatchErrorMessage = "Reservation reservations (%d) do not match requested (%d)"

	// ReservationReadyMessage
	ReservationReadyMessage = "Reservation successful"
)
