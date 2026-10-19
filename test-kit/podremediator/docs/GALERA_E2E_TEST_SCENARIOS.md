# Galera E2E test scenarios (PodRemediator + local PVC)

This document describes **test baselines** for OpenStack Galera (`mariadb.openstack.org/v1beta1`) when exercising the **PodRemediator** flow: simulate worker death with `virsh destroy`, wait for **NHC/SNR** to mark the node unhealthy, confirm **PodRemediator** deletes the **local** PVC bound to that node, then observe **StatefulSet** reschedule and **Galera/wsrep** behaviour.

Related material:

- Design: `../../../docs/STATEFUL_PVC_REMEDIATION_DESIGN.md` (Galera discussion, e.g. §1.5.1).
- Playbooks: `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml` (wrapper + preflight) and `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml` (shared node-death + PVC poll + restore).
- Lab runner: `test-kit/podremediator/scripts/run-e2e.sh` with `E2E_GALERA=1`.

Default names on many OpenStack-on-OpenShift layouts:

| Object | Typical name |
|--------|----------------|
| Namespace | `openstack` |
| Galera CR | `openstack` |
| StatefulSet | `openstack-galera` |
| Test pod (ordinal 0) | `openstack-galera-0` |
| Test PVC (ordinal 0) | `mysql-db-openstack-galera-0` |

Override with extra Ansible vars if your cluster uses `galera-openstack` or a cell (`openstack-cell1-galera`, etc.); see comments in the Galera playbook.

---

## Prerequisites (all scenarios)

1. **Cluster access** (e.g. from `controller-0` as `zuul`): `oc` logged in with rights on `openstack` and `openstack-operators`.
2. **PodRemediator** installed and `spec.namespaces` includes `openstack` (the Galera workload namespace). The Galera wrapper can merge this with `-e e2e_auto_patch_podremediator=true` (lab).
3. **E2E execution host**: playbook **must** run from **had-18** (or wherever **libvirt** can `virsh destroy` / `virsh start` the worker VM that backs the OpenShift node under test). See `test-kit/podremediator/inventory/inventory-from-jumphost.yml`.
4. **Storage class**: Galera uses **local** volumes (e.g. `lvms-local-storage` / TopolVM). Each PVC’s PV carries **node affinity** (e.g. `topology.topolvm.io/node` or `topology.lvms.io/node`). A pod **must** schedule on the node its PV is bound to, unless the PVC is deleted and reprovisioned elsewhere.
5. **Galera healthy**: CR conditions `Ready` / `DeploymentReady`, STS `readyReplicas == spec.replicas`, members `Running` and `wsrep` sane (see monitoring below).

---

## Scenario 1 — Simplest baseline: three members on three different workers

This is the **recommended default mental model** for PodRemediator Galera E2E: **one Galera pod per worker**, each with a **distinct local PV topology**, so killing **one** worker affects **exactly one** Galera member’s data volume and leaves **two** full members on other nodes during remediation.

### 1.1 Objective

- `openstack-galera-0`, `-1`, `-2` run on **three different** Kubernetes worker nodes (e.g. `worker-0`, `worker-1`, `worker-2`).
- Each `mysql-db-openstack-galera-<ordinal>` PVC’s PV `nodeAffinity` matches **that same** worker.
- E2E targets the pod (and PVC) on the worker you will **destroy**; the other two members remain on healthy nodes for the duration of node failure + PVC deletion + reschedule.

### 1.2 Why this is “simplest”

- **Failure isolation**: one dead node ⇒ one lost local volume for the test ordinal; the other two replicas stay on live nodes (subject to Galera quorum and SST rules).
- **Clear PodRemediator story**: the unhealthy node matches the PV topology of the **test** PVC; remediation deletes that PVC; STS recreates the pod and a **new** PVC/PV on another worker (capacity permitting).
- **Easier interpretation of wsrep**: snapshots from surviving pods show a **Primary** cluster with `wsrep_cluster_size` 3 once the third member is back.

### 1.3 Verifying topology (before E2E)

**Pods → nodes:**

```bash
oc get pod -n openstack openstack-galera-0 openstack-galera-1 openstack-galera-2 -o wide
```

**PV node affinity per member PVC:**

