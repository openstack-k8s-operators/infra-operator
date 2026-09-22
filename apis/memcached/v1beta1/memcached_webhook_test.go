/*
Copyright 2026.

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

func TestMemcachedSpecCoreDefaultExtraOptions(t *testing.T) {
	t.Run("defaults extra options to verbose logging", func(t *testing.T) {
		spec := MemcachedSpecCore{}

		spec.Default()

		if len(spec.ExtraOptions) != 1 || spec.ExtraOptions[0] != "-vv" {
			t.Fatalf("ExtraOptions = %v, want [-vv]", spec.ExtraOptions)
		}
	})

	t.Run("preserves explicitly empty extra options", func(t *testing.T) {
		spec := MemcachedSpecCore{ExtraOptions: []string{}}

		spec.Default()

		if spec.ExtraOptions == nil || len(spec.ExtraOptions) != 0 {
			t.Fatalf("ExtraOptions = %v, want empty list", spec.ExtraOptions)
		}
	})
}
