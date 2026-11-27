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

// Package admin asks a running RabbitMQ cluster about its own state.
//
// Kubernetes can tell you whether the broker pods started. It cannot tell you
// whether those brokers joined into one cluster, or whether a queue has all of
// its replicas, because RabbitMQ keeps track of that itself. This package asks
// RabbitMQ over its management HTTP API and returns the answers as ordinary Go
// values.
//
// Nothing here decides anything. It reports what the cluster said and leaves
// the caller to work out what to do about it.
package admin

import (
	"context"
	cryptotls "crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// requestTimeout limits a request whose cost does not grow with the size
	// of the cluster, such as listing brokers. Without a limit, a broker that
	// accepts the connection and then goes quiet would hold up a reconcile for
	// as long as it stayed quiet.
	requestTimeout = 15 * time.Second
	// listTimeout limits listing the queues, which RabbitMQ takes longer to
	// answer the more queues there are. Measured against RabbitMQ 4.2 on a
	// three-broker cluster, the listing costs about half a millisecond per
	// queue, so ten thousand queues take around five seconds. Sharing
	// requestTimeout would mean the largest clusters were the ones we never
	// managed to read.
	listTimeout = 60 * time.Second
	// maxResponseBytes limits how much of a response is read, so that a very
	// large answer cannot turn into operator memory without bound. Each queue
	// in the listing costs about 500 bytes, most of it the fully qualified
	// broker names, so this allows for roughly 34,000 queues.
	maxResponseBytes = 16 << 20
)

// Creds is what is needed to reach the management API of one cluster: the
// login, and how to check the server's certificate when it uses TLS.
type Creds struct {
	Username string
	Password string
	// TLS is true when the management endpoint uses HTTPS, which happens when
	// spec.tls.secretName is set on the RabbitMq.
	TLS bool
	// CAPEM is the CA used to check the server's certificate. When it is
	// empty, the system CA store is used instead.
	CAPEM []byte
}

// Client talks to the management API of one RabbitMq cluster.
//
// It holds a separate HTTP client per pod, because the name checked during the
// TLS handshake is built from the pod's own hostname. Keeping them around lets
// repeated reads of the same pod reuse a connection instead of setting up a
// new one each time.
//
// A Client is not safe to use from several goroutines at once.
type Client struct {
	instance *rabbitmqv1.RabbitMq
	creds    Creds

	// httpOverride, when set, is used for every request instead of a client
	// built from creds. Tests point it at a stub server; in production it is
	// nil.
	httpOverride *http.Client
	perPod       map[string]*http.Client

	// Set by WithExec, for the commands that have to run inside a broker pod.
	kclient kubernetes.Interface
	config  *rest.Config
	exec    ExecFunc
}

// New builds a Client for one cluster and reads its credentials using the
// reader it is given.
//
// The reader is an argument so the caller can choose where the credentials
// come from. A caller about to do something it cannot undo can pass a reader
// that skips the cache; ordinary reporting can use the cached one.
func New(ctx context.Context, reader client.Reader, instance *rabbitmqv1.RabbitMq) (*Client, error) {
	creds, err := LoadCreds(ctx, reader, instance)
	if err != nil {
		return nil, err
	}
	return NewWithCreds(instance, *creds), nil
}

// NewWithCreds builds a Client from credentials the caller already has.
func NewWithCreds(instance *rabbitmqv1.RabbitMq, creds Creds) *Client {
	return &Client{instance: instance, creds: creds, perPod: map[string]*http.Client{}}
}

// WithHTTPClient makes every request go through the supplied client. It exists
// so tests can point a Client at an httptest server.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpOverride = httpClient
	return c
}

