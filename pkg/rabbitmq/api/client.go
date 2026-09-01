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

// Package api provides a client for the RabbitMQ Management HTTP API.
//
//nolint:revive
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a RabbitMQ Management API client
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// Timeout constants for RabbitMQ Management API operations
const (
	// DefaultAPITimeout is the default timeout for most API operations
	DefaultAPITimeout = 30 * time.Second

	// DeleteTimeout is the timeout for delete operations which may take longer
	// (e.g., deleting vhosts with many queues)
	DeleteTimeout = 60 * time.Second

	// maxResponseBodySize limits how much of an error response body we read (1 MB)
	maxResponseBodySize = 1 << 20
)

// User represents a RabbitMQ user
type User struct {
	Name     string   `json:"name"`
	Password string   `json:"password"`
	Tags     []string `json:"tags"`
}

// Vhost represents a RabbitMQ virtual host
type Vhost struct {
	Name string `json:"name"`
}

// Permission represents RabbitMQ permissions
type Permission struct {
	User      string `json:"user"`
	Vhost     string `json:"vhost"`
	Configure string `json:"configure"`
	Write     string `json:"write"`
	Read      string `json:"read"`
}

// Policy represents a RabbitMQ policy
type Policy struct {
	Pattern    string                 `json:"pattern"`
	Definition map[string]interface{} `json:"definition"`
	Priority   int                    `json:"priority"`
	ApplyTo    string                 `json:"apply-to"`
}

// NewClient creates a new RabbitMQ Management API client
func NewClient(baseURL, username, password string, tlsEnabled bool, caCert []byte) (*Client, error) {
	httpClient := &http.Client{}

	if tlsEnabled && len(caCert) > 0 {
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate PEM data")
		}

		// Pin TLS 1.3 as the minimum. Do NOT lower this to TLS 1.2: only TLS 1.3
		// carries the hybrid post-quantum key exchange (X25519MLKEM768), which the
		// Go stdlib negotiates by default. Do NOT set CurvePreferences here —
		// leaving it unset keeps the post-quantum-capable default; a classical-only
		// list would silently disable PQ key exchange.
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				RootCAs:    caCertPool,
			},
		}
	}

	return &Client{
		baseURL:    baseURL,
		username:   username,
		password:   password,
		httpClient: httpClient,
	}, nil
}

// doRequest performs an HTTP request with authentication using the default timeout
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	return c.doRequestWithTimeout(ctx, method, path, body, DefaultAPITimeout)
}

// doRequestWithTimeout performs an HTTP request with authentication using a custom timeout
func (c *Client) doRequestWithTimeout(ctx context.Context, method, path string, body interface{}, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	apiURL := fmt.Sprintf("%s%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, method, apiURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// readAndCloseBody drains and closes a response body, reading up to maxResponseBodySize
// on error status codes for diagnostic messages
func readErrorBody(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	return string(body)
}

// drainAndClose fully drains and closes resp.Body to allow HTTP connection reuse
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// CreateOrUpdateUser creates or updates a RabbitMQ user
func (c *Client) CreateOrUpdateUser(ctx context.Context, name, password string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}

	user := User{
		Name:     name,
		Password: password,
		Tags:     tags,
	}

	encodedName := url.PathEscape(name)
	resp, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/users/%s", encodedName), user)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to create/update user %s: status %d, body: %s", name, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// DeleteUser deletes a RabbitMQ user
func (c *Client) DeleteUser(ctx context.Context, name string) error {
	encodedName := url.PathEscape(name)
	resp, err := c.doRequestWithTimeout(ctx, "DELETE", fmt.Sprintf("/api/users/%s", encodedName), nil, DeleteTimeout)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete user %s: status %d, body: %s", name, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// CreateOrUpdateVhost creates or updates a RabbitMQ vhost
func (c *Client) CreateOrUpdateVhost(ctx context.Context, name string) error {
	vhost := Vhost{
		Name: name,
	}

	encodedName := url.PathEscape(name)
	resp, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/vhosts/%s", encodedName), vhost)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to create/update vhost %s: status %d, body: %s", name, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// DeleteVhost deletes a RabbitMQ vhost
func (c *Client) DeleteVhost(ctx context.Context, name string) error {
	encodedName := url.PathEscape(name)
	resp, err := c.doRequestWithTimeout(ctx, "DELETE", fmt.Sprintf("/api/vhosts/%s", encodedName), nil, DeleteTimeout)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete vhost %s: status %d, body: %s", name, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// SetPermissions sets permissions for a user on a vhost
func (c *Client) SetPermissions(ctx context.Context, vhost, user, configure, write, read string) error {
	// The request body should only contain the permission fields, not user/vhost
	perm := map[string]string{
		"configure": configure,
		"write":     write,
		"read":      read,
	}

	// URL encode vhost and user (vhost "/" becomes "%2F")
	encodedVhost := url.PathEscape(vhost)
	encodedUser := url.PathEscape(user)

	resp, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/permissions/%s/%s", encodedVhost, encodedUser), perm)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to set permissions for user %s on vhost %s: status %d, body: %s", user, vhost, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// DeletePermissions deletes permissions for a user on a vhost
func (c *Client) DeletePermissions(ctx context.Context, vhost, user string) error {
	encodedVhost := url.PathEscape(vhost)
	encodedUser := url.PathEscape(user)
	resp, err := c.doRequestWithTimeout(ctx, "DELETE", fmt.Sprintf("/api/permissions/%s/%s", encodedVhost, encodedUser), nil, DeleteTimeout)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete permissions for user %s on vhost %s: status %d, body: %s", user, vhost, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// CreateOrUpdatePolicy creates or updates a RabbitMQ policy
func (c *Client) CreateOrUpdatePolicy(ctx context.Context, vhost, name, pattern string, definition map[string]interface{}, priority int, applyTo string) error {
	if applyTo == "" {
		applyTo = "all"
	}

	policy := Policy{
		Pattern:    pattern,
		Definition: definition,
		Priority:   priority,
		ApplyTo:    applyTo,
	}

	encodedVhost := url.PathEscape(vhost)
	encodedName := url.PathEscape(name)
	resp, err := c.doRequest(ctx, "PUT", fmt.Sprintf("/api/policies/%s/%s", encodedVhost, encodedName), policy)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to create/update policy %s on vhost %s: status %d, body: %s", name, vhost, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}

// DeletePolicy deletes a RabbitMQ policy
func (c *Client) DeletePolicy(ctx context.Context, vhost, name string) error {
	encodedVhost := url.PathEscape(vhost)
	encodedName := url.PathEscape(name)
	resp, err := c.doRequestWithTimeout(ctx, "DELETE", fmt.Sprintf("/api/policies/%s/%s", encodedVhost, encodedName), nil, DeleteTimeout)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to delete policy %s on vhost %s: status %d, body: %s", name, vhost, resp.StatusCode, readErrorBody(resp))
	}

	return nil
}
