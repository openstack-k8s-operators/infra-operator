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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// generateTestCACert returns a self-signed CA certificate in PEM form for use
// in TLS client tests.
func generateTestCACert(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	certTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
}

func TestNewClient(t *testing.T) {
	client, err := NewClient("http://localhost:15672", "user", "pass", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("Expected client to be created")
		return
	}
	if client.baseURL != "http://localhost:15672" {
		t.Errorf("Expected baseURL http://localhost:15672, got %s", client.baseURL)
	}
}

func TestNewClient_InvalidCACert(t *testing.T) {
	_, err := NewClient("https://localhost:15671", "user", "pass", true, []byte("not-valid-pem"))
	if err == nil {
		t.Fatal("Expected error for invalid CA cert PEM data")
	}
}

func TestNewClient_NoCACert_TLSEnabled(t *testing.T) {
	client, err := NewClient("https://localhost:15671", "user", "pass", true, nil)
	if err != nil {
		t.Fatalf("NewClient with TLS but no CA cert should succeed: %v", err)
	}
	if client == nil {
		t.Fatal("Expected client to be created")
	}

	// A TLS-enabled client must still pin TLS 1.3 even without a custom CA,
	// falling back to the system trust store (RootCAs nil).
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be set for a TLS-enabled client")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected MinVersion TLS 1.3 (%d), got %d", tls.VersionTLS13, transport.TLSClientConfig.MinVersion)
	}
	if transport.TLSClientConfig.RootCAs != nil {
		t.Error("expected RootCAs to be nil (system trust store) when no CA cert is supplied")
	}
}

// TestNewClient_NoTLS verifies a client created without TLS retains the default
// transport behavior (no custom TLS config is installed).
func TestNewClient_NoTLS(t *testing.T) {
	client, err := NewClient("http://localhost:15672", "user", "pass", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client.httpClient.Transport != nil {
		t.Errorf("expected default transport (nil) for a non-TLS client, got %T", client.httpClient.Transport)
	}
}

// TestNewClient_TLSConfig verifies the TLS client configuration used for
// outbound connections stays post-quantum ready: TLS 1.3 minimum and no
// override of CurvePreferences (leaving it unset preserves Go's hybrid
// post-quantum key exchange default, X25519MLKEM768).
func TestNewClient_TLSConfig(t *testing.T) {
	caCert := generateTestCACert(t)

	client, err := NewClient("https://localhost:15671", "user", "pass", true, caCert)
	if err != nil {
		t.Fatalf("NewClient with valid CA cert should succeed: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.httpClient.Transport)
	}
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		t.Fatal("expected TLSClientConfig to be set")
	}
	if tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected MinVersion TLS 1.3 (%d), got %d", tls.VersionTLS13, tlsConfig.MinVersion)
	}
	if tlsConfig.CurvePreferences != nil {
		t.Errorf("expected CurvePreferences to be unset to preserve the post-quantum default, got %v", tlsConfig.CurvePreferences)
	}
	if tlsConfig.RootCAs == nil {
		t.Error("expected RootCAs to be populated from the provided CA cert")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.CreateOrUpdateUser(context.Background(), "testuser", "testpass", []string{"monitoring"})
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.DeleteUser(context.Background(), "testuser")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.CreateOrUpdateVhost(context.Background(), "testvhost")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.DeleteVhost(context.Background(), "testvhost")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.SetPermissions(context.Background(), "/", "testuser", ".*", ".*", ".*")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.DeletePermissions(context.Background(), "/", "testuser")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	definition := map[string]interface{}{"max-length": 10000}
	err = client.CreateOrUpdatePolicy(context.Background(), "/", "testpolicy", ".*", definition, 1, "all")
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

	client, err := NewClient(server.URL, "admin", "admin", false, nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	err = client.DeletePolicy(context.Background(), "/", "testpolicy")
	if err != nil {
		t.Errorf("DeletePolicy failed: %v", err)
	}
}
