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
	"time"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq/admin"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterHealthInterval is the shortest gap between two reads of the same
// cluster. Reconciles are triggered by watches and often arrive several at a
// time, and each read makes the broker list all of its queues, so reading
// again for every one of them would repeat that work for the same answer.
const clusterHealthInterval = time.Minute

// clusterHealthReadTimeout bounds the broker and queue reads together. The
// per-request limits allow 15 seconds for the broker list and 60 for the
// queue list, so without a shared bound an unresponsive broker could hold the
// reconcile worker for their sum. This is comfortably above what the largest
// cluster the client will read needs: maxResponseBytes allows about 36,000
// queues, which the client measures at around half a millisecond each.
const clusterHealthReadTimeout = 30 * time.Second

// reportClusterHealth asks RabbitMQ how it is doing and writes the answer to
// status.clusterHealth.
//
// It only reports. It never fails the reconcile and never changes whether the
// RabbitMq is Ready, because Ready is only true when every sub-condition is
// true. Adding one here would mean a broker that is briefly unreachable marks
// the RabbitMq as not ready, and with it everything waiting on the RabbitMq -
// which is far too much to conclude from one failed HTTP request.
//
// When a read fails, the error is written onto the previous report instead of
// replacing it. That way the last known-good numbers are still there next to
// the reason the latest read did not work. ObservedAt stays at the time of the
// last successful read, so old counts are never given a fresh timestamp;
// LastAttemptAt records the failed attempt and keeps the interval below
// working.
func (r *Reconciler) reportClusterHealth(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq) {
	Log := r.GetLogger(ctx)
	previous := instance.Status.ClusterHealth
	if previous != nil && !previous.LastAttemptAt.IsZero() &&
		time.Since(previous.LastAttemptAt.Time) < clusterHealthInterval {
		return
	}

	report := func(health *rabbitmqv1beta1.RabbitMqClusterHealth) {
		health.LastAttemptAt = metav1.Now()
		instance.Status.ClusterHealth = health
	}
	failed := func(err error) {
		Log.V(1).Info("Could not read RabbitMQ cluster health", "error", err.Error())
		health := previous.DeepCopy()
		if health == nil {
			health = &rabbitmqv1beta1.RabbitMqClusterHealth{}
		}
		health.Error = err.Error()
		report(health)
	}

	pod, err := r.readyRabbitMQPod(ctx, instance)
	if err != nil {
		failed(err)
		return
	}
	if pod == nil {
		// There is no broker up to ask. The StatefulSet status already says so.
		return
	}
	cluster, err := admin.New(ctx, r.Client, instance)
	if err != nil {
		failed(err)
		return
	}
	if r.ManagementHTTPClient != nil {
		cluster = cluster.WithHTTPClient(r.ManagementHTTPClient)
	}

	readCtx, cancel := context.WithTimeout(ctx, clusterHealthReadTimeout)
	defer cancel()

	nodes, err := cluster.Nodes(readCtx, pod)
	if err != nil {
		failed(err)
		return
	}
	queues, err := cluster.Queues(readCtx, pod, admin.ColumnsReplication)
	if err != nil {
		failed(err)
		return
	}
	summary := admin.SummarizeQueues(queues)
	if summary.Err != nil {
		// The listing was read, but not every quorum queue in it could be
		// accounted for. The counts would understate what is wrong, so treat
		// this like any other failed read and keep the last complete report.
		failed(summary.Err)
		return
	}

	health := &rabbitmqv1beta1.RabbitMqClusterHealth{
		ObservedAt:                  metav1.Now(),
		Members:                     len(nodes),
		RunningMembers:              len(admin.RunningNodes(nodes)),
		QuorumQueues:                summary.Quorum,
		UnderReplicatedQuorumQueues: summary.UnderReplicated,
		QuorumQueuesBelowQuorum:     summary.BelowQuorum,
		DegradedQueues:              summary.Degraded,
	}
	report(health)
}

// readyRabbitMQPod returns one Ready pod of this cluster to send the
// management API requests to, or nil if none is Ready.
//
// Any Ready broker will do, because the management API answers about the whole
// cluster and not just the broker you happen to ask.
func (r *Reconciler) readyRabbitMQPod(ctx context.Context, instance *rabbitmqv1beta1.RabbitMq) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{labels.K8sAppName: instance.Name},
	); err != nil {
		return nil, err
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return pod, nil
			}
		}
	}
	return nil, nil
}
