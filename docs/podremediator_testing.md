# PodRemediator testing

This document describes what each test layer can establish about PodRemediator.
It is a testing reference, not a list of fixes, priorities, or release gates.
Application-specific consent policy should also be tested in the repository that
implements that policy; see the [Galera integration](podremediator_galera.md)
and [RabbitMQ integration](podremediator_rabbitmq.md) documents.

## Controller tests

Tests in `internal/controller/remediation/` exercise controller helpers and
reconcile behavior, including PV locality, SNR evidence, consent freshness,
revocation around the commit boundary, force-deletion of selected Pods, and
resumption of committed work. These tests are the narrowest place to verify
race-sensitive safety conditions and fail-closed input handling.

The envtest suite in `test/functional/podremediator_controller_test.go` covers
the controller's Kubernetes API behavior, such as CR initialization, dependency
reporting, watches, and namespace-scoped reconciliation. Envtest does not
simulate a real node failure or the actual NHC/SNR remediation machinery.

Run the repository's functional test suite with:

```bash
make test
```

## KUTTL integration test

`test/kuttl/tests/podremediator-deletion-commit/` exercises a complete
controller-level PVC deletion transaction against a Kubernetes API server. It
creates test NHC/SNR resources, binds a local PVC to a test node, changes the
synthetic node and SNR state, grants request-scoped consent, and checks committed
cleanup and PVC-protection/finalizer behavior, including recreation of a claim.

The test uses synthetic node and SNR states. It validates PodRemediator's
interaction with Kubernetes objects; it does not prove that a deployed SNR
implementation physically fenced a machine or that an application can recover.
Run it using the repository's KUTTL test configuration and a cluster suitable
for the test suite; do not treat the synthetic phase patch as a fencing test.

## Real-cluster integration tests

Real NHC/SNR and workload behavior requires a cluster test. The lab suite in
`pidone/ngtestkit` contains Galera and RabbitMQ scenarios that exercise actual
operator adapters and node-failure workflows. These tests are disruptive and
must run only in a lab where node power operations and workload impact are
controlled.

The test responsibilities are split deliberately:

- infra-operator tests verify node/PV eligibility, SNR gating, the shared
  consent protocol, and committed Pod/PVC cleanup;
- Galera tests verify the Galera operator's quorum/data policy and recovery
  behavior;
- RabbitMQ tests verify the RabbitMq operator's quorum policy and recovery
  behavior;
- a real-cluster scenario verifies that the independently tested layers work
  together with the deployed NHC/SNR versions and storage driver.

Passing one layer does not imply that the others have passed. In particular,
synthetic SNR objects cannot validate physical fencing, and successful PVC
deletion does not establish application recovery.
