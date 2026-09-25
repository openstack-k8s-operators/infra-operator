# Galera topology spread, PodRemediator E2E, and had-18 lab notes

This document records **goals**, **rationale**, and **implementation** for work performed on OpenStack Galera scheduling, PodRemediator PVC remediation E2E testing, and validation on lab cluster **had-18** (OpenShift-based OSP lab). It is written for operators and developers who need to reproduce or extend the same patterns.

---

## 1. Purpose and scope

### 1.1 Goals

1. **Improve member spread** when Galera pods reschedule after node failure: avoid colocation on workers that already run another Galera member when empty workers exist (within limits of **local PVC** topology).
2. **Run and observe** PodRemediator **Galera E2E** (node death simulation, PVC remediation, cluster recovery) with **detailed telemetry** (MariaDB `wsrep` snapshots and Kubernetes **Events**).
3. **Establish a durable (product-native) way** to apply **Topology Spread Constraints (TSC)** without manual StatefulSet patches that the MariaDB operator immediately overwrites.
4. **Validate on had-18** the **Topology CR + `topologyRef`** path end-to-end, including interaction with **OpenStackControlPlane** and the **`Galera`** CR.

### 1.2 Why this matters

- Default scheduling for Galera in **mariadb-operator** uses **preferred** (soft) `podAntiAffinity` via `lib-common` `affinity.DistributePods`. That **does not forbid** two members on the same worker if the scheduler has stronger constraints (e.g. **local volume node affinity**).
- **Local PVCs** (e.g. TopolVM / LVMS) bind each ordinal to a node; TSC and anti-affinity **cannot move** a member off a node without **recreating the PVC/PV** on another node.
- Patching the **StatefulSet** directly is **volatile**: `mariadb-operator-controller-manager` reconciles the template and **removes** foreign fields such as `topologySpreadConstraints`.
- Scaling the operator to **zero** to preserve a manual STS patch **breaks** new Galera pods: they wait indefinitely for **gcomm URI** configuration that the operator normally injects. Recovery requires bringing the operator back and letting the cluster rejoin before re-applying any lab-only operator-off procedure.

---

## 2. PodRemediator Galera E2E and Kubernetes Events reporting

### 2.1 Purpose

Provide a **time-ordered view** of cluster activity (beyond Ansible `-vvv` and `wsrep` probes) during the long phases of node **NotReady**, NHC/SNR, and PVC deletion.

### 2.2 Implementation (repository)

| Item | Location |
|------|----------|
| Append `oc get events` (workload + operator namespaces) after each Galera `wsrep` snapshot | `test-kit/podremediator/tasks/e2e-galera-wsrep-monitor.yml` |
| Enable by default for Galera wrapper | `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml` (`e2e_k8s_events_report: true`) |
| Document behaviour / tune | Comments in the same files; `test-kit/podremediator/scripts/run-e2e.sh` header |
| Doc typo fix (`e2e-galera-wsrep-monitor.yml` reference) | `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml` header |

**Extra variables**

- `e2e_k8s_events_max_lines` (default 200): tail size per namespace per snapshot.
- `e2e_k8s_events_report=false`: disable event dumps.

### 2.3 How to run (from repo root)

```bash
HAD18=<jump-host> SSHPASS=<password> SYNC=1 E2E_GALERA=1 \
  E2E_OC_CMD=/path/to/oc \
  E2E_OC_API_SERVER=https://api.<cluster>:6443 \
  E2E_PVC_TIMEOUT=1200 \
  E2E_EXTRA_ANSIBLE_ARGS='-e e2e_k8s_events_max_lines=400' \
  ./test-kit/podremediator/scripts/run-e2e.sh 2>&1 | tee /tmp/galera-e2e.log
```

`SYNC=1` rsyncs the repo to the lab jump host so the playbooks on the controller match your branch.

**Note:** OpenShift `install.yaml`-style CRDs can hit **annotation size limits** with client-side apply; use **server-side apply** for large third-party manifests (see Kyverno section).

---

## 3. Direct StatefulSet TSC patch (quick test only)

### 3.1 Purpose

Prove scheduler behaviour with TSC **without** changing operator code.

