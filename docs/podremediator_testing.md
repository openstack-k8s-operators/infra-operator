# PodRemediator testing

<!-- Damien #4104796972, #4105180193: give testing its own document and point to the fuller environment-specific integration test instructions. -->

This document describes what each test layer can establish about PodRemediator.
It is a testing reference, not a list of fixes, priorities, or release gates.
Application-specific consent policy should also be tested in the repository that
implements that policy; see the [Galera integration](podremediator_galera.md)
and [RabbitMQ integration](podremediator_rabbitmq.md) documents.

## Controller tests

The unit tests in `test/unit/remediation/` call the public `Reconcile` and
`SetupWithManager` methods with fake Kubernetes clients and informers. They cover
PV locality, SNR evidence and watch events, namespace selection, consent
revocation, fencing changes immediately before commitment, selected Pod deletion,
resumption of committed work, and progress of independent PVCs while cleanup is
pending. Failure-injection cases exercise rejected writes, writes persisted
before a lost response, unavailable readback, conflicting commit records, and
PVC replacement between commit writes. They assert resource preservation before
commitment and recovery through later reconciliations. Consent combinations,
malformed persisted records, dependency outages, and polling precedence are also
covered. Fixtures and injected client failures live alongside the tests. Run
them with:

```bash
go test ./test/unit/remediation
```

The envtest suite in `test/functional/podremediator_controller_test.go` covers
the controller's Kubernetes API behavior, such as CR initialization, dependency
reporting, watches, and reconciliation within configured namespaces. The cases in
`test/functional/podremediator_preconditions_test.go` also verify that the real
API server rejects stale PVC finalization patches, Pod/PVC deletion with stale
versions or incorrect UIDs, and invalid polling intervals. These precondition
tests use ordered API calls without a background PodRemediator reconciliation.
Envtest does not simulate a real node failure or the actual NHC/SNR remediation
machinery.

Run the PodRemediator unit and functional tests together with:

```bash
make test-podremediator
```

The broader `make test` target also runs other repository test suites.

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

Real NHC/SNR and workload behavior requires a cluster test. The project lab kit
contains Galera and RabbitMQ scenarios for operator adapters and node-failure
workflows. Its [test README](https://gitlab.cee.redhat.com/pidone/ngtestkit/-/blob/main/podremediator-tests/README.md)
describes the suite; the [POC runbook](https://gitlab.cee.redhat.com/pidone/ngtestkit/-/blob/main/podremediator-tests/docs/PODREMEDIATOR_POC_RUNBOOK.md)
covers lab setup and execution. These internal instructions require access to
the project GitLab. The tests are disruptive and must run only in a lab where
node power operations and workload impact are controlled.

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
