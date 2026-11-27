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

package functional_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// rabbitMQMgmt is the stub the RabbitMq controller reads during the suite.
// It is created once during setup, because the controller is handed its HTTP
// client when the manager is built.
var rabbitMQMgmt *rabbitMQManagementStub

// rabbitMQManagementStub stands in for the management API of every broker,
// since no RabbitMQ runs under envtest.
//
// The controller builds a URL from the pod's hostname, which does not resolve
// here, so the HTTP client this stub hands out sends every request to the
// stub instead. The controller still has to ask for the same paths it would
// ask a real broker, because that is what the stub matches on.
type rabbitMQManagementStub struct {
	server *httptest.Server

	mu sync.Mutex
	// nodes and queues are the JSON returned for the two endpoints the
	// controller reads. A test sets them before marking the cluster ready.
	nodes  string
	queues string
	// status, when it is not 200, is returned for every request, so a test can
	// reproduce a broker that is unreachable or refuses the login.
	status int
}

func newRabbitMQManagementStub() *rabbitMQManagementStub {
	stub := &rabbitMQManagementStub{nodes: "[]", queues: "[]", status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	return stub
}

func (s *rabbitMQManagementStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status, nodes, queues := s.status, s.nodes, s.queues
	s.mu.Unlock()

	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"stub"}`))
		return
	}
	body := ""
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/nodes"):
		body = nodes
	case strings.HasPrefix(r.URL.Path, "/api/queues/detailed"):
		body = queues
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// Respond sets what the next reads will return.
func (s *rabbitMQManagementStub) Respond(nodes, queues string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes, s.queues, s.status = nodes, queues, http.StatusOK
}

// Fail makes every endpoint return the given status.
func (s *rabbitMQManagementStub) Fail(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// HTTPClient returns a client that reaches the stub whatever host the caller
// asks for.
func (s *rabbitMQManagementStub) HTTPClient() *http.Client {
	return &http.Client{Transport: stubTransport{target: s.server.URL}}
}

func (s *rabbitMQManagementStub) Close() {
	s.server.Close()
}

type stubTransport struct {
	target string
}

func (t stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := http.NewRequest(req.Method, t.target, nil)
	if err != nil {
		return nil, err
	}
	rewritten := req.Clone(req.Context())
	rewritten.URL.Scheme = target.URL.Scheme
	rewritten.URL.Host = target.URL.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}
