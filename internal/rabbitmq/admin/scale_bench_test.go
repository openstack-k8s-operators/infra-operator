package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func syntheticQueueBody(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		// Node names are the fully qualified form a real cluster reports,
		// which is most of the size of each entry: 459 bytes per queue in
		// the body this builds.
		fmt.Fprintf(&b, `{"name":"cinder-volume.scheduler_fanout_%032d","vhost":"/","type":"quorum",`+
			`"node":"rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack","durable":true,`+
			`"members":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack",`+
			`"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack",`+
			`"rabbit@rabbitmq-server-2.rabbitmq-nodes.openstack"],`+
			`"online":["rabbit@rabbitmq-server-0.rabbitmq-nodes.openstack",`+
			`"rabbit@rabbitmq-server-1.rabbitmq-nodes.openstack"]}`, i)
	}
	b.WriteString("]")
	return b.String()
}

func BenchmarkQueuesAtScale(b *testing.B) {
	// The largest case stays below maxResponseBytes (16 MiB, about 36,000
	// queues): get rejects anything larger, so the benchmark would fail
	// instead of measuring. BenchmarkDecodeOnly covers larger bodies.
	for _, n := range []int{1000, 10000, 30000} {
		body := syntheticQueueBody(n)
		b.Run(fmt.Sprintf("queues=%d/bytes=%d", n, len(body)), func(b *testing.B) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := NewWithCreds(testInstance(), Creds{Username: "u", Password: "p"}).
				WithHTTPClient(&http.Client{Transport: rewriteTo(server.URL, http.DefaultTransport)})

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				queues, err := client.Queues(context.Background(), testPod(), ColumnsReplication)
				if err != nil {
					b.Fatal(err)
				}
				summary := SummarizeQueues(queues)
				if summary.UnderReplicated != n {
					b.Fatalf("got %d under-replicated, want %d", summary.UnderReplicated, n)
				}
			}
		})
	}
}

func BenchmarkDecodeOnly(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		body := []byte(syntheticQueueBody(n))
		b.Run(fmt.Sprintf("queues=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var queues []Queue
				if err := json.Unmarshal(body, &queues); err != nil {
					b.Fatal(err)
				}
				_ = SummarizeQueues(queues)
			}
		})
	}
}
