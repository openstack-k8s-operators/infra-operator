/*
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

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testInstance() *rabbitmqv1.RabbitMq {
	return &rabbitmqv1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq", Namespace: "openstack"},
	}
}

func testPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "rabbitmq-server-0", Namespace: "openstack"}}
}

// stubCluster answers every management API path with the same canned
// response and points a Client at it, so the parsing and checking can be
// tested without a broker.
func stubCluster(t *testing.T, status int, body string) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing stub response: %v", err)
		}
	}))
	transport := &http.Transport{}
	httpClient := &http.Client{Transport: transport}
	// Send every request to the stub regardless of the pod hostname the client builds.
	httpClient.Transport = rewriteTo(server.URL, transport)
	client := NewWithCreds(testInstance(), Creds{Username: "u", Password: "p"}).
		WithHTTPClient(httpClient)
	return client, server.Close
}

type rewritingTransport struct {
	target string
	inner  http.RoundTripper
}

func (t rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	target, err := http.NewRequest(req.Method, t.target, nil)
	if err != nil {
		return nil, err
	}
	rewritten.URL.Scheme = target.URL.Scheme
	rewritten.URL.Host = target.URL.Host
	return t.inner.RoundTrip(rewritten)
}

func rewriteTo(target string, inner http.RoundTripper) http.RoundTripper {
	return rewritingTransport{target: target, inner: inner}
}

func TestNodesParsesMembership(t *testing.T) {
	client, stop := stubCluster(t, http.StatusOK,
		`[{"name":"rabbit@a","running":true},{"name":"rabbit@b","running":false}]`)
	defer stop()

	nodes, err := client.Nodes(context.Background(), testPod())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	running := RunningNodes(nodes)
	if len(running) != 1 || !running["rabbit@a"] {
		t.Errorf("running nodes = %v, want only rabbit@a", running)
	}
}

// A list that names the same broker twice, or leaves one unnamed, cannot be
// counted. Counting it anyway would give the wrong number of brokers.
func TestNodesRejectsAmbiguousMembership(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"duplicate node", `[{"name":"rabbit@a","running":true},{"name":"rabbit@a","running":true}]`},
		{"nameless node", `[{"name":"","running":true}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, stop := stubCluster(t, http.StatusOK, tc.body)
			defer stop()
			if _, err := client.Nodes(context.Background(), testPod()); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestQueuesParsesEveryKind(t *testing.T) {
	client, stop := stubCluster(t, http.StatusOK, `[
		{"name":"q1","vhost":"/","type":"quorum","members":["a","b","c"],"online":["a","b"]},
		{"name":"q2","vhost":"/","type":"classic","node":"a","durable":true},
		{"name":"q3","vhost":"other","type":"stream","members":["a"]}]`)
	defer stop()

	queues, err := client.Queues(context.Background(), testPod(), ColumnsReplication)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queues) != 3 {
		t.Fatalf("got %d queues, want 3", len(queues))
	}
	if queues[2].Ref() != "other/q3" {
		t.Errorf("Ref() = %q, want other/q3", queues[2].Ref())
	}
	if queues[1].Durable == nil || !*queues[1].Durable {
		t.Errorf("classic queue lost its durability flag")
	}
}

// A cluster with no queues and a response that failed to produce a list look
// the same unless an empty list is told apart from a missing one.
func TestQueuesDistinguishesEmptyFromAbsent(t *testing.T) {
	client, stop := stubCluster(t, http.StatusOK, `[]`)
	defer stop()
	queues, err := client.Queues(context.Background(), testPod(), ColumnsReplication)
	if err != nil {
		t.Fatalf("an empty cluster is not an error: %v", err)
	}
	if len(queues) != 0 {
		t.Errorf("got %d queues, want none", len(queues))
	}

	absent, stopAbsent := stubCluster(t, http.StatusOK, `null`)
	defer stopAbsent()
	if _, err := absent.Queues(context.Background(), testPod(), ColumnsReplication); err == nil {
		t.Fatal("a response that is not an array must be an error")
	}
}

func TestQueuesRejectsUnusableResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"queue without identity", `[{"name":"","vhost":"/","type":"quorum"}]`, http.StatusOK},
		{"same queue twice", `[{"name":"q","vhost":"/","type":"quorum"},` +
			`{"name":"q","vhost":"/","type":"quorum"}]`, http.StatusOK},
		{"not json", `<html>no</html>`, http.StatusOK},
		{"unauthorized", `{"error":"not_authorised"}`, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, stop := stubCluster(t, tc.status, tc.body)
			defer stop()
			if _, err := client.Queues(context.Background(), testPod(), ColumnsReplication); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func fakeReader(objects ...runtime.Object) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...)
}

func userSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "rabbitmq-default-user", Namespace: "openstack"},
		Data:       data,
	}
}

func TestLoadCredsWithoutTLS(t *testing.T) {
	reader := fakeReader(userSecret(map[string][]byte{
		"username": []byte("guest"), "password": []byte("secret")})).Build()

	creds, err := LoadCreds(context.Background(), reader, testInstance())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Username != "guest" || creds.Password != "secret" || creds.TLS {
		t.Errorf("unexpected creds: %+v", creds)
	}
}

func TestLoadCredsRequiresUsernameAndPassword(t *testing.T) {
	reader := fakeReader(userSecret(map[string][]byte{"username": []byte("guest")})).Build()
	if _, err := LoadCreds(context.Background(), reader, testInstance()); err == nil {
		t.Fatal("expected an error when the password is absent")
	}
}

// When no CA secret is named, the TLS secret is the usual place to find one.
// A deployment with neither falls back to the system CA store.
func TestLoadCredsFallsBackToTheTLSSecretForItsCA(t *testing.T) {
	instance := testInstance()
	instance.Spec.TLS.SecretName = "cert-rabbitmq"
	reader := fakeReader(
		userSecret(map[string][]byte{"username": []byte("u"), "password": []byte("p")}),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cert-rabbitmq", Namespace: "openstack"},
			Data:       map[string][]byte{"ca.crt": []byte("PEM")},
		}).Build()

	creds, err := LoadCreds(context.Background(), reader, instance)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !creds.TLS || string(creds.CAPEM) != "PEM" {
		t.Errorf("unexpected creds: %+v", creds)
	}
}

// Naming a CA secret is asking for that CA to be used. Quietly using the
// system CA store instead is not what was asked for, so a secret without a
// usable ca.crt is an error.
func TestLoadCredsRejectsNamedCASecretWithoutMaterial(t *testing.T) {
	instance := testInstance()
	instance.Spec.TLS.SecretName = "cert-rabbitmq"
	instance.Spec.TLS.CaSecretName = "ca-bundle"
	reader := fakeReader(
		userSecret(map[string][]byte{"username": []byte("u"), "password": []byte("p")}),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "ca-bundle", Namespace: "openstack"},
			Data:       map[string][]byte{"other.crt": []byte("PEM")},
		}).Build()

	_, err := LoadCreds(context.Background(), reader, instance)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "ca-bundle") {
		t.Errorf("error does not name the secret: %v", err)
	}
}

func TestLoadCredsRejectsEmptyCA(t *testing.T) {
	instance := testInstance()
	instance.Spec.TLS.SecretName = "cert-rabbitmq"
	reader := fakeReader(
		userSecret(map[string][]byte{"username": []byte("u"), "password": []byte("p")}),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "cert-rabbitmq", Namespace: "openstack"},
			Data:       map[string][]byte{"ca.crt": {}},
		}).Build()

	if _, err := LoadCreds(context.Background(), reader, instance); err == nil {
		t.Fatal("expected an error when ca.crt is present but empty")
	}
}

// A cluster can hold more queues than fit in one response we are willing to
// read. The error has to say that, because "unexpected end of JSON input"
// would send whoever reads it looking for a bug in RabbitMQ.
func TestGetRejectsAResponseTooLargeToRead(t *testing.T) {
	// Just over the limit: roughly 460 bytes per queue.
	client, stop := stubCluster(t, http.StatusOK, syntheticQueueBody(40000))
	defer stop()

	_, err := client.Queues(context.Background(), testPod(), ColumnsReplication)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "too many queues") {
		t.Errorf("error does not explain the limit: %v", err)
	}
}
