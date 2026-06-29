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

import "testing"

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
