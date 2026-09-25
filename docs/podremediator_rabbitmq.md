# PodRemediator with RabbitMQ

This document separates RabbitMQ-specific authorization policy from the
generic PodRemediator controller. The generic contract is described in the
[PodRemediator architecture](podremediator_architecture.md).

The RabbitMQ integration discussed here targets infra-operator's namespaced
`RabbitMq` resource (`rabbitmq.openstack.org`). It is distinct from the upstream
RabbitMQ Cluster Operator's `RabbitmqCluster` API.

## Integration state

As of 2026-09-25, the RabbitMq-controller adapter is being developed separately
from the generic PodRemediator foundation in [stacked PR #684](https://github.com/openstack-k8s-operators/infra-operator/pull/684),
which is marked WIP. The details below describe that proposed integration, not a
claim that it is already merged, released, or supported. Recheck the code and
its consent-protocol compatibility before relying on it.

The PR description says the adapter grants `safe-to-delete=true`; that alone
does not establish compatibility with the current request-ID contract. Confirm
that the implementation also writes `consent-id` equal to the current
`request-id` atomically before treating the adapter as functional.

## Responsibilities and proposed policy

PodRemediator publishes the current fenced-node PVC request and enforces the
shared deletion contract. The RabbitMq controller is responsible for deciding
whether the RabbitMQ cluster can tolerate losing one member and for granting
consent to at most one PVC at a time. PodRemediator does not inspect RabbitMQ
membership or queue state.

The current PR description proposes two gates before granting consent:

- **Kubernetes gate:** `RabbitMq.status.readyCount` must be at least
  `floor(spec.replicas / 2) + 1`.
- **RabbitMQ live-view gate:** query the management HTTP API at `/api/nodes` and
  require at least that many nodes with `running=true`. The proposed check uses
  a ready probe pod not on the stuck node. Secret, pod, HTTP, and response
  errors defer consent rather than falling back to the Kubernetes count.

The proposal replaces an earlier `rabbitmqctl` exec probe with the management
API and therefore does not require `pods/exec` for that check. It also adds
`RabbitMq.status.pvcRemediation` to expose the PVCs in the handshake and whether
consent was granted.

These are cluster-level availability checks. A running-node count does not by
itself establish that each queue has the required replica set, that all messages
are recoverable, or that the cluster can re-form after PVC deletion. RabbitMQ
owners must review whether this policy is sufficient for the queues and
workloads they operate; the generic controller cannot make that application
decision.

## Consent protocol requirement

The current PodRemediator contract accepts consent only when the PVC has a
non-empty current `remediation.openstack.org/request-id`,
`remediation.openstack.org/safe-to-delete=true`, and
`remediation.openstack.org/consent-id` exactly equal to that request ID. The
RabbitMq controller must write both consent annotations together with
optimistic concurrency. A write of `safe-to-delete` alone is ignored. The
request ID is only a freshness/correlation token; RabbitMQ safety must come from
RabbitMQ-specific policy, not from the token.

Before enabling the adapter, verify that the implementation follows this
contract and that consent is cleared or refreshed when the request changes.

## TLS and cleanup considerations

The WIP PR description identifies an open TLS question: when TLS is enabled but
no CA secret is configured, the proposed internal management-API probe uses
`InsecureSkipVerify`. That certificate-validation behavior must be explicitly
reviewed and resolved before treating the integration as production-ready.

As with any workload, a replacement Pod can keep a terminating PVC protected.
PodRemediator does not delete that replacement or change RabbitMq replica
counts. The RabbitMq controller must coordinate any pause or replica change
with its own cluster-safety policy. See the [generic limitations](podremediator_architecture.md#current-limitations).

For shared installation and operations, see the [operator guide](podremediator_guide.md).