```bash
for i in 0 1 2; do
  vol=$(oc get pvc -n openstack "mysql-db-openstack-galera-${i}" -o jsonpath='{.spec.volumeName}')
  echo -n "ordinal ${i} PV ${vol} -> "
  oc get pv "$vol" -o jsonpath='{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}'
  echo
done
```

You want **three distinct node names** in the second loop, aligned with `spec.nodeName` for each pod.

**wsrep (any Running member):**

```bash
oc exec -n openstack openstack-galera-0 -c galera -- mysql -uroot -e \
  "SHOW GLOBAL STATUS WHERE Variable_name IN ('wsrep_cluster_size','wsrep_cluster_status','wsrep_local_state_comment');"
```

### 1.4 Why `oc patch` StatefulSet anti-affinity alone is often insufficient

1. **MariaDB operator reconciliation**  
   The controller that owns `openstack-galera` re-applies pod template fields. A **required** `podAntiAffinity` patch may be replaced by **preferred** anti-affinity (soft spread). Preferred rules do **not** prevent two ordinals from landing on the same worker if the scheduler chooses so.

2. **Local PVC topology**  
   Even with strong anti-affinity, **existing** PVCs are pinned to nodes. Two ordinals whose PVs are both on `worker-0` **cannot** move to other workers without **new** volumes (PVC delete + recreate, or full redeploy).

So establishing Scenario 1 in a lab is usually a **deliberate data/layout** step, not a one-line patch.

### 1.5 Lab-only procedure: (re)establish three workers + three PVs

**Warning:** Deleting Galera member PVCs **destroys that member’s local data**; the member rejoins via Galera SST/rebuild. **Do not** use on production. Coordinate with your team; snapshot/backup if anything matters beyond disposable lab.

**Idea:** briefly scale down the **mariadb-operator** deployment so it stops overwriting the StatefulSet template, apply **required** pod anti-affinity, delete **only** the PVCs (and pods) that must move, let new PVs bind on other workers, then scale the operator back up so gcomm / bootstrap completes.

**Example sequence** (adjust names if your STS label differs; `service: openstack-galera` matches the default pod labels on many installs):

```bash
NS=openstack
OP_NS=openstack-operators
STS=openstack-galera

# 1) Stop reconciliation overwriting the STS
oc scale deploy mariadb-operator-controller-manager -n "$OP_NS" --replicas=0
oc rollout status deploy/mariadb-operator-controller-manager -n "$OP_NS" --timeout=120s

# 2) Required anti-affinity: at most one Galera pod per hostname
oc patch sts "$STS" -n "$NS" --type=strategic -p '{
  "spec":{"template":{"spec":{"affinity":{"podAntiAffinity":{
    "requiredDuringSchedulingIgnoredDuringExecution":[{
      "labelSelector":{"matchLabels":{"service":"openstack-galera"}},
      "topologyKey":"kubernetes.io/hostname"
    }]
  }}}}}}

# 3) Example: ordinals 1 and 2 share a worker — delete their PVCs and pods (lab only)
oc delete pvc -n "$NS" mysql-db-openstack-galera-1 mysql-db-openstack-galera-2 --wait=false
oc delete pod -n "$NS" openstack-galera-1 openstack-galera-2 --wait=false

# 4) Wait until STS is fully ready (Galera SST can take many minutes)
oc wait sts/"$STS" -n "$NS" --for=jsonpath='{.status.readyReplicas}'=3 --timeout=900s

# 5) Restore operator
oc scale deploy mariadb-operator-controller-manager -n "$OP_NS" --replicas=1
```

After step 5, re-check the STS `affinity`: the operator may have reverted **required** back to **preferred**. That is expected; **what matters for Scenario 1** is that **each ordinal’s PV topology** sits on a **different** worker after reprovisioning.

If pods stall on **“Waiting for gcomm URI…”**, the operator was likely scaled to 0 too long; scale it back to 1 and wait.

### 1.6 E2E flow under this scenario (what the playbooks do)

The imported playbook (`test-e2e-podremediator-pvc-remediation.yml`) performs roughly:

