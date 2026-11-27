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
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The brokers used throughout: a, b and c are safe survivors, d is the
// deletion candidate and must never be allowed to make a queue look healthy.
var proofSafeNodes = map[string]bool{"a": true, "b": true, "c": true}

func proofQueue(members ...string) Queue {
	return Queue{Name: "orders", Vhost: "/", Type: QueueTypeQuorum, Members: members}
}

// participant is one broker's Ra state as the Erlang program reports it.
func participant(node, state string, term int64, leader string, members int) queueQuorumParticipant {
	return queueQuorumParticipant{
		Node: node, State: state, Membership: "voter",
		Term: term, Leader: leader, Members: members,
	}
}

// proofReply builds the JSON the in-pod Erlang program prints, so the Go side
// that judges it can be tested without a broker.
func proofReply(results ...queueQuorumReadResult) string {
	encoded, err := json.Marshal(results)
	if err != nil {
		panic(err)
	}
	return "QUEUE_CONSENSUS=" + string(encoded)
}

func healthyResult() queueQuorumReadResult {
	return queueQuorumReadResult{
		Vhost: "/", Name: "orders", ExpectedMembers: 3,
		Acknowledged: []string{"a", "b"},
		Participants: []queueQuorumParticipant{
			participant("a", "leader", 5, "a", 3),
			participant("b", "follower", 5, "a", 3),
		},
	}
}

func proveWith(t *testing.T, reply string, queues ...Queue) error {
	t.Helper()
	f := &fakeExec{replies: map[string]string{"eval": reply}}
	return execClient(f).ProveQueueQuorum(context.Background(),
		brokerPod("rabbitmq-server-0"), queues, proofSafeNodes)
}

func TestProveQueueQuorumAcceptsAnAgreeingMajority(t *testing.T) {
	if err := proveWith(t, proofReply(healthyResult()), proofQueue("a", "b", "d")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The candidate is excluded before the Erlang program runs, so a queue whose
// surviving members cannot themselves form a majority is refused without
// asking the cluster anything.
func TestProveQueueQuorumRefusesWhenTooFewSafeMembersRemain(t *testing.T) {
	err := proveWith(t, proofReply(healthyResult()), proofQueue("a", "d", "e"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "fewer safe Ra members than a majority") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProveQueueQuorumRejectsUnusableAnswers(t *testing.T) {
	healthy := healthyResult()

	noAck := healthy
	noAck.Acknowledged = []string{"a"}

	repeatedAck := healthy
	repeatedAck.Acknowledged = []string{"a", "a"}

	emptyAck := healthy
	emptyAck.Acknowledged = []string{"a", ""}

	noMembers := healthy
	noMembers.ExpectedMembers = 0

	reportedError := healthy
	reportedError.Error = "badarg"

	// Two brokers each calling themselves leader is a split view, not a quorum.
	twoLeaders := healthy
	twoLeaders.Participants = []queueQuorumParticipant{
		participant("a", "leader", 5, "a", 3),
		participant("b", "leader", 5, "a", 3),
	}

	// A follower on an older term has not caught up with the others.
	disagreeingTerms := healthy
	disagreeingTerms.Participants = []queueQuorumParticipant{
		participant("a", "leader", 5, "a", 3),
		participant("b", "follower", 4, "a", 3),
	}

	// Only the candidate acknowledges, so the majority depends on the broker
	// that is about to be removed.
	candidateOnly := healthy
	candidateOnly.Acknowledged = []string{"a", "d"}
	candidateOnly.Participants = []queueQuorumParticipant{
		participant("a", "leader", 5, "a", 3),
		participant("d", "follower", 5, "a", 3),
	}

	// A non-voter cannot be counted towards a majority.
	nonVoter := healthy
	nonVoter.Participants = []queueQuorumParticipant{
		participant("a", "leader", 5, "a", 3),
		{Node: "b", State: "follower", Membership: "promotable", Term: 5, Leader: "a", Members: 3},
	}

	for _, tc := range []struct {
		name   string
		result queueQuorumReadResult
	}{
		{"too few acknowledgements", noAck},
		{"the same broker acknowledges twice", repeatedAck},
		{"an acknowledgement has no broker", emptyAck},
		{"configured membership is zero", noMembers},
		{"the program reported an error", reportedError},
		{"two brokers claim to lead", twoLeaders},
		{"a follower is on an older term", disagreeingTerms},
		{"only the candidate acknowledges", candidateOnly},
		{"a non-voter is counted", nonVoter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := proveWith(t, proofReply(tc.result), proofQueue("a", "b", "d"))
			if err == nil {
				t.Fatal("expected the queue to be refused, got nil")
			}
		})
	}
}

// Every queue has to be accounted for. A reply that simply omits one, or
// answers about a queue nobody asked about, cannot be matched up.
func TestProveQueueQuorumRequiresAnAnswerForEveryQueue(t *testing.T) {
	err := proveWith(t, proofReply(healthyResult()),
		proofQueue("a", "b", "d"),
		Queue{Name: "payments", Vhost: "/", Type: QueueTypeQuorum, Members: []string{"a", "b", "d"}})
	if err == nil {
		t.Fatal("expected an error when a queue has no answer, got nil")
	}
}

// Queues are asked about in batches so one enormous command is never built.
// The batch boundary is a place where a queue could be dropped, so check that
// a set spanning several batches is fully covered.
func TestProveQueueQuorumBatchesWithoutLosingQueues(t *testing.T) {
	const total = 70
	queues := make([]Queue, 0, total)
	results := make([]queueQuorumReadResult, 0, total)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("q%03d", i)
		queues = append(queues, Queue{
			Name: name, Vhost: "/", Type: QueueTypeQuorum, Members: []string{"a", "b", "d"}})
		result := healthyResult()
		result.Name = name
		results = append(results, result)
	}
	// Each batch is answered only for the queues it asked about, which is what
	// a real broker would do, and is what makes a dropped queue visible.
	batch := 0
	f := &fakeExec{replyFunc: func([]string) string {
		start := batch * 32
		end := min(start+32, len(results))
		batch++
		return proofReply(results[start:end]...)
	}}
	if err := execClient(f).ProveQueueQuorum(context.Background(),
		brokerPod("rabbitmq-server-0"), queues, proofSafeNodes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.calls) < 2 {
		t.Fatalf("got %d commands for %d queues, expected several batches", len(f.calls), total)
	}
	// Resource names travel as base64 binaries, never as Erlang source.
	for _, call := range f.calls {
		joined := strings.Join(call.command, " ")
		if strings.Contains(joined, "q000\"") || strings.Contains(joined, "<<\"q0") {
			t.Fatalf("a queue name was interpolated into the Erlang program: %q", joined)
		}
		if !strings.Contains(joined, "base64:decode") {
			t.Fatalf("expected base64-encoded arguments, got %q", joined)
		}
	}
}
