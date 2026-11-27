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
	"fmt"
	"strings"
	"testing"

	"k8s.io/utils/ptr"
)

func quorumQueue(name string, members, online []string) Queue {
	return Queue{Name: name, Vhost: "/", Type: QueueTypeQuorum, Members: members, Online: online}
}

func TestQuorumOf(t *testing.T) {
	for _, tc := range []struct{ members, want int }{
		{0, 0}, {-1, 0}, {1, 1}, {2, 2}, {3, 2}, {4, 3}, {5, 3},
	} {
		if got := QuorumOf(tc.members); got != tc.want {
			t.Errorf("QuorumOf(%d) = %d, want %d", tc.members, got, tc.want)
		}
	}
}

func TestQuorumQueueMembers(t *testing.T) {
	members, online, err := QuorumQueueMembers(
		quorumQueue("q", []string{"a", "b", "c"}, []string{"a", "b"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 3 || !members["c"] {
		t.Errorf("configured members = %v, want a,b,c", members)
	}
	if len(online) != 2 || online["c"] {
		t.Errorf("online members = %v, want a,b", online)
	}
}

// If the member list does not add up, say so. Turning it into a number
// anyway would make a queue whose replication nobody knows look healthy.
func TestQuorumQueueMembersRejectsUncountableMembership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		queue Queue
	}{
		{"no configured members", quorumQueue("q", nil, []string{})},
		{"online is absent rather than empty", Queue{
			Name: "q", Vhost: "/", Type: QueueTypeQuorum, Members: []string{"a"}}},
		{"a configured member repeats", quorumQueue("q", []string{"a", "a"}, []string{"a"})},
		{"a configured member is empty", quorumQueue("q", []string{"a", ""}, []string{"a"})},
		{"an online member repeats", quorumQueue("q", []string{"a", "b"}, []string{"a", "a"})},
		{"online holds a non-member", quorumQueue("q", []string{"a"}, []string{"a", "b"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := QuorumQueueMembers(tc.queue); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestSummarizeQueuesCountsByKind(t *testing.T) {
	summary := SummarizeQueues([]Queue{
		quorumQueue("full", []string{"a", "b", "c"}, []string{"a", "b", "c"}),
		{Name: "classic", Vhost: "/", Type: QueueTypeClassic, Node: "a", Durable: ptr.To(true)},
		{Name: "stream", Vhost: "/", Type: QueueTypeStream, Members: []string{"a"}},
	})
	if summary.Total != 3 || summary.Quorum != 1 || summary.Classic != 1 || summary.Stream != 1 {
		t.Errorf("unexpected counts: %+v", summary)
	}
	if summary.UnderReplicated != 0 || summary.BelowQuorum != 0 || len(summary.Degraded) != 0 {
		t.Errorf("a fully replicated cluster reported degradation: %+v", summary)
	}
}

// A queue with two of its three copies still works. One with a single copy
// left does not. These are counted separately because only the second means
// clients are affected.
func TestSummarizeQueuesSeparatesDegradedFromUnavailable(t *testing.T) {
	summary := SummarizeQueues([]Queue{
		quorumQueue("missing-one", []string{"a", "b", "c"}, []string{"a", "b"}),
		quorumQueue("missing-two", []string{"a", "b", "c"}, []string{"a"}),
	})
	if summary.UnderReplicated != 2 {
		t.Errorf("UnderReplicated = %d, want 2", summary.UnderReplicated)
	}
	if summary.BelowQuorum != 1 {
		t.Errorf("BelowQuorum = %d, want 1", summary.BelowQuorum)
	}
	// The default vhost is itself named "/", so a queue on it reads as "//name".
	if len(summary.Degraded) != 2 || summary.Degraded[0] != "//missing-one" {
		t.Errorf("Degraded = %v, want both queues sorted", summary.Degraded)
	}
}

// This list ends up in status, so it has to stay short and in a predictable
// order however many queues the cluster has.
func TestSummarizeQueuesCapsAndSortsTheSample(t *testing.T) {
	queues := []Queue{}
	for i := SummarySampleLimit * 2; i > 0; i-- {
		queues = append(queues, quorumQueue(fmt.Sprintf("q%02d", i), []string{"a", "b"}, []string{"a"}))
	}
	summary := SummarizeQueues(queues)
	if summary.UnderReplicated != SummarySampleLimit*2 {
		t.Errorf("UnderReplicated = %d, want %d", summary.UnderReplicated, SummarySampleLimit*2)
	}
	if len(summary.Degraded) != SummarySampleLimit {
		t.Fatalf("sample length = %d, want %d", len(summary.Degraded), SummarySampleLimit)
	}
	for i := 1; i < len(summary.Degraded); i++ {
		if summary.Degraded[i-1] >= summary.Degraded[i] {
			t.Fatalf("sample is not sorted: %v", summary.Degraded)
		}
	}
}

// One queue that cannot be read should not throw away the counts for the
// others, and should not be passed over without a word either.
func TestSummarizeQueuesReportsUnreadableQueueAndKeepsCounting(t *testing.T) {
	summary := SummarizeQueues([]Queue{
		quorumQueue("broken", []string{"a", "a"}, []string{"a"}),
		quorumQueue("healthy", []string{"a", "b", "c"}, []string{"a", "b", "c"}),
	})
	if summary.Err == nil {
		t.Fatal("expected the unreadable queue to be reported")
	}
	if !strings.Contains(summary.Err.Error(), "/broken") {
		t.Errorf("error does not name the queue: %v", summary.Err)
	}
	if summary.Quorum != 2 {
		t.Errorf("Quorum = %d, want both queues counted", summary.Quorum)
	}
}