// LoadCreds reads the cluster's default-user secret and, when the management
// endpoint uses TLS, the secret holding the CA that signs its certificate.
//
// If spec.tls.caSecretName is not set, the TLS secret is checked for a ca.crt
// of its own, which is where most deployments keep it. If caSecretName is set
// but that secret has no usable ca.crt, this fails instead of quietly falling
// back to the system CA store: someone asked for a particular CA to be used,
// and silently using a different one would not be what they asked for.
func LoadCreds(ctx context.Context, reader client.Reader, instance *rabbitmqv1.RabbitMq) (*Creds, error) {
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("%s-default-user", instance.Name),
		Namespace: instance.Namespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("read default-user secret: %w", err)
	}
	user := string(secret.Data["username"])
	pass := string(secret.Data["password"])
	if user == "" || pass == "" {
		return nil, fmt.Errorf("default-user secret %s-default-user missing username/password", instance.Name)
	}
	creds := &Creds{Username: user, Password: pass}
	if instance.Spec.TLS.SecretName == "" {
		return creds, nil
	}
	creds.TLS = true
	caSecretName := instance.Spec.TLS.CaSecretName
	if caSecretName == "" {
		caSecretName = instance.Spec.TLS.SecretName
	}
	caSecret := &corev1.Secret{}
	if err := reader.Get(ctx, types.NamespacedName{
		Name:      caSecretName,
		Namespace: instance.Namespace,
	}, caSecret); err != nil {
		return nil, fmt.Errorf("read CA secret %s: %w", caSecretName, err)
	}
	caPEM, present := caSecret.Data["ca.crt"]
	if present && len(caPEM) == 0 {
		return nil, fmt.Errorf("RabbitMq CA secret %s has an empty ca.crt", caSecretName)
	}
	if instance.Spec.TLS.CaSecretName != "" && !present {
		return nil, fmt.Errorf("RabbitMq CA secret %s has no ca.crt", caSecretName)
	}
	creds.CAPEM = caPEM
	return creds, nil
}

// httpClientFor returns the HTTP client for one pod, building it the first
// time that pod is read. The name checked during the TLS handshake has to be
// the one in the pod's certificate, which is its headless-service hostname
// without the trailing .svc.
func (c *Client) httpClientFor(pod *corev1.Pod) (*http.Client, error) {
	if c.httpOverride != nil {
		return c.httpOverride, nil
	}
	if existing, ok := c.perPod[pod.Name]; ok {
		return existing, nil
	}
	transport := &http.Transport{
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if c.creds.TLS {
		tlsCfg := &cryptotls.Config{
			MinVersion: cryptotls.VersionTLS12,
			ServerName: rabbitmq.ManagementPodServerName(pod.Name, c.instance.Name, c.instance.Namespace),
		}
		if len(c.creds.CAPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(c.creds.CAPEM) {
				return nil, fmt.Errorf("failed to parse RabbitMQ CA certificate")
			}
			tlsCfg.RootCAs = pool
		}
		transport.TLSClientConfig = tlsCfg
	}
	// Each request sets its own deadline on the context, so the client-wide
	// limit only has to be loose enough not to cut the longest of them short.
	httpClient := &http.Client{Transport: transport, Timeout: listTimeout}
	c.perPod[pod.Name] = httpClient
	return httpClient, nil
}

// countingReader records how many bytes were read through it, so that a
// response cut short by maxResponseBytes can be told apart from one that was
// malformed to begin with.
type countingReader struct {
	inner io.Reader
	read  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	c.read += int64(n)
	return n, err
}

// get makes one management API request to a pod and decodes the JSON it
// returns into result. timeout is how long the request may take.
func (c *Client) get(
	ctx context.Context,
	pod *corev1.Pod,
	path string,
	timeout time.Duration,
	result interface{},
) (retErr error) {
	httpClient, err := c.httpClientFor(pod)
	if err != nil {
		return err
	}
	scheme, port := "http", rabbitmq.ManagementPort
	if c.creds.TLS {
		scheme, port = "https", rabbitmq.ManagementTLSPort
	}
	// DNS resolves the name with .svc on the end, but the certificate is
	// issued for the name without it.
	host := rabbitmq.ManagementPodServerName(pod.Name, c.instance.Name, c.instance.Namespace) + ".svc"
	url := fmt.Sprintf("%s://%s:%d%s", scheme, host, port, path)

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.creds.Username, c.creds.Password)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("close management API response body: %w", closeErr)
			} else {
				log.FromContext(ctx).V(1).Info(
					"Failed to close RabbitMQ management API response body", "error", closeErr)
			}
		}
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("management API %s returned %d: %s", url, resp.StatusCode, string(body))
	}
	counted := &countingReader{inner: io.LimitReader(resp.Body, maxResponseBytes+1)}
	if err := json.NewDecoder(counted).Decode(result); err != nil {
		if counted.read > maxResponseBytes {
			return fmt.Errorf(
				"management API %s returned more than %d bytes: this cluster holds too many queues to read in one response",
				path, maxResponseBytes)
		}
		return fmt.Errorf("parse management API %s: %w", path, err)
	}
	if counted.read > maxResponseBytes {
		return fmt.Errorf(
			"management API %s returned more than %d bytes: this cluster holds too many queues to read in one response",
			path, maxResponseBytes)
	}
	return nil
}
