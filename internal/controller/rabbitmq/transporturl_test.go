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

package rabbitmq

import (
	"testing"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCreateTransportURLSecret(t *testing.T) {
	r := &TransportURLReconciler{}
	instance := &rabbitmqv1.TransportURL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-transport",
			Namespace: "openstack",
		},
	}

	tests := []struct {
		name       string
		username   string
		password   string
		hosts      []string
		port       string
		vhost      string
		tlsEnabled bool
		quorum     bool
		wantURL    string
	}{
		{
			name:       "simple alphanumeric password",
			username:   "nova",
			password:   "secret123",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://nova:secret123@rabbitmq.openstack.svc:5672/?ssl=0",
		},
		{
			name:       "password with special characters",
			username:   "nova",
			password:   "p@ss:w/rd#1",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://nova:p%40ss%3Aw%2Frd%231@rabbitmq.openstack.svc:5672/?ssl=0",
		},
		{
			name:       "username with special characters",
			username:   "user@domain",
			password:   "secret",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://user%40domain:secret@rabbitmq.openstack.svc:5672/?ssl=0",
		},
		{
			name:       "password with percent sign",
			username:   "nova",
			password:   "100%safe",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://nova:100%25safe@rabbitmq.openstack.svc:5672/?ssl=0",
		},
		{
			name:       "TLS enabled with custom vhost",
			username:   "nova",
			password:   "s3cr3t!",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5671",
			vhost:      "nova",
			tlsEnabled: true,
			wantURL:    "rabbit://nova:s3cr3t%21@rabbitmq.openstack.svc:5671/nova?ssl=1",
		},
		{
			name:       "multiple hosts with special password",
			username:   "nova",
			password:   "p@ss",
			hosts:      []string{"rabbit-0.svc", "rabbit-1.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://nova:p%40ss@rabbit-0.svc:5672,nova:p%40ss@rabbit-1.svc:5672/?ssl=0",
		},
		{
			name:       "quorum flag set",
			username:   "nova",
			password:   "secret",
			hosts:      []string{"rabbitmq.openstack.svc"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			quorum:     true,
			wantURL:    "rabbit://nova:secret@rabbitmq.openstack.svc:5672/?ssl=0",
		},
		{
			// Per-pod hosts already carry their own port (host:port), e.g. when
			// several per-pod services share a single LoadBalancer IP on distinct
			// ports. The embedded port must be used verbatim, not the shared one.
			name:       "per-pod hosts with embedded ports",
			username:   "nova",
			password:   "p@ss",
			hosts:      []string{"r-server-0.openstack.svc:5672", "r-server-1.openstack.svc:5673", "r-server-2.openstack.svc:5674"},
			port:       "5672",
			vhost:      "/",
			tlsEnabled: false,
			wantURL:    "rabbit://nova:p%40ss@r-server-0.openstack.svc:5672,nova:p%40ss@r-server-1.openstack.svc:5673,nova:p%40ss@r-server-2.openstack.svc:5674/?ssl=0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := r.createTransportURLSecret(
				instance, tt.username, tt.password,
				tt.hosts, tt.port, tt.vhost,
				tt.tlsEnabled, tt.quorum,
			)

			gotURL := string(secret.Data["transport_url"])
			if gotURL != tt.wantURL {
				t.Errorf("transport_url mismatch\n  got:  %s\n  want: %s", gotURL, tt.wantURL)
			}

			if tt.quorum {
				if string(secret.Data["quorumqueues"]) != "true" {
					t.Error("expected quorumqueues=true in secret data")
				}
			} else {
				if _, ok := secret.Data["quorumqueues"]; ok {
					t.Error("unexpected quorumqueues key in secret data")
				}
			}
		})
	}
}
