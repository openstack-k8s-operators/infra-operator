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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type queueQuorumParticipant struct {
	Node       string `json:"node"`
	State      string `json:"state"`
	Membership string `json:"membership"`
	Term       int64  `json:"term"`
	Leader     string `json:"leader"`
	Members    int    `json:"members"`
	Error      bool   `json:"error"`
}

type queueQuorumReadResult struct {
	Vhost           string                   `json:"vhost"`
	Name            string                   `json:"name"`
	ExpectedMembers int                      `json:"expected_members"`
	Acknowledged    []string                 `json:"acknowledged"`
	Participants    []queueQuorumParticipant `json:"participants"`
	Error           string                   `json:"error,omitempty"`
}

// ProveQueueQuorum determines, for every quorum queue, whether the queue
// would still have a working majority without the deletion candidate.
//
// Two cheaper checks are insufficient. The management API's online, leader and
// state=running fields are a cached view, and report healthy values for a Raft
// group that has restarted and not yet elected a leader. A plain consistent
// query is also insufficient, because the group may form its majority using
// the candidate broker itself, so the query succeeds while the queue would not
// survive the candidate's removal.
//
// This therefore queries each queue's members directly and counts only brokers
// that satisfy all of:
//
//   - membership in safeNodes, the set of survivors already established as
//     healthy, which excludes the deletion candidate;
//   - a live local Raft process reporting the same leader;
//   - acknowledgement of a heartbeat issued during this call, observed as an
//     advance in the leader's query index rather than read from stored state.
//
// A queue whose satisfying brokers exceed half of its full configured
// membership survives losing the candidate. Otherwise this returns an error
// and the caller refuses the deletion.
func (c *Client) ProveQueueQuorum(ctx context.Context, peer *corev1.Pod,
	queues []Queue, safeNodes map[string]bool) error {
	binaryArg := func(value string) string {
		return `base64:decode(<<"` + base64.StdEncoding.EncodeToString([]byte(value)) + `">>)`
	}
	// Batch to bound the exec argument and avoid starting one CLI per queue.
	for start := 0; start < len(queues); start += 32 {
		end := min(start+32, len(queues))
		inputs := make([]string, 0, end-start)
		batch := queues[start:end]
		for _, q := range batch {
			nodes := []string{}
			for _, node := range q.Members {
				if safeNodes[node] {
					nodes = append(nodes, binaryArg(node))
				}
			}
			if len(nodes) < QuorumOf(len(q.Members)) {
				return fmt.Errorf("RabbitMq quorum queue %s/%s has fewer safe Ra members than a majority", q.Vhost, q.Name)
			}
			sort.Strings(nodes)
			inputs = append(inputs, "{"+binaryArg(q.Vhost)+","+binaryArg(q.Name)+","+strconv.Itoa(len(q.Members))+",["+strings.Join(nodes, ",")+"]}")
		}
		// Every resource name is a base64 binary, never interpolated Erlang
		// source. Query only safe members, prove their local Ra state, then
		// issue a fresh heartbeat and count acknowledgements only among those
		// safe configured voters.
		expression := `Process=fun(V,N,Expected,Nodes) ->
    try
    {ok,Q}=rabbit_amqqueue:lookup(rabbit_misc:r(V,queue,N)),
    {RaName,_}=amqqueue:get_pid(Q),
    Participants=[begin
        Node=binary_to_existing_atom(B,utf8),
        Id={RaName,Node},
        try
            {ok,M,{RaName,Leader}}=erpc:call(Node,ra,members,[Id,3000],3000),
            true=is_pid(erpc:call(Node,erlang,whereis,[RaName],3000)),
            Metrics=erpc:call(Node,ra,key_metrics,[Id],3000),
            #{node=>B,members=>length(M),term=>maps:get(term,Metrics),
              state=>atom_to_binary(maps:get(state,Metrics),utf8),
              membership=>atom_to_binary(maps:get(membership,Metrics),utf8),
              leader=>atom_to_binary(Leader,utf8),error=>false}
        catch _:_ ->
            #{node=>B,error=>true}
        end
    end || B<-Nodes],
    Leaders=lists:usort([binary_to_existing_atom(L,utf8) ||
        #{leader:=L,error:=false} <- Participants]),
    case Leaders of
        [Leader] ->
            Servers=[{RaName,binary_to_existing_atom(B,utf8)} || B<-Nodes],
            {ok,Before,_}=erpc:call(Leader,ra,member_overview,[{RaName,Leader},3000],4000),
            BeforeQueryIndex=maps:get(query_index,Before),
            case ra:consistent_query(Servers,fun(_)->ok end,3000) of
                {ok,ok,{RaName,Leader}} ->
                    LeaderName=atom_to_binary(Leader,utf8),
                    PeerQueryIndex=fun(Overview,Name) ->
                        OverviewQueryIndex=maps:get(query_index,Overview),
                        case Name =:= LeaderName of
                            true -> OverviewQueryIndex;
                            false ->
                                Peer={RaName,binary_to_existing_atom(Name,utf8)},
                                Cluster=maps:get(cluster,Overview),
                                case maps:get(Peer,Cluster,undefined) of
                                    #{query_index:=QI} -> QI;
                                    _ -> 0
                                end
                        end
                    end,
                    VoterNodes=[B || #{node:=B,membership:=<<"voter">>,error:=false} <- Participants],
                    Acknowledged=fun(Overview) ->
                        [B || B<-VoterNodes,
                            PeerQueryIndex(Overview,B)>BeforeQueryIndex]
                    end,
                    Required=Expected div 2 + 1,
                    Await=fun F(0) ->
                        {ok,Overview,_}=erpc:call(Leader,ra,member_overview,
                            [{RaName,Leader},3000],4000),
                        #{vhost=>V,name=>N,expected_members=>Expected,
                          acknowledged=>Acknowledged(Overview),
                          participants=>Participants};
                    F(Tries) ->
                        {ok,Overview,_}=erpc:call(Leader,ra,member_overview,
                            [{RaName,Leader},3000],4000),
                        case length(Acknowledged(Overview)) >= Required of
                            true ->
                                #{vhost=>V,name=>N,expected_members=>Expected,
                                  acknowledged=>Acknowledged(Overview),
                                  participants=>Participants};
                            false ->
                                timer:sleep(50),
                                F(Tries-1)
                        end
                    end,
                    Await(20);
                _ ->
                    #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>[],
                      participants=>[#{node=><<"heartbeat_error">>,error=>true}]}
            end;
        _ ->
            #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>[],
              participants=>[#{node=><<"leader_error">>,error=>true}]}
    end
catch C:R:S ->
    #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>[],participants=>[],
      error=>iolist_to_binary(io_lib:format("~p",[{C,R,S}]))}
    end
end,
Results=[Process(V,N,Expected,Nodes) || {V,N,Expected,Nodes}<-` + "[" + strings.Join(inputs, ",") + "]]" + `,io:format("QUEUE_CONSENSUS=~ts~n",[rabbit_json:encode(Results)]),ok.`
		out, err := c.Ctl(ctx, podKey(peer), "-q", "eval", expression)
		if err != nil {
			return fmt.Errorf("queue survivor consensus query: %w", err)
		}
		var results []queueQuorumReadResult
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "QUEUE_CONSENSUS=") {
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "QUEUE_CONSENSUS=")), &results); err != nil {
					return fmt.Errorf("decoding queue survivor consensus: %w", err)
				}
			}
		}
		resultsByQueue := make(map[string]queueQuorumReadResult, len(results))
		for _, result := range results {
			key := result.Vhost + "\x00" + result.Name
			if _, exists := resultsByQueue[key]; exists {
				return fmt.Errorf("queue survivor consensus returned duplicate queue %s/%s", result.Vhost, result.Name)
			}
			resultsByQueue[key] = result
		}
		if len(resultsByQueue) != len(batch) {
			return fmt.Errorf("queue survivor consensus returned %d of %d queues", len(resultsByQueue), len(batch))
		}
		for _, queue := range batch {
			result, ok := resultsByQueue[queue.Vhost+"\x00"+queue.Name]
			if !ok || result.ExpectedMembers != len(queue.Members) {
				return fmt.Errorf("queue survivor consensus changed for %s/%s", queue.Vhost, queue.Name)
			}
			if result.Error != "" {
				return fmt.Errorf("RabbitMq quorum queue %s/%s proof failed: %s", queue.Vhost, queue.Name, result.Error)
			}
			required := result.ExpectedMembers/2 + 1
			if result.ExpectedMembers <= 0 {
				return fmt.Errorf("RabbitMq quorum queue %s/%s has invalid configured membership", queue.Vhost, queue.Name)
			}
			acknowledged := make(map[string]bool, len(result.Acknowledged))
			for _, node := range result.Acknowledged {
				if node == "" || acknowledged[node] {
					return fmt.Errorf("RabbitMq quorum queue %s/%s returned an invalid acknowledgement", queue.Vhost, queue.Name)
				}
				acknowledged[node] = true
			}
			if len(acknowledged) < required {
				return fmt.Errorf("RabbitMq quorum queue %s/%s lacks a candidate-excluded heartbeat majority: %d of %d", queue.Vhost, queue.Name, len(acknowledged), result.ExpectedMembers)
			}
			groups := map[string][]queueQuorumParticipant{}
			memberSet := make(map[string]bool, len(queue.Members))
			for _, member := range queue.Members {
				memberSet[member] = true
			}
			for _, participant := range result.Participants {
				if participant.Error || !safeNodes[participant.Node] ||
					(participant.State != "leader" && participant.State != "follower") ||
					participant.Membership != "voter" || participant.Members != result.ExpectedMembers {
					continue
				}
				key := participant.Leader + "\x00" + strconv.FormatInt(participant.Term, 10) + "\x00" + strconv.Itoa(participant.Members)
				groups[key] = append(groups[key], participant)
			}
			confirmed := false
			for _, group := range groups {
				if !memberSet[group[0].Leader] {
					continue
				}
				nodes := map[string]bool{}
				leaders := 0
				acknowledgedInGroup := 0
				for _, participant := range group {
					if nodes[participant.Node] {
						return fmt.Errorf("queue survivor consensus returned duplicate node %s", participant.Node)
					}
					nodes[participant.Node] = true
					if acknowledged[participant.Node] {
						acknowledgedInGroup++
					}
					if participant.State == "leader" {
						leaders++
					}
				}
				if leaders == 1 && len(nodes) >= required && acknowledgedInGroup >= required {
					confirmed = true
					break
				}
			}
			if !confirmed {
				return fmt.Errorf("RabbitMq quorum queue %s/%s lacks a safe Ra majority", queue.Vhost, queue.Name)
			}
		}
	}
	return nil
}
