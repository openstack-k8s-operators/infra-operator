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

package v1beta1

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateQueuePolicies(t *testing.T) {
	basePath := field.NewPath("spec")

	tests := []struct {
		name     string
		policies *QueuePolicies
		wantErr  bool
	}{
		{name: "nil queuePolicies is allowed", policies: nil, wantErr: false},
		{
			name:     "empty transient pattern is allowed (defaults)",
			policies: &QueuePolicies{TransientQueueExpiry: &TransientQueueExpiryPolicy{Expiry: 60000}},
			wantErr:  false,
		},
		{
			name:     "default transient pattern is valid",
			policies: &QueuePolicies{TransientQueueExpiry: &TransientQueueExpiryPolicy{Expiry: 60000, Pattern: DefaultTransientQueueExpiryPattern}},
			wantErr:  false,
		},
		{
			name:     "invalid transient regex is rejected",
			policies: &QueuePolicies{TransientQueueExpiry: &TransientQueueExpiryPolicy{Expiry: 60000, Pattern: "^(reply_.*"}},
			wantErr:  true,
		},
		{
			name:     "valid messageTTL pattern is accepted",
			policies: &QueuePolicies{MessageTTL: &MessageTTLPolicy{TTL: 60000, Pattern: "^clear-.*"}},
			wantErr:  false,
		},
		{
			name:     "invalid messageTTL regex is rejected",
			policies: &QueuePolicies{MessageTTL: &MessageTTLPolicy{TTL: 60000, Pattern: "^(clear-.*"}},
			wantErr:  true,
		},
		{
			name:     "invalid queueExpiry regex is rejected",
			policies: &QueuePolicies{QueueExpiry: &QueueExpiryPolicy{Expiry: 1800000, Pattern: "^(clear-.*"}},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := RabbitMqSpecCore{QueuePolicies: tt.policies}
			errs := spec.ValidateQueuePolicies(basePath)
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected a validation error, got none")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Errorf("expected no validation error, got %v", errs)
			}
		})
	}
}

func TestSpecCoreDefault_MigratesOldGracePeriod(t *testing.T) {
	oldDefault := int64(604800)
	spec := RabbitMqSpecCore{
		TerminationGracePeriodSeconds: &oldDefault,
	}

	spec.Default(false)

	if spec.TerminationGracePeriodSeconds == nil {
		t.Fatal("TerminationGracePeriodSeconds should not be nil")
	}
	if *spec.TerminationGracePeriodSeconds != 60 {
		t.Errorf("TerminationGracePeriodSeconds = %d, want 60", *spec.TerminationGracePeriodSeconds)
	}
}

func TestSpecCoreDefault_PreservesCustomGracePeriod(t *testing.T) {
	custom := int64(120)
	spec := RabbitMqSpecCore{
		TerminationGracePeriodSeconds: &custom,
	}

	spec.Default(false)

	if spec.TerminationGracePeriodSeconds == nil {
		t.Fatal("TerminationGracePeriodSeconds should not be nil")
	}
	if *spec.TerminationGracePeriodSeconds != 120 {
		t.Errorf("TerminationGracePeriodSeconds = %d, want 120", *spec.TerminationGracePeriodSeconds)
	}
}

func TestSpecCoreDefault_PreservesNewDefault(t *testing.T) {
	newDefault := int64(60)
	spec := RabbitMqSpecCore{
		TerminationGracePeriodSeconds: &newDefault,
	}

	spec.Default(false)

	if spec.TerminationGracePeriodSeconds == nil {
		t.Fatal("TerminationGracePeriodSeconds should not be nil")
	}
	if *spec.TerminationGracePeriodSeconds != 60 {
		t.Errorf("TerminationGracePeriodSeconds = %d, want 60", *spec.TerminationGracePeriodSeconds)
	}
}
