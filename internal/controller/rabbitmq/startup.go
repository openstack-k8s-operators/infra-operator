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

package rabbitmq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errOldQueueReplicaPresent = errors.New("old broker still has queue replicas")

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
	Acknowledged    int                      `json:"acknowledged"`
	Participants    []queueQuorumParticipant `json:"participants"`
	Error           string                   `json:"error,omitempty"`
}

// verifyQueueQuorumReads determines, for every quorum queue, whether the queue
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
func (r *Reconciler) verifyQueueQuorumReads(ctx context.Context, peer *corev1.Pod,
	queues []rmqQueueStatus, safeNodes map[string]bool) error {
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
			if len(nodes) < quorumOf(int32(len(q.Members))) {
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
                        length([B || B<-VoterNodes,
                            PeerQueryIndex(Overview,B)>BeforeQueryIndex])
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
                        case Acknowledged(Overview) >= Required of
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
                    #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>0,
                      participants=>[#{node=><<"heartbeat_error">>,error=>true}]}
            end;
        _ ->
            #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>0,
              participants=>[#{node=><<"leader_error">>,error=>true}]}
    end
catch C:R:S ->
    #{vhost=>V,name=>N,expected_members=>Expected,acknowledged=>0,participants=>[],
      error=>iolist_to_binary(io_lib:format("~p",[{C,R,S}]))}
    end
end,
Results=[Process(V,N,Expected,Nodes) || {V,N,Expected,Nodes}<-` + "[" + strings.Join(inputs, ",") + "]]" + `,io:format("QUEUE_CONSENSUS=~ts~n",[rabbit_json:encode(Results)]),ok.`
		out, err := r.rabbitmqctl(ctx, client.ObjectKeyFromObject(peer), "-q", "eval", expression)
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
			if result.Acknowledged < int(quorumOf(int32(result.ExpectedMembers))) {
				return fmt.Errorf("RabbitMq quorum queue %s/%s lacks a candidate-excluded heartbeat majority: %d of %d", queue.Vhost, queue.Name, result.Acknowledged, result.ExpectedMembers)
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
				for _, participant := range group {
					if nodes[participant.Node] {
						return fmt.Errorf("queue survivor consensus returned duplicate node %s", participant.Node)
					}
					nodes[participant.Node] = true
					if participant.State == "leader" {
						leaders++
					}
				}
				if leaders <= 1 && len(nodes) >= quorumOf(int32(result.ExpectedMembers)) {
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

// hasStartupGate reports whether this pod was built with the startup-gate init
// container. Pods in clusters that do not take part in remediation are not,
// and start their broker without waiting for anything.
func hasStartupGate(pod *corev1.Pod) bool {
	for _, init := range pod.Spec.InitContainers {
		if init.Name == "startup-gate" {
			return true
		}
	}
	return false
}

// startupBlocked reports whether this pod's broker is still held behind the
// gate and has therefore not yet contacted the cluster.
//
// It returns false on any sign that the broker may have started: an approval
// annotation matching the pod, a gate container that has exited successfully,
// or a rabbitmq container that is running or has previously run. Callers use
// this to decide whether a pod can still be treated as inert, so each of those
// states has to disqualify it.
func startupBlocked(pod *corev1.Pod) bool {
	if !hasStartupGate(pod) || pod.UID == "" ||
		pod.Annotations[rabbitmqv1.AnnotationStartupApproved] == string(pod.UID) {
		return false
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == "startup-gate" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
			return false
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == rabbitmqContainerName &&
			(status.State.Running != nil || status.State.Terminated != nil || status.LastTerminationState.Terminated != nil) {
			return false
		}
	}
	return true
}

// ensurePodStartup decides whether this pod may start its broker, and if so
// writes the approval annotation.
//
// It reads from the API server rather than the cache because the pod-create
// event can arrive before the cache reflects the consent and status writes
// that explain why the pod exists. Approving from that older view would allow
// a replacement to start before its old member was removed.
//
// An ordinary pod is approved once its claim checks out. A pod recorded as a
// replacement is approved only if the durable status reports the old member
// removed, names this exact claim UID as the replacement it cleared, and the
// pod carries the matching rejoin authorization. Any missing or inconsistent
// value returns allowed=false, leaving the pod to wait for a later attempt.
func (r *Reconciler) ensurePodStartup(ctx context.Context, instance *rabbitmqv1.RabbitMq, pod *corev1.Pod) (allowed, changed bool, err error) {
	if !hasStartupGate(pod) {
		return true, false, nil
	}
	live := &corev1.Pod{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(pod), live); err != nil {
		return false, false, err
	}
	if live.UID == "" || live.UID != pod.UID || live.DeletionTimestamp != nil {
		return false, false, fmt.Errorf("pod %s changed before startup approval", pod.Name)
	}
	if live.Annotations[rabbitmqv1.AnnotationStartupApproved] == string(live.UID) {
		*pod = *live
		return true, false, nil
	}
	pvcName := "persistence-" + live.Name
	if _, ok := rabbitMQClaimOrdinal(instance.Name, pvcName); !ok || !podUsesPVC(live, pvcName) {
		return false, false, fmt.Errorf("pod %s does not use its canonical RabbitMQ claim", pod.Name)
	}
	current := &rabbitmqv1.RabbitMq{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
		return false, false, err
	}
	if current.DeletionTimestamp != nil || current.UID != instance.UID {
		return false, false, nil
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKey{Namespace: live.Namespace, Name: pvcName}, claim); err != nil {
		return false, false, err
	}
	if claim.UID == "" || claim.DeletionTimestamp != nil {
		return false, false, nil
	}
	// The cached instance already knew this was a replacement: refuse to start
	// it unless the durable record still agrees on who authorized the deletion.
	if expected, pending := instance.Status.PVCRemediation[pvcName]; pending &&
		expected.Consented() && string(claim.UID) != expected.PVCUID {
		record, exists := current.Status.PVCRemediation[pvcName]
		if !exists || !record.Consented() ||
			record.RequestID != expected.RequestID || record.PVCUID != expected.PVCUID {
			return false, false, nil
		}
	}
	// A replacement may only start once the old member has been removed from
	// the surviving cluster, and only for the exact claim that removal targeted.
	if recovery, exists := current.Status.PVCRemediation[pvcName]; exists &&
		recovery.Consented() && string(claim.UID) != recovery.PVCUID {
		if recovery.RequestID == "" || recovery.PVCUID == "" ||
			!recovery.Phase.AtLeast(rabbitmqv1.PVCRemediationMemberRemoved) ||
			recovery.ReplacementPVCUID != string(claim.UID) ||
			live.Annotations[rabbitmqv1.AnnotationRejoinCluster] != recovery.RequestID {
			return false, false, nil
		}
	}
	base := live.DeepCopy()
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[rabbitmqv1.AnnotationStartupApproved] = string(live.UID)
	patchErr := r.Patch(ctx, live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(patchErr) {
		// The pod moved under us; report a change so the caller comes back with
		// a fresh read instead of failing the reconcile.
		return false, true, nil
	}
	if patchErr != nil {
		return false, false, fmt.Errorf("approving startup of %s: %w", live.Name, patchErr)
	}
	*pod = *live
	return true, true, nil
}

// consistentMembers asks the cluster for its agreed member list instead of
// reading the per-broker cache that `rabbitmqctl cluster_status` reports from.
//
// The two differ when confirming a removal. A node absent from one broker's
// cached view may only be unrefreshed there, which is indistinguishable from a
// node that has been forgotten cluster-wide. Only the agreed list separates
// the two cases; using the cached one can report a member as removed while
// other brokers still count it.
func (r *Reconciler) consistentMembers(ctx context.Context, instance *rabbitmqv1.RabbitMq, peer *corev1.Pod) (map[string]bool, error) {
	out, err := r.rabbitmqctl(ctx, client.ObjectKeyFromObject(peer), "-q", "eval",
		`io:format("MEMBERS=~ts~n", [rabbit_json:encode([atom_to_binary(N, utf8) || N <- rabbit_nodes:list_consistent_members()])]), ok.`)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "MEMBERS=") {
			continue
		}
		var names []string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "MEMBERS=")), &names); err != nil {
			return nil, err
		}
		members := make(map[string]bool, len(names))
		for _, name := range names {
			if name == "" || members[name] {
				return nil, fmt.Errorf("ambiguous authoritative RabbitMQ membership")
			}
			members[name] = true
		}
		if !members[rmqNodeName(instance, peer.Name)] {
			return nil, fmt.Errorf("authoritative membership excludes its queried peer")
		}
		return members, nil
	}
	return nil, fmt.Errorf("missing authoritative RabbitMQ membership response")
}

func (r *Reconciler) verifyOldQueueReplicasRemoved(ctx context.Context, instance *rabbitmqv1.RabbitMq, peer *corev1.Pod, creds *rmqMgmtCreds, oldNode string) error {
	return r.scanQueues(ctx, instance, peer, creds, "name,vhost,type,members,online",
		func(q rmqQueueStatus) error {
			if q.Type != "quorum" {
				return nil // Other queue kinds are checked by rmqQueuesSafeAfterDeletion.
			}
			members, _, err := quorumQueueMembers(q)
			if err != nil {
				return err
			}
			if members[oldNode] {
				return fmt.Errorf("%w: %s/%s", errOldQueueReplicaPresent, q.Vhost, q.Name)
			}
			return nil
		})
}

// removeOldMember runs only while the replacement cannot start RabbitMQ. The
// original PVC deletion already required the generic controller's SNR fencing
// barrier. If the old broker is nevertheless online, keep startup blocked.
func (r *Reconciler) removeOldMember(ctx context.Context, instance *rabbitmqv1.RabbitMq,
	target, anchor *corev1.Pod, pods []corev1.Pod, pvcName string, membership clusterMembership) error {
	live := &corev1.Pod{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(target), live); err != nil {
		return err
	}
	if live.UID != target.UID || live.DeletionTimestamp != nil || !startupBlocked(live) {
		return fmt.Errorf("replacement %s is not held behind its startup gate", target.Name)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.safetyReader().Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: pvcName}, claim); err != nil {
		return err
	}
	recovery := instance.Status.PVCRemediation[pvcName]
	current := &rabbitmqv1.RabbitMq{}
	if err := r.safetyReader().Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
		return err
	}
	if current.UID != instance.UID || current.DeletionTimestamp != nil {
		return fmt.Errorf("RabbitMQ resource changed before member removal")
	}
	record, exists := current.Status.PVCRemediation[pvcName]
	if !exists || !record.Consented() ||
		record.RequestID != recovery.RequestID || record.PVCUID != recovery.PVCUID {
		return fmt.Errorf("PVC recovery authorization changed before member removal")
	}
	if claim.DeletionTimestamp != nil || claim.UID == "" || recovery.PVCUID == "" ||
		string(claim.UID) == recovery.PVCUID || recovery.RequestID == "" {
		return fmt.Errorf("replacement claim changed before old-member removal")
	}
	oldNode := rmqNodeName(instance, target.Name)
	if membership.Running[oldNode] {
		return fmt.Errorf("old RabbitMQ member is still running; keeping replacement blocked")
	}
	safe := map[string]bool{}
	stuck := map[string]bool{}
	for _, entry := range instance.Status.PVCRemediation {
		if entry.StuckNode != "" {
			stuck[entry.StuckNode] = true
		}
	}
	for i := range pods {
		p := &pods[i]
		n := rmqNodeName(instance, p.Name)
		if p.Name != target.Name && podReady(p) && !stuck[p.Spec.NodeName] && membership.Running[n] {
			safe[n] = true
		}
	}
	quorum := quorumOf(*instance.Spec.Replicas)
	if len(safe) < quorum {
		return fmt.Errorf("not enough healthy survivors for member removal")
	}
	creds, err := r.rmqLoadMgmtCreds(ctx, instance)
	if err != nil {
		return err
	}
	if err := r.rmqQueuesSafeAfterDeletion(ctx, instance, anchor, creds, oldNode, safe); err != nil {
		return err
	}
	members, err := r.consistentMembers(ctx, instance, anchor)
	if err != nil {
		return err
	}
	queueErr := r.verifyOldQueueReplicasRemoved(ctx, instance, anchor, creds, oldNode)
	if queueErr != nil && !errors.Is(queueErr, errOldQueueReplicaPresent) {
		return queueErr
	}
	if members[oldNode] || errors.Is(queueErr, errOldQueueReplicaPresent) {
		if _, err := r.rabbitmqctl(ctx, client.ObjectKeyFromObject(anchor), "forget_cluster_node", oldNode); err != nil {
			return fmt.Errorf("removing old member: %w", err)
		}
	}
	members, err = r.consistentMembers(ctx, instance, anchor)
	if err != nil {
		return err
	}
	if members[oldNode] || len(members) < quorum {
		return fmt.Errorf("old member removal is not confirmed with survivor quorum")
	}
	after, err := r.clusterMembership(ctx, instance, anchor)
	if err != nil {
		return err
	}
	if after.Running[oldNode] || len(after.Running) < quorum {
		return fmt.Errorf("survivor membership changed after removal")
	}
	if err := r.rmqQueuesSafeAfterDeletion(ctx, instance, anchor, creds, oldNode, safe); err != nil {
		return err
	}
	if err := r.verifyOldQueueReplicasRemoved(ctx, instance, anchor, creds, oldNode); err != nil {
		return err
	}
	recovery.ReplacementPVCUID = string(claim.UID)
	recovery.Phase = rabbitmqv1.PVCRemediationMemberRemoved
	instance.Status.PVCRemediation[pvcName] = recovery
	// Return before approving startup. The outer reconcile must first persist
	// this progress, and ensurePodStartup subsequently reads it from the API.
	return nil
}
