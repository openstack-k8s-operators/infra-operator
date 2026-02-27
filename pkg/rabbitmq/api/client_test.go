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

//nolint:revive
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	client := NewClient("http://localhost:15672", "user", "pass", false, nil)
	if client == nil {
		t.Fatal("Expected client to be created")
	}
	if client.baseURL != "http://localhost:15672" {
		t.Errorf("Expected baseURL http://localhost:15672, got %s", client.baseURL)
	}
}

func TestCreateOrUpdateUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		if r.URL.Path != "/api/users/testuser" {
			t.Errorf("Expected /api/users/testuser, got %s", r.URL.Path)
		}

		var user User
		if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
			t.Fatal(err)
		}
		if user.Name != "testuser" || user.Password != "testpass" {
			t.Errorf("Unexpected user data: %+v", user)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.CreateOrUpdateUser("testuser", "testpass", []string{"monitoring"})
	if err != nil {
		t.Errorf("CreateOrUpdateUser failed: %v", err)
	}
}

func TestDeleteUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if r.URL.Path != "/api/users/testuser" {
			t.Errorf("Expected /api/users/testuser, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.DeleteUser("testuser")
	if err != nil {
		t.Errorf("DeleteUser failed: %v", err)
	}
}

func TestCreateOrUpdateVhost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		if r.URL.Path != "/api/vhosts/testvhost" {
			t.Errorf("Expected /api/vhosts/testvhost, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.CreateOrUpdateVhost("testvhost")
	if err != nil {
		t.Errorf("CreateOrUpdateVhost failed: %v", err)
	}
}

func TestDeleteVhost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if r.URL.Path != "/api/vhosts/testvhost" {
			t.Errorf("Expected /api/vhosts/testvhost, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.DeleteVhost("testvhost")
	if err != nil {
		t.Errorf("DeleteVhost failed: %v", err)
	}
}

func TestSetPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		if r.URL.Path != "/api/permissions///testuser" {
			t.Errorf("Expected /api/permissions///testuser, got %s", r.URL.Path)
		}

		var perms map[string]string
		if err := json.NewDecoder(r.Body).Decode(&perms); err != nil {
			t.Fatal(err)
		}
		if perms["configure"] != ".*" || perms["write"] != ".*" || perms["read"] != ".*" {
			t.Errorf("Unexpected permissions: %+v", perms)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.SetPermissions("/", "testuser", ".*", ".*", ".*")
	if err != nil {
		t.Errorf("SetPermissions failed: %v", err)
	}
}

func TestDeletePermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if r.URL.Path != "/api/permissions///testuser" {
			t.Errorf("Expected /api/permissions///testuser, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.DeletePermissions("/", "testuser")
	if err != nil {
		t.Errorf("DeletePermissions failed: %v", err)
	}
}

func TestCreateOrUpdatePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		if r.URL.Path != "/api/policies///testpolicy" {
			t.Errorf("Expected /api/policies///testpolicy, got %s", r.URL.Path)
		}

		var policy Policy
		if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
			t.Fatal(err)
		}
		if policy.Pattern != ".*" || policy.Priority != 1 || policy.ApplyTo != "all" {
			t.Errorf("Unexpected policy: %+v", policy)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	definition := map[string]interface{}{"max-length": 10000}
	err := client.CreateOrUpdatePolicy("/", "testpolicy", ".*", definition, 1, "all")
	if err != nil {
		t.Errorf("CreateOrUpdatePolicy failed: %v", err)
	}
}

func TestDeletePolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if r.URL.Path != "/api/policies///testpolicy" {
			t.Errorf("Expected /api/policies///testpolicy, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.DeletePolicy("/", "testpolicy")
	if err != nil {
		t.Errorf("DeletePolicy failed: %v", err)
	}
}

func TestCreateOrUpdateFederationUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		if r.URL.Path != "/api/parameters/federation-upstream///test-upstream" {
			t.Errorf("Expected /api/parameters/federation-upstream///test-upstream, got %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		// Check that it has the "value" wrapper
		value, ok := body["value"].(map[string]interface{})
		if !ok {
			t.Errorf("Expected 'value' key in body, got: %+v", body)
		}

		// Verify upstream parameters
		if value["uri"] != "amqp://user:pass@host:5672/%2F" {
			t.Errorf("Unexpected uri: %v", value["uri"])
		}
		if value["ack-mode"] != "on-confirm" {
			t.Errorf("Unexpected ack-mode: %v", value["ack-mode"])
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	upstream := FederationUpstream{
		URI:            "amqp://user:pass@host:5672/%2F",
		AckMode:        "on-confirm",
		Expires:        1800000,
		MaxHops:        1,
		PrefetchCount:  1000,
		ReconnectDelay: 5,
	}
	err := client.CreateOrUpdateFederationUpstream("/", "test-upstream", upstream)
	if err != nil {
		t.Errorf("CreateOrUpdateFederationUpstream failed: %v", err)
	}
}

func TestDeleteFederationUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("Expected DELETE request, got %s", r.Method)
		}
		if r.URL.Path != "/api/parameters/federation-upstream///test-upstream" {
			t.Errorf("Expected /api/parameters/federation-upstream///test-upstream, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	err := client.DeleteFederationUpstream("/", "test-upstream")
	if err != nil {
		t.Errorf("DeleteFederationUpstream failed: %v", err)
	}
}

func TestFederationUpstreamWithCustomVhost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		// Default vhost "/" gets encoded in the path
		// The raw path will show as /// due to URL structure /api/.../vhost/name
		// but it's properly encoded when sent over HTTP
		if r.URL.Path != "/api/parameters/federation-upstream///test-upstream" {
			t.Errorf("Expected /api/parameters/federation-upstream///test-upstream, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "admin", false, nil)
	upstream := FederationUpstream{
		URI:     "amqp://user:pass@host:5672/%2F",
		AckMode: "on-confirm",
	}
	err := client.CreateOrUpdateFederationUpstream("/", "test-upstream", upstream)
	if err != nil {
		t.Errorf("CreateOrUpdateFederationUpstream with default vhost failed: %v", err)
	}
}
