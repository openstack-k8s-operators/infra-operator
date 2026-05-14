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

package rabbitmq

import (
	"testing"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
)

func TestIsInternalFinalizer(t *testing.T) {
	tests := []struct {
		name       string
		finalizer  string
		wantResult bool
	}{
		{
			name:       "UserFinalizer is internal",
			finalizer:  rabbitmqv1.UserFinalizer,
			wantResult: true,
		},
		{
			name:       "TransportURLFinalizer is internal",
			finalizer:  rabbitmqv1.TransportURLFinalizer,
			wantResult: true,
		},
		{
			name:       "RabbitMQUserCleanupBlockedFinalizer is internal",
			finalizer:  rabbitmqv1.RabbitMQUserCleanupBlockedFinalizer,
			wantResult: true,
		},
		{
			name:       "per-consumer finalizer is internal",
			finalizer:  rabbitmqv1.TransportURLFinalizerPrefix + "my-transport-url",
			wantResult: true,
		},
		{
			name:       "vhost user finalizer is internal",
			finalizer:  rabbitmqv1.UserVhostFinalizerPrefix + "nova",
			wantResult: true,
		},
		{
			name:       "random finalizer is external",
			finalizer:  "some.other.controller/finalizer",
			wantResult: false,
		},
		{
			name:       "dataplane finalizer is external",
			finalizer:  "dataplane.openstack.org/finalizer",
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rabbitmqv1.IsInternalFinalizer(tt.finalizer)
			if result != tt.wantResult {
				t.Errorf("IsInternalFinalizer(%q) = %v, want %v", tt.finalizer, result, tt.wantResult)
			}
		})
	}
}
