# PodRemediator – Test Plan

This document defines the test plan to verify the Stateful PVC Remediation (PodRemediator) implementation. It covers both **automated** (envtest/functional) and **manual / E2E** scenarios.

**Reference**: Implementation and design are described in [STATEFUL_PVC_REMEDIATION_DESIGN.md](../../../docs/STATEFUL_PVC_REMEDIATION_DESIGN.md).

---

## 1. Automated tests (envtest / functional)

Run with:

```bash
make test
# or, from repo root:
KUBEBUILDER_ASSETS="$(make -f Makefile envtest 2>/dev/null | grep KUBEBUILDER_ASSETS | cut -d= -f2)" go test ./test/functional/... -v -run "PodRemediator"
```

### 1.1 Controller behaviour without NHC/SNR

**Goal**: Without Node Health Check (NHC) and Self Node Remediation (SNR) CRDs or resources, the controller must set Ready=False and not delete any PVC.

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| A1.1 | PodRemediator created, no NHC/SNR | 1. Create a PodRemediator CR in test namespace. 2. Wait for reconcile. | Status.Ready = False; condition reason `NHC/SNRNotFound`; message states NHC and SNR are required. InputReady = False. |
| A1.2 | PodRemediator with disabled=true | 1. Create PodRemediator with `spec.disabled: true`. 2. (Optional) Simulate NHC/SNR present. | If NHC/SNR missing: Ready=False. If NHC/SNR present: Ready=True; no PVC deletion is attempted. |

### 1.2 Finalizer and deletion

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| A2.1 | PodRemediator has finalizer | 1. Create PodRemediator. 2. Get the CR. | CR has the operator finalizer. |
| A2.2 | PodRemediator deletion removes finalizer | 1. Create PodRemediator. 2. Delete the CR. 3. Wait for reconcile. | CR is removed; no finalizer left (no owned resources to clean up). |

### 1.3 Status conditions

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| A3.1 | Conditions initialized | 1. Create PodRemediator. 2. Fetch status. | Ready and InputReady conditions exist (Ready typically False without NHC/SNR). |
| A3.2 | Ready mirrors sub-conditions | 1. Create PodRemediator (no NHC/SNR). | Ready is False when InputReady is False. |

### 1.4 Watches and reconcile triggers

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| A4.1 | Reconcile on Pod change | 1. Create PodRemediator. 2. Create a Pod in same namespace. | Controller reconciles (e.g. status updated); without NHC/SNR, Ready stays False. |
| A4.2 | Reconcile on PVC change | 1. Create PodRemediator. 2. Create a PVC in same namespace. | Controller reconciles. |
| A4.3 | Reconcile on Node change | 1. Create PodRemediator. 2. (Envtest has nodes by default; no change needed or simulate node update if possible.) | Controller can reconcile on node events. |

*Note*: In envtest, NHC and SNR CRDs are not installed by default. To test the “NHC/SNR present” path and PVC deletion, either install NHC/SNR CRDs and create dummy NodeHealthCheck and SelfNodeRemediationTemplate resources, or add a dedicated test that mocks the dynamic client (out of scope for this test plan document).

---

## 2. Manual / E2E test scenarios

These require a real cluster (e.g. OpenShift) with NHC and SNR installed and configured.

### 2.1 Prerequisites

- OpenShift (or Kubernetes) cluster with:
  - **Node Health Check operator** installed; at least one **NodeHealthCheck** targeting worker nodes.
  - **Self Node Remediation operator** installed; at least one **SelfNodeRemediationTemplate** (e.g. ResourceDeletion or similar).
- **infra-operator** with PodRemediator controller and CRD deployed.
- A way to make a worker node unhealthy (e.g. power off, disconnect network, or taint/stop kubelet for testing).

### 2.2 Basic flow: NHC/SNR missing

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| M1.1 | PodRemediator without NHC/SNR | 1. In a cluster **without** NHC/SNR (or with NHC/SNR disabled), create a PodRemediator. 2. `oc get podremediator -o yaml`. | Ready=False; message that NHC and SNR are required. No PVC deletion. |
| M1.2 | PodRemediator after installing NHC/SNR | 1. Create PodRemediator (Ready=False). 2. Install and configure NHC and SNR (at least one NHC and one SNRT). 3. Re-check PodRemediator. | After NHC/SNR exist, next reconcile sets Ready=True (and InputReady=True). |