### 3.2 Implementation

Merge patch on `statefulset/openstack-galera` in namespace `openstack`:

```yaml
spec:
  template:
    spec:
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              service: openstack-galera
```

(Adjust `labelSelector` to match your pods’ labels if they differ.)

### 3.3 Limitations

- **Reverted** on the next mariadb-operator reconcile.
- **`oc rollout restart`** while the operator is scaled to **0** leaves new pods stuck on **gcomm** until the operator runs again.
- TSC is **soft** with `ScheduleAnyway`; **local PV** node affinity can still force colocation.

---

## 4. “Permanent” TSC without forking mariadb-operator: Topology CR

### 4.1 Purpose

Keep TSC in the **desired state** reconciled by the **MariaDB operator**, not fight it with manual STS edits.

### 4.2 Rationale and design

- **`GaleraSpecCore`** (mariadb-operator API) includes **`topologyRef`** pointing to an **infra-operator** `Topology` CR (`topology.openstack.org/v1beta1`).
- **`Topology.Spec`** supports **`topologySpreadConstraints`** and optional **`affinity`**.
- In **`mariadb-operator/pkg/mariadb/statefulset.go`**, when a resolved **`Topology`** object is non-nil, **`Topology.ApplyTo`** (infra-operator `apis/topology/v1beta1/topology_types.go`) copies **`topologySpreadConstraints`** (and optional affinity) into the StatefulSet **pod template**.
- When **`topologyRef` is unset**, the operator applies only **`affinity.DistributePods`** (preferred anti-affinity), **not** TSC.

**Important:** If you set **only** TSC in the `Topology` CR and leave **`affinity` unset**, you **no longer** get the default `DistributePods` soft anti-affinity branch. If you need both behaviours, add explicit **`spec.affinity`** to the `Topology` CR or extend operator merge logic in code.

### 4.3 Default label selector on TSC entries

`EnsureTopologyRef` fills an empty **`labelSelector`** on each TSC entry with the **StatefulSet service labels** for that Galera instance, so spread counts the correct pod set.

### 4.4 OpenStackControlPlane wiring

- **`OpenStackControlPlane.spec.galera.templates.<templateName>`** embeds **`mariadbv1.GaleraSpecCore`**, which includes **`topologyRef`**.
- **`openstack-operator/pkg/openstack/galera.go`** copies each template into the corresponding **`Galera`** CR via **`DeepCopyInto`**.
- On clusters where the control plane reconcile lagged, a **direct `oc patch galera …`** was still required once so the **`Galera`** spec matched the control plane; after that, both stayed aligned (verify on your version).

---

## 5. had-18 lab execution summary

### 5.1 Topology CR (correct namespace)

Create the Topology in the **same namespace as Galera** (here `openstack`). Avoid shell heredocs that reference unset variables on the remote host (a `Topology` was accidentally created in **`default`** and was deleted).

Example:

```yaml
apiVersion: topology.openstack.org/v1beta1
kind: Topology
metadata:
  name: galera-host-spread
  namespace: openstack
spec:
  topologySpreadConstraints:
    - maxSkew: 1
      topologyKey: kubernetes.io/hostname
      whenUnsatisfiable: ScheduleAnyway
```

Apply with:

```bash
oc apply -n openstack -f topology.yaml
```

### 5.2 OpenStackControlPlane patch

JSON patch on **`openstackcontrolplane/controlplane`** in **`openstack`**:

```json
[{"op": "add", "path": "/spec/galera/templates/openstack/topologyRef", "value": {"name": "galera-host-spread"}}]
```

(Use `replace` if the path already exists.)

### 5.3 Galera CR patch (when needed)

```bash
oc patch galera openstack -n openstack --type=merge \
  -p '{"spec":{"topologyRef":{"name":"galera-host-spread"}}}'
```

### 5.4 Observed outcome

- **`TopologyReady`** on `Galera` became **True**.
- **`StatefulSet/openstack-galera`** showed **`spec.template.spec.topologySpreadConstraints`** with populated **`labelSelector`** (including `service: openstack-galera`).
- StatefulSet reached **3/3** ready; **`wsrep_cluster_size=3`**, **`Primary`**.
- Members landed on **distinct workers** where PV topology allowed (lab-specific).

