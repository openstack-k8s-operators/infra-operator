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
	"strings"
	"testing"
)

func TestTransportURLFinalizerFor(t *testing.T) {
	tests := []struct {
		name         string
		transportURL string
		wantPrefix   bool
		wantMaxLen   int
		wantExact    string
	}{
		{
			name:         "short name used directly",
			transportURL: "nova-api-transport",
			wantPrefix:   true,
			wantExact:    TransportURLFinalizerPrefix + "nova-api-transport",
		},
		{
			name:         "61-char name fits exactly",
			transportURL: strings.Repeat("a", 61),
			wantPrefix:   true,
			wantExact:    TransportURLFinalizerPrefix + strings.Repeat("a", 61),
		},
		{
			name:         "62-char name gets truncated and hashed",
			transportURL: strings.Repeat("b", 62),
			wantPrefix:   true,
			wantMaxLen:   maxFinalizerNameSegment + len("turl.openstack.org/"),
		},
		{
			name:         "253-char name gets truncated and hashed",
			transportURL: strings.Repeat("c", 253),
			wantPrefix:   true,
			wantMaxLen:   maxFinalizerNameSegment + len("turl.openstack.org/"),
		},
		{
			name:         "different long names produce different finalizers",
			transportURL: strings.Repeat("d", 100),
			wantPrefix:   true,
			wantMaxLen:   maxFinalizerNameSegment + len("turl.openstack.org/"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TransportURLFinalizerFor(tt.transportURL)

			if !strings.HasPrefix(got, TransportURLFinalizerPrefix) {
				t.Errorf("finalizer %q does not start with prefix %q", got, TransportURLFinalizerPrefix)
			}

			nameSegment := strings.SplitN(got, "/", 2)[1]
			if len(nameSegment) > maxFinalizerNameSegment {
				t.Errorf("name segment %q is %d chars, exceeds max %d", nameSegment, len(nameSegment), maxFinalizerNameSegment)
			}

			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("got %q, want %q", got, tt.wantExact)
			}
		})
	}

	// Verify two different long names produce different finalizers
	fin1 := TransportURLFinalizerFor(strings.Repeat("x", 100))
	fin2 := TransportURLFinalizerFor(strings.Repeat("y", 100))
	if fin1 == fin2 {
		t.Errorf("different long names produced the same finalizer: %q", fin1)
	}
}