### 2.3 Basic flow: NHC/SNR present, no local PVCs

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| M2.1 | PodRemediator with NHC/SNR, no workloads | 1. Ensure NHC and SNR are present. 2. Create PodRemediator in a namespace that has no stateful workloads. 3. Check status. | Ready=True; no PVC deletion (no local PVCs). |
| M2.2 | Disable remediation | 1. Create PodRemediator with `spec.disabled: true`. 2. Create a StatefulSet with local PVC; make its node unhealthy. | PodRemediator stays Ready=True; **no** PVC is deleted; workload stays stuck until manual intervention. |

### 2.4 End-to-end: local PVC on unhealthy node

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| M3.1 | Single workload, one node down | 1. Deploy a StatefulSet (or single Pod) with a **local** PVC (e.g. local volume or storage class that provisions node-bound PVs). 2. Note the node where the pod runs. 3. Create PodRemediator watching that namespace. 4. Make the node unhealthy (e.g. power off, or simulate NotReady). 5. Wait for NHC/SNR to detect and start remediation. 6. Observe PodRemediator and PVCs. | PodRemediator reconciles; when the node is NotReady, the controller deletes the PVC bound to that node. The StatefulSet (or controller) can recreate the PVC and reschedule the pod on a healthy node. |
| M3.2 | Multiple namespaces | 1. Create PodRemediator with `spec.namespaces: [ns-a, ns-b]`. 2. Deploy stateful workloads with local PVCs in ns-a and ns-b. 3. Make one node (hosting a pod in ns-a) unhealthy. | Only PVCs in the watched namespaces are considered; the PVC in ns-a on the unhealthy node is deleted; ns-b unaffected (unless its node is also unhealthy). |
| M3.3 | No deletion when node is healthy | 1. Create PodRemediator and a workload with local PVC. 2. Do **not** make the node unhealthy. | No PVC is deleted; Ready=True; workload unchanged. |
| M3.4 | RabbitMQ StatefulSet (real workload) | 1. Ensure `spec.namespaces` on PodRemediator includes `openstack` (and operators namespace if needed). 2. Run `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-rabbitmq.yml` from had-18 (after optional test STS E2E), or `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-sequential.yml` for test STS then RabbitMQ. 3. Playbook targets `rabbitmq-server-0` / `persistence-rabbitmq-server-0` by default; destroys the worker VM hosting that pod; expects PVC delete/remount like M3.1. | Same as M3.1 for the RabbitMQ PVC; pod reschedules and may take longer until Ready (Mnesia); optional `oc wait` Ready with `e2e_pod_ready_wait_seconds`. **Warning:** disruptive to the RabbitMQ cluster — lab only. |
| M3.5 | Galera / MariaDB StatefulSet (real workload, exploratory) | **Agreed approach:** mirror **M3.4** in intent—same cluster setup (NHC, SNR, PodRemediator watching `openstack`), pick a **Galera member pod** on a worker with **local-like** PVC, then **simulate worker failure** (`virsh destroy` via ansible, same as main E2E). **Playbook:** `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml` (defaults `galera-openstack-0` / `mysql-db-galera-openstack-0`; override `-e e2e_*` to match your STS/PVC names). From laptop: `E2E_GALERA=1 ./test-kit/podremediator/scripts/run-e2e.sh`. **Goal is observation**: record PVC deletion timing, operator and STS behaviour, WSREP / cluster recovery, errors, manual intervention. | **Expected varies** until characterised: generic PodRemediator may delete the member’s PVC when node is NotReady and PV is local (see controller). Document **actual** outcome and **corner cases** for follow-up (see [STATEFUL_PVC_REMEDIATION_DESIGN.md](../../../docs/STATEFUL_PVC_REMEDIATION_DESIGN.md) §1.5.1). **Warning:** highly disruptive — lab only; coordinate with Galera operator owners. |

