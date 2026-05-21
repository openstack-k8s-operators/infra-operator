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

import (
	"strings"
	"testing"
)

func TestCanonicalUserName(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		vhostName   string
		username    string
	}{
		{
			name:        "short names",
			clusterName: "rabbitmq",
			vhostName:   "/",
			username:    "nova",
		},
		{
			name:        "with custom vhost",
			clusterName: "rabbitmq",
			vhostName:   "testvhost",
			username:    "nova-user",
		},
		{
			name:        "long names that broke with old convention",
			clusterName: "rabbitmq-notifications",
			vhostName:   "watcher-notifications",
			username:    "watcher-notifications",
		},
		{
			name:        "very long names",
			clusterName: strings.Repeat("cluster", 5),
			vhostName:   strings.Repeat("vhost", 5),
			username:    "a-reasonable-username",
		},
		{
			name:        "empty vhost treated as default",
			clusterName: "rabbitmq",
			vhostName:   "",
			username:    "testuser",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalUserName(tt.clusterName, tt.vhostName, tt.username)

			if len(got) > 63 {
				t.Errorf("canonical user name %q is %d chars, exceeds 63-char label limit", got, len(got))
			}

			if !strings.HasPrefix(got, tt.username+"-") {
				t.Errorf("canonical user name %q does not start with username prefix %q", got, tt.username+"-")
			}

			// Verify determinism
			got2 := CanonicalUserName(tt.clusterName, tt.vhostName, tt.username)
			if got != got2 {
				t.Errorf("non-deterministic: got %q then %q", got, got2)
			}
		})
	}
}

func TestCanonicalUserNameUniqueness(t *testing.T) {
	// Same username on different clusters must produce different names
	name1 := CanonicalUserName("rabbitmq", "/", "nova")
	name2 := CanonicalUserName("rabbitmq-cell1", "/", "nova")
	if name1 == name2 {
		t.Errorf("same user on different clusters produced identical name: %q", name1)
	}

	// Same username on different vhosts must produce different names
	name3 := CanonicalUserName("rabbitmq", "vhost-a", "nova")
	name4 := CanonicalUserName("rabbitmq", "vhost-b", "nova")
	if name3 == name4 {
		t.Errorf("same user on different vhosts produced identical name: %q", name3)
	}

	// Different usernames on same cluster/vhost must produce different names
	name5 := CanonicalUserName("rabbitmq", "/", "nova")
	name6 := CanonicalUserName("rabbitmq", "/", "cinder")
	if name5 == name6 {
		t.Errorf("different users on same cluster produced identical name: %q", name5)
	}
}

func TestCanonicalVhostName(t *testing.T) {
	tests := []struct {
		name        string
		clusterName string
		vhostName   string
	}{
		{
			name:        "short names",
			clusterName: "rabbitmq",
			vhostName:   "testvhost",
		},
		{
			name:        "long names that broke with old convention",
			clusterName: "rabbitmq-notifications",
			vhostName:   "watcher-notifications",
		},
		{
			name:        "very long names",
			clusterName: strings.Repeat("cluster", 5),
			vhostName:   "a-reasonable-vhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanonicalVhostName(tt.clusterName, tt.vhostName)

			if len(got) > 63 {
				t.Errorf("canonical vhost name %q is %d chars, exceeds 63-char label limit", got, len(got))
			}

			if !strings.HasPrefix(got, tt.vhostName+"-") {
				t.Errorf("canonical vhost name %q does not start with vhost prefix %q", got, tt.vhostName+"-")
			}

			// Verify determinism
			got2 := CanonicalVhostName(tt.clusterName, tt.vhostName)
			if got != got2 {
				t.Errorf("non-deterministic: got %q then %q", got, got2)
			}
		})
	}
}

func TestCanonicalVhostNameUniqueness(t *testing.T) {
	// Same vhost on different clusters must produce different names
	name1 := CanonicalVhostName("rabbitmq", "testvhost")
	name2 := CanonicalVhostName("rabbitmq-cell1", "testvhost")
	if name1 == name2 {
		t.Errorf("same vhost on different clusters produced identical name: %q", name1)
	}
}
