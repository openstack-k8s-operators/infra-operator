# Verifying that PodRemediator works – possible tests

After running the POC playbook, here are the tests you can run to confirm that the controller **reacts** and **deletes local PVCs** when a node becomes Not Ready.

---

## Checking NHC and SNR manually

PodRemediator depends on **Node Health Check (NHC)** and **Self Node Remediation (SNR)**. Use these commands from a host with `oc` and cluster access (e.g. controller-0 as zuul).

### NHC (Node Health Check)

```bash
# List all NodeHealthChecks (namespace is usually openshift-workload-availability)
oc get nodehealthchecks -A

# Details and status of NHCs
oc get nodehealthchecks -A -o wide

# Status conditions (remediation progress, unhealthy nodes)
oc get nodehealthchecks -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n  conditions: "}{.status.conditions}{"\n"}{end}'

# Describe a specific NHC
oc get nodehealthcheck -n openshift-workload-availability nodehealthcheck-sample -o yaml
```

### SNR (Self Node Remediation)

```bash
# List SelfNodeRemediationTemplates (SNR strategy templates)
oc get selfnoderemediationtemplates -A

# List SelfNodeRemediation CRs (one per node being remediated)
oc get selfnoderemediation -A

# Pods of the SNR operator / agents
oc get pods -n openshift-workload-availability -l app.kubernetes.io/name=self-node-remediation
```

### Quick sanity check

```bash
# At least one NHC and one SNR template must exist for PodRemediator to be Ready
oc get nodehealthchecks -A --no-headers
oc get selfnoderemediationtemplates -A --no-headers
```

If either list is empty, PodRemediator will report Ready=False with a message that NHC and SNR are required. Install them (e.g. via the runbook Step 4 / ci-framework `cifmw_snr_nhc` role) and ensure at least one NodeHealthCheck and one SelfNodeRemediationTemplate exist.

### When the E2E PVC poll times out (PVC not deleted)

If PodRemediator is Ready and InputReady but the test PVC is still there after the timeout:

1. **Confirm PodRemediator is watching the right namespace**  
   `spec.namespaces: []` means “watch the CR namespace” (e.g. `openstack-operators`), so the test PVC in that namespace is in scope.

2. **Check infra-operator logs for remediation** (filter other controllers):
   ```bash
   oc logs -n openstack-operators -l app.kubernetes.io/name=infra-operator --tail=1000 -c manager | grep -i -E 'PodRemediator|Unhealthy nodes|Deleting PVC'
   ```
   - `Unhealthy nodes detected, checking for local PVCs to delete` – the controller saw at least one NotReady node (confirms Node watch is triggering reconcile).
   - `Deleting PVC bound to unhealthy node` – the controller found the local PVC and deleted it.
   If neither line appears while the node was NotReady, the controller may not be reconciling on Node updates (e.g. watch not firing or queue delay). Try a larger `--tail` or reproduce the failure and check logs immediately after.

3. **Confirm the test PV is considered “local”**  
   The controller only deletes PVCs bound to PVs with node affinity (e.g. `kubernetes.io/hostname`) and local-like volume (hostPath, or CSI with node affinity). Check the test PV:
   ```bash
   oc get pv test-local-pv-podremediator -o yaml
   ```
   It should have `nodeAffinity` with `kubernetes.io/hostname` and a `hostPath` (or similar) volume source.

4. **Retry with a longer timeout**  
   NHC/SNR and reconciliation can be slow. Re-run the E2E with a longer wait, e.g. `E2E_PVC_TIMEOUT=1200` (20 min) or `-e "e2e_pvc_deleted_timeout_seconds=1200"`.

---

## 1. Automated tests (no real cluster)

Run the controller’s functional tests (envtest, no live cluster):

```bash
# From repo root
make test-podremediator
```

Verify they pass: they cover the logic (NHC/SNR absent → Ready=False, finalizer, etc.). They **do not** simulate a real NotReady node with PVC; they validate the code.

---

## 2. Verify “no deletion when the node is healthy”

**Current situation:** you already have a Pod with a local PVC (`test-pod-local-pvc-podremediator`) on a node (e.g. `worker-0`). All nodes are Ready.

- **Expected:** PodRemediator **must not** delete any PVC.
- **Check:**  
  `oc get pvc -n openstack-operators test-local-pvc-podremediator`  
  The PVC must exist. Also check infra-operator logs: message like “No unhealthy nodes; monitoring”.

---

## 3. E2E test: node actually down → PVC deletion (recommended: virsh destroy)

This is the test that demonstrates “node down → NHC/SNR detect → PVC deleted”. The most realistic way is to **power off the worker VM** with **virsh destroy** on had-18 (where libvirt runs).

### Ansible E2E playbook (virsh on had-18)

A playbook is available that automates everything:

```bash
# From had-18 (as zuul, with -b for virsh; or as root), from repo root
ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
  test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml -b
```

The playbook: finds the test pod’s node, runs **virsh destroy** on the domain (e.g. worker-0), waits for the node to become NotReady and for NHC/SNR to detect it, verifies that the PVC was deleted by PodRemediator, then **virsh start** to restore the VM. See `test-kit/podremediator/README.md` for optional variables (`e2e_virsh_domain`, `e2e_skip_restore`).

### Manual steps (if you don’t use the playbook)

1. **Find the test pod’s node**
   ```bash
   oc get pod -n openstack-operators test-pod-local-pvc-podremediator -o jsonpath='{.spec.nodeName}'
   ```
   Example: `worker-0`.

2. **Verify the PVC exists**
   ```bash
   oc get pvc -n openstack-operators test-local-pvc-podremediator
   ```

3. **Simulate node death** (on had-18, where libvirt runs):
   ```bash
   virsh destroy worker-0
   ```
   (Replace with the libvirt domain name if different, e.g. `overcloud-worker-0`.)

4. **Wait for the node to become NotReady** (the control plane marks it when heartbeats are lost):
   ```bash
   watch -n5 'oc get nodes'
   ```
   NHC/SNR will detect the node as unhealthy.

5. **Verify that PodRemediator deletes the PVC**
   ```bash
   oc get pvc -n openstack-operators test-local-pvc-podremediator
   ```
   Expected: `Error from server (NotFound)`.

6. **Restore the VM**
   ```bash
   virsh start worker-0
   ```
   Wait for the node to become Ready again.

7. **Note:** after the PVC is deleted, the test pod does not come back (it’s a bare Pod). To repeat the test, re-run runbook Step 6.

---

## 4. Test “disabled: true”

Verify that with remediation disabled **no** PVC is deleted even when the node is NotReady.

1. Patch the CR:
   ```bash
   oc patch podremediator podremediator-sample -n openstack-operators --type=merge -p '{"spec":{"disabled":true}}'
   ```
2. Run steps 3–4 from section 3 (node NotReady).
3. **Expected:** the PVC is **not** deleted; PodRemediator stays Ready with message “PVC remediation is disabled”.
4. Restore the node and remove `disabled` or set it to **false** again if you want to re-enable remediation.

---

## 5. Summary

| Test | What it verifies | Where |
|------|------------------|-------|
| `make test-podremediator` | Controller logic (NHC/SNR, conditions, finalizer) | Local (envtest) |
| PVC present with all nodes Ready | No spurious deletion | Cluster (already verifiable) |
| Node NotReady → PVC deleted | PodRemediator E2E behaviour | Cluster (stop kubelet or simulate failure) |
| `disabled: true` + node NotReady | No deletion when disabled | Cluster |

The test that **demonstrates** PodRemediator “works” is the **E2E test (section 3)**: node made NotReady → controller deletes the PVC bound to that node (and logs confirm it).