---

## 6. One Galera member per worker (data layout)

### 6.1 Purpose

Make PodRemediator Galera E2E and failure analysis predictable: **one member per worker**, one local volume per worker for the three ordinals.

### 6.2 Implementation (operational, lab only)

When two ordinals shared a worker (same PV node affinity):

1. **`oc adm cordon <worker>`** so new PVCs for a recreated ordinal cannot bind on that worker.
2. Delete the **pod** and **PVC** for the ordinal to move (e.g. `mysql-db-openstack-galera-2`).
3. Wait until the new pod is **Running** and the new PV’s node affinity is a **different** worker.
4. **`oc adm uncordon <worker>`**.

**Warning:** destroys data for that ordinal; Galera must re-SST / resync. **Not for production** without design and backup.

Automated single-step variant (dry-run by default; same cordon / PVC+pod delete / uncordon for the **highest ordinal** on the **most crowded** worker): `test-kit/podremediator/playbooks/playbook-galera-rebalance-workers-lab.yml`. Run with `-e galera_rebalance_execute=true` after reviewing the printed plan. Each E2E wsrep snapshot also prints **`--- <STS> members grouped by worker ---`** (see `tasks/e2e-galera-wsrep-monitor.yml`).

---

## 7. Kyverno-based admission (optional, not used successfully on had-18)

### 7.1 Purpose

Inject TSC at **Pod admission** if you cannot use the `Topology` CR path and refuse operator-off hacks.

### 7.2 Repository artifacts

| File | Role |
|------|------|
| `hack/galera-tsc-permanent/README.md` | Install notes, **server-side apply**, OpenShift **SCC**, signature issues |
| `hack/galera-tsc-permanent/kyverno-clusterpolicy-galera-pod-mutate.yaml` | Example `ClusterPolicy` mutating Galera-labelled pods |

### 7.3 had-18 outcome

1. First `kubectl/oc apply` of upstream `install.yaml` failed: **CRD metadata.annotations too long** (client-side last-applied).
2. **Reinstall with `oc apply --server-side --force-conflicts`** succeeded for CRDs and workloads.
3. Kyverno pods failed **`SignatureValidationFailed`** (cluster image policy / unsigned `ghcr.io` images).
4. **Cleanup:** delete `kyverno` namespace and Kyverno-related **CRDs** to leave the cluster clean.

For a future retry, mirror Kyverno images to a **trusted registry** or use a **certified operator** build, then re-apply the policy YAML.

---

## 8. References in this repository

| Topic | Path |
|-------|------|
| Galera E2E scenarios and lab layout | [`GALERA_E2E_TEST_SCENARIOS.md`](./GALERA_E2E_TEST_SCENARIOS.md) |
| StatefulSet anti-affinity vs operator, lab rebalance | Same doc, §1.4–1.5 |
| Ansible E2E playbooks | `test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation*.yml` |
| `wsrep` + Events snapshots | `test-kit/podremediator/tasks/e2e-galera-wsrep-monitor.yml` |
| Optional Kyverno path | `hack/galera-tsc-permanent/*` |
| Topology API (`ApplyTo`, TSC field) | `infra-operator` repo: `apis/topology/v1beta1/topology_types.go` |
| Galera STS construction | `mariadb-operator` repo: `pkg/mariadb/statefulset.go` |
| Control plane → Galera reconcile | `openstack-operator` repo: `pkg/openstack/galera.go` |

---

## 9. Summary table

| Approach | Persists across operator reconcile? | Notes |
|----------|--------------------------------------|--------|
| Patch STS only | No | Lost on reconcile |
| Operator scaled to 0 + STS patch | N/A | Breaks gcomm for new pods |
| **`Topology` CR + `topologyRef` on `Galera` / OSP template** | **Yes** | **Recommended product path** |
| Kyverno mutate | Yes (if admission runs) | Needs trusted images on OpenShift; separate lifecycle |
| Cordon + delete PVC (ordinal) | Layout only | Data loss for that member; lab procedure |

---

*Document generated from engineering notes; adjust API versions, namespaces, and names to match your environment.*
