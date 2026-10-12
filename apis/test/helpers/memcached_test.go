/*
Copyright 2026 Red Hat
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

package helpers

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMemcachedServerLists(t *testing.T) {
	name := types.NamespacedName{Name: "memcached", Namespace: "test"}
	tests := []struct {
		name                   string
		replicas               int32
		tlsSupport             bool
		ipFamily               corev1.IPFamily
		wantServerList         []string
		wantServerListWithInet []string
	}{
		{
			name:     "non-TLS IPv4",
			replicas: 2,
			ipFamily: corev1.IPv4Protocol,
			wantServerList: []string{
				"memcached-0.memcached.test.svc:11211",
				"memcached-1.memcached.test.svc:11211",
			},
			wantServerListWithInet: []string{
				"inet:memcached-0.memcached.test.svc:11211",
				"inet:memcached-1.memcached.test.svc:11211",
			},
		},
		{
			name:       "TLS provides TLS and non-TLS endpoint lists",
			replicas:   2,
			tlsSupport: true,
			ipFamily:   corev1.IPv4Protocol,
			wantServerList: []string{
				"memcached-0.memcached.test.svc:11212",
				"memcached-1.memcached.test.svc:11212",
			},
			wantServerListWithInet: []string{
				"inet:memcached-0.memcached.test.svc:11211",
				"inet:memcached-1.memcached.test.svc:11211",
			},
		},
		{
			name:       "TLS IPv6",
			replicas:   1,
			tlsSupport: true,
			ipFamily:   corev1.IPv6Protocol,
			wantServerList: []string{
				"memcached-0.memcached.test.svc:11212",
			},
			wantServerListWithInet: []string{
				"inet6:[memcached-0.memcached.test.svc]:11211",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotServerList, gotServerListWithInet := memcachedServerLists(
				name,
				tt.replicas,
				tt.tlsSupport,
				tt.ipFamily,
			)
			if !reflect.DeepEqual(gotServerList, tt.wantServerList) {
				t.Errorf("ServerList = %v, want %v", gotServerList, tt.wantServerList)
			}
			if !reflect.DeepEqual(gotServerListWithInet, tt.wantServerListWithInet) {
				t.Errorf("ServerListWithInet = %v, want %v", gotServerListWithInet, tt.wantServerListWithInet)
			}
		})
	}
}