1. **Preflight** (Galera wrapper): CR + STS + pod Running checks; optional **wsrep snapshot** (`e2e_galera_wsrep_monitor: true` from the Galera import).
2. **Assert** test pod node == PV node for `mysql-db-openstack-galera-0` (or whichever `e2e_test_pvc_name` you set); otherwise PodRemediator would skip the PVC.
3. **`virsh destroy`** on the VM for that worker → node goes **NotReady**.
4. **Poll** NotReady; show NHC; **poll** until PVC is deleted or `spec.volumeName` changes.
5. **Poll** until the StatefulSet pod is **Running** again (often on another worker); optional `oc wait` for Ready.
6. **`virsh start`** worker; poll node **Ready**.
7. **Optional final wsrep assert**: `wsrep_cluster_size` equals STS replicas, `Primary`, `Synced`, `wsrep_connected` / `wsrep_ready` ON.

**DB monitoring during the run** (when `e2e_galera_wsrep_monitor` is enabled): informational `mysql` snapshots at steps such as: before destroy, node NotReady, NHC visible, start/end of PVC poll, pod rescheduled, node Ready. Disable only the hard final assert with `-e e2e_galera_wsrep_assert_final=false` if you want logs without failing the play on strict wsrep.

**Typical lab invocation from laptop:**

```bash
HAD18=<jump-host> SSHPASS=<password> SYNC=1 E2E_GALERA=1 \
  E2E_OC_CMD=/path/to/oc \
  E2E_OC_API_SERVER=https://api.<cluster>:6443 \
  E2E_PVC_TIMEOUT=1200 \
  ./test-kit/podremediator/scripts/run-e2e.sh
```

### 1.7 What to observe (two survivors + one rescheduled)

During the window when one worker is dead:

- **Kubernetes**: test node `NotReady`; test pod terminating/evicted; PVC for the test ordinal removed or replaced.
- **Galera / wsrep** (from a pod still **Running** on a healthy node): `wsrep_cluster_size` may still report **3** while the cluster protocol handles the failed member, or dip depending on timing; `wsrep_cluster_status` should remain **Primary** if quorum holds. Use the playbook snapshots plus manual `mysql` queries.
- **After reschedule**: new pod for the test ordinal should get a **new** PV on a surviving worker; SST completes; final assert expects full cluster health.

---

## Scenario 2 — Two Galera members on the same worker (colocated PVCs)

**Layout:** e.g. `openstack-galera-1` and `openstack-galera-2` share `worker-0` because both PVs have `nodeAffinity` for that node.

**Behaviour differences:**

- **`virsh destroy` of that worker** removes **two** Galera instances at once (two local volumes unavailable). Quorum and operator recovery paths differ from Scenario 1; PodRemediator may remediate **one** PVC per reconcile pass tied to the test pod you selected.
- **E2E targeting**: the playbook asserts **one** test pod and **one** test PVC. Pick the ordinal whose PVC PV is on the node you destroy; the colocated sibling is a **separate** failure domain.

**Use case:** stress partial cluster behaviour, operator ordering, or scheduling bugs—not the minimal “single member loss” baseline.

---

## Scenario 3 — Cell Galera (`openstack-cell1-galera`)

A second Galera cluster may exist for Nova cell DBs, with its own STS and PVC names.

**Playbook overrides** (examples):

```text
-e e2e_galera_cr_name=openstack-cell1
-e e2e_statefulset_name=openstack-cell1-galera
-e e2e_test_pod_name=openstack-cell1-galera-0
-e e2e_test_pvc_name=mysql-db-openstack-cell1-galera-0
```

Apply the same **topology verification** and lab discipline as Scenario 1 if you need one pod per worker for that STS.

---

## Scenario 4 — Persistence of scheduling policy (operator vs manual patch)

| Approach | Effect |
|----------|--------|
| Only `oc patch` STS `required` anti-affinity | Often **reverted** to **preferred** on next reconcile. |
| `OpenStackControlPlane` / `Galera` CR **if** the API gains explicit `affinity` / advanced topology | Desirable long-term; check `oc explain galera.spec --recursive` on your version. |
| Lab procedure in §1.5 | Establishes **PV placement**; re-run if ordinals collapse to one node again after other tests. |

---

## Victim selection — which Galera member the E2E kills

