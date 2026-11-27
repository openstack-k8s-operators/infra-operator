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

package admin

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// Queue kinds as RabbitMQ names them in the management API.
const (
	QueueTypeClassic = "classic"
	QueueTypeQuorum  = "quorum"
	QueueTypeStream  = "stream"
)

// Column sets for Queues. RabbitMQ only works out the fields you ask for, so
// asking for the fewest that answer the question keeps the call cheap on a
// cluster with many queues.
const (
	// ColumnsReplication is enough to see how a queue is replicated and which
	// of its replicas are online.
	ColumnsReplication = "name,vhost,type,node,durable,members,online"
	// ColumnsMembership is enough to see which brokers hold a queue.
	ColumnsMembership = "name,vhost,type,members,online"
)

// Queue is one queue as RabbitMQ reports it.
//
// Members and Online apply to queue types that are replicated: Members is
// every broker the queue is meant to live on, Online the ones currently
// reachable. Node and Durable apply to classic queues, which have one copy
// living on one broker.
type Queue struct {
	Name    string   `json:"name"`
	Vhost   string   `json:"vhost"`
	Type    string   `json:"type"`
	Node    string   `json:"node"`
	Durable *bool    `json:"durable"`
	Members []string `json:"members"`
	Online  []string `json:"online"`
}

// Ref identifies a queue for reporting, as vhost/name.
func (q Queue) Ref() string {
	return q.Vhost + "/" + q.Name
}

// Queues returns every queue in the cluster, read from the broker in pod.
//
// They all come from one response. Fetching them a page at a time would be
// cheaper per request, but queues are created and deleted while the pages are
// being fetched, which moves everything along and can skip a queue entirely.
// A skipped queue is one whose replication nobody looked at.
//
// The answer is checked: a queue with no name, vhost or type, or the same
// queue listed twice, means the response cannot be trusted, so this returns an
// error.
func (c *Client) Queues(ctx context.Context, pod *corev1.Pod, columns string) ([]Queue, error) {
	var queues []Queue
	if err := c.get(ctx, pod, "/api/queues/detailed?columns="+columns, listTimeout, &queues); err != nil {
		return nil, err
	}
	if queues == nil {
		return nil, fmt.Errorf("RabbitMq queue scan did not return an array")
	}
	seen := make(map[string]bool, len(queues))
	for _, queue := range queues {
		if queue.Name == "" || queue.Vhost == "" || queue.Type == "" {
			return nil, fmt.Errorf("RabbitMq queue scan has a queue without name, vhost, or type")
		}
		key := queue.Vhost + "\x00" + queue.Name
		if seen[key] {
			return nil, fmt.Errorf("RabbitMq queue %s appears more than once", queue.Ref())
		}
		seen[key] = true
	}
	return queues, nil
}

// QuorumQueueMembers returns the brokers a quorum queue is meant to live on,
// and which of those currently hold an online copy.
//
// The two are checked against each other. No brokers at all, the same broker
// listed twice, or an online copy on a broker the queue does not live on all
// mean the queue's replication cannot be counted. In those cases this returns
// an error, rather than a count that looks fine but is not.
func QuorumQueueMembers(queue Queue) (members, online map[string]bool, err error) {
	if len(queue.Members) == 0 || queue.Online == nil {
		return nil, nil, fmt.Errorf("RabbitMq quorum queue %s has unknown membership", queue.Ref())
	}
	members = make(map[string]bool, len(queue.Members))
	for _, member := range queue.Members {
		if member == "" || members[member] {
			return nil, nil, fmt.Errorf("RabbitMq quorum queue %s has ambiguous membership", queue.Ref())
		}
		members[member] = true
	}
	online = make(map[string]bool, len(queue.Online))
	for _, member := range queue.Online {
		if !members[member] || online[member] {
			return nil, nil, fmt.Errorf("RabbitMq quorum queue %s has ambiguous online members", queue.Ref())
		}
		online[member] = true
	}
	return members, online, nil
}

// QuorumOf is how many of n replicas have to agree before the group can do
// anything: more than half.
func QuorumOf(n int) int {
	if n <= 0 {
		return 0
	}
	return n/2 + 1
}

// QueueSummary counts how the cluster's queues are replicated.
//
// UnderReplicated and BelowQuorum mean different things and should not be
// added together. UnderReplicated means a copy is missing, which RabbitMQ
// normally puts back on its own. BelowQuorum means more than half the copies
// are gone, which means the queue cannot be used right now.
type QueueSummary struct {
	Total           int
	Quorum          int
	Classic         int
	Stream          int
	UnderReplicated int
	BelowQuorum     int
	// Degraded lists the under-replicated queues, sorted and cut off at
	// SummarySampleLimit so the list stays short and does not jump around
	// between reads.
	Degraded []string
	// Err is the first queue whose replication could not be read. The counts
	// above cover only the queues that could be read, so a summary with Err
	// is not a complete picture of the cluster.
	Err error
}

// SummarySampleLimit is how many queue names a summary lists. A cluster can
// hold tens of thousands of queues, and the list is there to show an example
// of what is wrong, not to name everything that is.
const SummarySampleLimit = 10

// SummarizeQueues counts how the given queues are replicated.
//
// It talks to nothing and decides nothing. It describes what was read, and
// leaves the caller to report or act on it.
func SummarizeQueues(queues []Queue) QueueSummary {
	summary := QueueSummary{Total: len(queues)}
	degraded := []string{}
	for _, queue := range queues {
		switch queue.Type {
		case QueueTypeQuorum:
			summary.Quorum++
			members, online, err := QuorumQueueMembers(queue)
			if err != nil {
				if summary.Err == nil {
					summary.Err = err
				}
				continue
			}
			if len(online) < len(members) {
				summary.UnderReplicated++
				degraded = append(degraded, queue.Ref())
			}
			if len(online) < QuorumOf(len(members)) {
				summary.BelowQuorum++
			}
		case QueueTypeClassic:
			summary.Classic++
		case QueueTypeStream:
			summary.Stream++
		}
	}
	sort.Strings(degraded)
	if len(degraded) > SummarySampleLimit {
		degraded = degraded[:SummarySampleLimit]
	}
	summary.Degraded = degraded
	return summary
}
