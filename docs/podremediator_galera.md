# PodRemediator with Galera clusters

This document describes the Galera-specific adapter to the generic
PodRemediator contract. The generic fencing, consent, commit, and cleanup
behavior is documented in the [PodRemediator architecture](podremediator_architecture.md).
This integration is implemented by the Galera controller in `mariadb-operator`,
not by the generic PodRemediator controller.

## Responsibilities

PodRemediator reports a local PVC that is pinned to an unhealthy, fenced node.
The Galera operator owns the decision to authorize deletion: it has the
application-specific view of the cluster and must decide whether the remaining
members can safely tolerate losing that member. PodRemediator does not inspect
Galera quorum or select a Galera bootstrap node.

Conversely, Galera's authorization does not make PodRemediator responsible for
reforming the cluster or guaranteeing that the replacement member rejoins. PVC
removal and database recovery are separate operations.

## Consent protocol compatibility

The current PodRemediator contract requires both
`remediation.openstack.org/safe-to-delete=true` and
`remediation.openstack.org/consent-id` equal to the PVC's current
`remediation.openstack.org/request-id`. The application operator must write both
fields together using optimistic concurrency. The request ID is an opaque
freshness token; it is not evidence that Galera considers deletion safe.

As of 2026-09-25, the `CheckForStuckPVCRequiringRemediation` implementation
reviewed alongside this document sets `safe-to-delete`, but does not write `consent-id`.
That is not sufficient for the current PodRemediator contract: the request will
remain unconsented until the Galera adapter echoes the current request ID. Do not
treat that implementation as end-to-end compatible until this mismatch is
resolved and verified. The expected annotations and semantics are defined in
[`apis/remediation/v1beta1/annotations.go`](../apis/remediation/v1beta1/annotations.go).

## Galera policy currently implemented

The Galera controller implementation evaluates PVCs carrying
`pvc-stuck-on-node`, updates `Galera.status.pvcRemediation`, and considers at most
one new consent candidate per reconcile. Its current policy has these elements:

- **Kubernetes replica gate:** the StatefulSet's `AvailableReplicas` must be at
  least `floor(spec.replicas / 2) + 1`. If it is below that threshold, consent
  is deferred.
- **wsrep cluster-view check:** when the query succeeds, the observed
  `wsrep_cluster_size` must meet the same quorum threshold. If the root secret
  is unset or cannot be read, no ready probe pod is available, or execution/
  output parsing fails,
  the implementation falls back to the Kubernetes replica gate. This is
  fail-open for the wsrep check; it is not a second mandatory gate. Operators
  should account for that limitation when deciding whether the policy is
  sufficient for their data-safety requirements.
- **Candidate order:** the PVC belonging to the pod with the highest reported
  seqno is held until last; other candidates are ordered by StatefulSet ordinal.
  Consent is granted to one candidate per reconcile so the StatefulSet status
  can be observed again before another consent is considered.
- **Status:** `Galera.status.pvcRemediation`, keyed by PVC name, reports the
  stuck node and whether consent has been granted. It is cleared when there are
  no PVCs carrying the PodRemediator request annotation.
- **Discovery:** the Galera controller checks whether the PodRemediator kind is
  available through its REST mapper. If it is not registered, the integration
  does not attempt PVC remediation.

These checks reduce risk but do not prove that every Galera data-recovery or
bootstrap condition is satisfied. The application operator remains accountable
for the policy and its failure modes.

## Operational boundary

The application operator should evaluate only a current request and should
withdraw consent if its safety decision changes before PodRemediator commits the
PVC deletion. Before commitment, revocation cancels the prepared cleanup. After
commitment, the operation is durable and later annotation changes cannot undo
it.

A live StatefulSet can recreate a pod that uses the terminating PVC. In that
case PVC protection may keep deletion pending. PodRemediator does not delete the
replacement pod or scale the StatefulSet to zero; Galera's controller must
coordinate any pause or replica change with its quorum and recovery policy.
See [current limitations](podremediator_architecture.md#current-limitations)
for the generic behavior.

The [operator guide](podremediator_guide.md) covers PodRemediator installation
and operations. Real-cluster Galera scenarios belong in the integration E2E
suite, not in the generic architecture contract.