By default the shared E2E playbook uses **`e2e_test_pod_name`** / **`e2e_test_pvc_name`** from the Galera wrapper (usually **`openstack-galera-0`** and **`mysql-db-openstack-galera-0`**). `virsh destroy` always targets the **worker that runs that pod**; PodRemediator must see the **test PVC’s PV** on the same node (the playbook asserts this).

To **vary the victim** without hand-editing names for every run:

| Variable | Effect |
|----------|--------|
| `e2e_galera_victim_selection=max_wsrep_last_committed` | Among StatefulSet members that are **Running** and accept `mysql`, pick the one with the largest **`SHOW GLOBAL STATUS LIKE 'wsrep_last_committed'`** (tie-break: **lowest ordinal**). Then set `e2e_test_pod_name` / `e2e_test_pvc_name` to that pod and its `mysql-db-<sts>-<ordinal>` PVC before the colocation assert and `virsh destroy`. |
| `e2e_galera_victim_selection=random_member` or `random` | Uniform random choice over the **same eligible set**. |
| `e2e_galera_wsrep_spread_before_fault=true` | **(Lab)** Before victim selection: **`SET GLOBAL wsrep_desync=ON`** on peers, batched **`INSERT` churn**, assert **spread** (seqno gaps, recv queue, or **`Donor/Desynced`** / non-`Synced` state). Optional **`wsrep_reject_queries`** is **off by default** (`-e e2e_galera_wsrep_spread_reject_queries=true` to add it). Cleanup after victim pick. |

Implementation: `test-kit/podremediator/tasks/e2e-galera-select-victim-pod.yml` (included from `test-e2e-podremediator-pvc-remediation.yml` when the STS name contains **`galera`** and the variable is set to a non-`fixed` value).

**Examples (from laptop via `run-e2e.sh`):**

```bash
E2E_GALERA=1 E2E_OC_CMD=/home/zuul/bin/oc \
  E2E_EXTRA_ANSIBLE_ARGS='-e e2e_galera_victim_selection=max_wsrep_last_committed' \
  ./test-kit/podremediator/scripts/run-e2e.sh

E2E_GALERA=1 E2E_OC_CMD=/home/zuul/bin/oc \
  E2E_EXTRA_ANSIBLE_ARGS='-e e2e_galera_victim_selection=random_member' \
  ./test-kit/podremediator/scripts/run-e2e.sh
```

### Optional lab: make `wsrep_last_committed` differ before `max_wsrep_last_committed`

On a **healthy** cluster, every member often reports the **same** (or nearly the same) `wsrep_last_committed`, so `max_wsrep_last_committed` ties on the **lowest ordinal** (usually `openstack-galera-0`) and does not stress “most applied commits”.

Set **`e2e_galera_wsrep_spread_before_fault=true`** to run **`tasks/e2e-galera-wsrep-spread-before-fault.yml`** immediately before victim selection:

1. **`SET GLOBAL wsrep_desync = ON`** on peer ordinals (default **`e2e_galera_wsrep_spread_desync_ordinals=1,2`** for a 3-member STS).
2. **Default lab path is `wsrep_desync` only.** Optionally **`SET GLOBAL wsrep_reject_queries = ALL`** on the same peers with **`-e e2e_galera_wsrep_spread_reject_queries=true`** (extra signal; not the same as desync). Unsupported builds log a warning and continue.
3. **Batched `INSERT` stream** (one `mysql` session via `oc exec -i`, not one `oc exec` per row) on **`e2e_galera_wsrep_spread_churn_ordinal`** (default **`0`**) so desynced peers can show apply lag. Per-row `oc` churn was too slow: replication often caught up before the poll, leaving **gap=0**.
4. Poll until any of: **`max(wsrep_last_committed) − min(...) ≥ min_delta`**, or **`max(wsrep_last_written) − min(...)`** when `wsrep_last_written` exists in `SHOW GLOBAL STATUS` (some builds omit it — the task prints a single skip line and uses other signals), or fallbacks: **`wsrep_local_recv_queue`**, **`wsrep_local_state_comment` ≠ `Synced`**. The shell output ends with **`Spread lab passed (criterion: …)`** naming which signal matched.
5. **`tasks/e2e-galera-wsrep-spread-cleanup.yml`** runs right **after** victim pick (and on spread / PVC-timeout failure paths) to **`SET GLOBAL wsrep_reject_queries = NONE`** and **`SET GLOBAL wsrep_desync = OFF`**.

