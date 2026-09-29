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

func TestMemcachedSpecCoreDefaultLogLevel(t *testing.T) {
	t.Run("defaults log level to debug", func(t *testing.T) {
		spec := MemcachedSpecCore{}

		spec.Default()

		if spec.LogLevel != "debug" {
			t.Fatalf("LogLevel = %q, want %q", spec.LogLevel, "debug")
		}
	})

	t.Run("preserves explicitly set log level", func(t *testing.T) {
		spec := MemcachedSpecCore{LogLevel: "none"}

		spec.Default()

		if spec.LogLevel != "none" {
			t.Fatalf("LogLevel = %q, want %q", spec.LogLevel, "none")
		}
	})
}

func TestMemcachedSpecCoreLogOption(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
		want     string
	}{
		{name: "none maps to empty", logLevel: "none", want: ""},
		{name: "verbose maps to -v", logLevel: "verbose", want: "-v"},
		{name: "debug maps to -vv", logLevel: "debug", want: "-vv"},
		{name: "unknown falls back to -vv", logLevel: "bogus", want: "-vv"},
		{name: "empty falls back to -vv", logLevel: "", want: "-vv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := MemcachedSpecCore{LogLevel: tt.logLevel}
			if got := spec.LogOption(); got != tt.want {
				t.Fatalf("LogOption() = %q, want %q", got, tt.want)
			}
		})
	}
}