### 2.5 Edge cases and regression

| ID   | Scenario | Steps | Expected result |
|------|----------|--------|------------------|
| M4.1 | Non-local PVC | 1. Deploy workload with **non-local** PVC (e.g. RWX or RWO on shared storage). 2. Make the node unhealthy. | PodRemediator does **not** delete that PVC (only local PVs are considered). |
| M4.2 | PVC already deleted | 1. Trigger remediation path (unhealthy node with local PVC). 2. Manually delete the PVC before the controller does. 3. Let controller reconcile. | No error; controller may attempt delete and get NotFound, which is ignored. |
| M4.3 | PodRemediator deleted while monitoring | 1. Create PodRemediator and workloads. 2. Delete the PodRemediator CR. | CR is removed cleanly (finalizer removed); no leftover resources. |

---

## 3. Test matrix summary

| Category | Automated (envtest) | Manual / E2E |
|----------|---------------------|--------------|
| NHC/SNR missing | Yes (Ready=False, message) | Yes (same + install NHC/SNR) |
| Finalizer / delete | Yes | Optional |
| disabled=true | Logic only (no NHC/SNR in envtest) | Yes |
| Local PVC on unhealthy node | No (no NHC/SNR or real nodes in envtest) | Yes |
| RabbitMQ E2E (ansible on had-18) | No | Yes (`test-e2e-podremediator-pvc-remediation-rabbitmq.yml`) |
| Galera E2E (same scenario shape as M3.4; exploratory) | No | Yes (`test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml`; `E2E_GALERA=1` with `test-kit/podremediator/scripts/run-e2e.sh`) |
| Multiple namespaces | Optional (reconcile triggered) | Yes |
| Non-local PVC | No | Yes |
| Status conditions | Yes | Optional |

---

## 4. Running the tests

### 4.1 Automated (functional)

Functional tests use **Ginkgo** and **envtest**. From repo root:

```bash
# Run all tests (including PodRemediator). This runs make manifests generate gowork fmt vet envtest ginkgo first.
make test
```

To run **only** PodRemediator controller tests:

```bash
make test-podremediator
```

This target uses an **absolute path** for `KUBEBUILDER_ASSETS` (via `$(LOCALBIN)` in the Makefile). If you run ginkgo manually instead, you must set `KUBEBUILDER_ASSETS` to an **absolute** path; otherwise envtest may fail with:

```text
fork/exec bin/k8s/1.31.0-linux-amd64/etcd: no such file or directory
```

That happens because `setup-envtest use -p path --bin-dir ./bin` returns a *relative* path (`bin/k8s/...`). When Ginkgo runs the test suite, the working directory can be the package directory (`test/functional/`), so the relative path no longer points to the real `bin/` and etcd is not found. Fix by using an absolute bin-dir so the returned path is absolute:

```bash
# From repo root; $(pwd)/bin makes KUBEBUILDER_ASSETS absolute
KUBEBUILDER_ASSETS="$(./bin/setup-envtest use 1.31 -p path --bin-dir $(pwd)/bin)" \
  ./bin/ginkgo --focus "PodRemediator" -v ./test/functional/
```

If envtest binaries are missing, run `make envtest` once to download them.

### 4.2 Manual checklist

Use the tables in §2 as a checklist. For each scenario:

1. Perform the steps in order.
2. Verify the expected result (status, PVC presence/absence, pod reschedule).
3. Record result (pass/fail) and environment (OCP version, NHC/SNR versions).

---

## 5. Future improvements

- **Envtest with NHC/SNR CRDs**: Install NHC and SNR CRDs in the test env, create dummy NodeHealthCheck and SelfNodeRemediationTemplate, and add automated tests for Ready=True and for PVC deletion when a node is simulated as NotReady.
- **Unit tests**: Extract `isLocalPV`, `getLocalPVNodeName`, `isNodeUnhealthy` into a small package and add unit tests with fake PV/Node objects.
- **E2E in CI**: Add a periodic or optional E2E job that runs a subset of manual scenarios (e.g. M1.1, M2.1, M3.1) on a real cluster.