Optional tuning: `e2e_galera_wsrep_spread_churn_inserts` (default **8000**), `e2e_galera_wsrep_spread_insert_batch_size` (default **400**), `e2e_galera_wsrep_spread_poll_seconds` (default **180**). **Lab-only**; the MariaDB image must accept `wsrep_desync`. If batches fail, lower batch size (MySQL `max_allowed_packet`).

Example:

```bash
E2E_GALERA=1 E2E_OC_CMD=/home/zuul/bin/oc \
  E2E_EXTRA_ANSIBLE_ARGS='-e e2e_galera_victim_selection=max_wsrep_last_committed -e e2e_galera_wsrep_spread_before_fault=true' \
  ./test-kit/podremediator/scripts/run-e2e.sh
```

### Why `wsrep_last_committed` (and how it differs from `grastate.dat`)

- **`wsrep_last_committed`** (queried live with `mysql`) is a practical **per-member “how far commits have been applied”** hint while the cluster is running. Picking the **maximum** before a planned node kill is a reasonable way to say: “destroy the worker that currently hosts the **most ahead** replica” (subject to all members being reachable and certified).
- After **total cluster loss** or unsafe shutdowns, **`/var/lib/mysql/grastate.dat`** (especially **`safe_to_bootstrap`**) and operator/bootstrap procedures matter for **who may start alone** — that is **not** what the E2E task implements; it only chooses a **victim pod** for the **PodRemediator + virsh** flow.

Manual spot-check on any member:

```bash
oc exec -n openstack openstack-galera-0 -c galera -- mysql -uroot -N -e \
  "SHOW GLOBAL STATUS LIKE 'wsrep_last_committed';"
```

Repeat for `-1`, `-2`, compare the second column.

### Colocation reminder

If **two Galera pods** share the worker you kill, **two local PVCs** can be remediated; until both are replaced and both pods rescheduled, the DB can look **degraded or unavailable** even though recovery eventually succeeds. Victim selection does **not** change that topology — it only changes **which ordinal** drives which node is destroyed first.

---

## Appendix A — Quick health checks

```bash
# STS + pods
oc get sts,pod -n openstack | grep openstack-galera

# Galera CR
oc get galera openstack -n openstack -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.message}{"\n"}{end}'

# Operator
oc get deploy -n openstack-operators mariadb-operator-controller-manager
```

---

## Appendix B — Files touched by Galera E2E automation

| File | Role |
|------|------|
| `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml` | PodRemediator namespace patch, Galera preflight, imports main E2E, sets `e2e_galera_wsrep_monitor` / timeouts. |
| `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml` | Node death, PVC poll, reschedule, restore VM, wsrep snapshots + optional final assert; optional Galera victim selection. |
| `test-kit/podremediator/tasks/e2e-galera-select-victim-pod.yml` | Before virsh: set test pod/PVC to `max_wsrep_last_committed` or `random_member`. |
| `test-kit/podremediator/tasks/e2e-galera-wsrep-spread-before-fault.yml` | Optional lab: **`wsrep_desync`** + churn; optional `reject_queries`; asserts spread via seqno, `last_written`, recv_queue, or state comment. |
| `test-kit/podremediator/tasks/e2e-galera-wsrep-spread-cleanup.yml` | Clears `wsrep_reject_queries` and `wsrep_desync` after victim pick or on failure. |
| `test-kit/podremediator/tasks/e2e-galera-wsrep-monitor.yml` | Informational `mysql` / wsrep snapshots. |
| `test-kit/podremediator/tasks/e2e-galera-wsrep-assert-final.yml` | Post-run strict wsrep checks. |

---

## Appendix C — Glossary

| Term | Meaning |
|------|--------|
| **NHC** | Node Health Check (Metal3 / OpenShift remediation stack). |
| **SNR** | Self Node Remediation (triggered from unhealthy node signal). |
| **SST** | State Snapshot Transfer (Galera node join / resync). |
| **wsrep** | Galera replication plugin status variables in MariaDB. |
