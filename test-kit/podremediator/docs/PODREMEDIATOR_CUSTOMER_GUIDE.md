# PodRemediator – Customer guide

This guide explains **what PodRemediator does for you**, **why you would deploy it**, and **exactly what you must put in place** so that automated recovery of **node‑local storage** works in an **OpenShift** environment (for example **Red Hat OpenStack Services on OpenShift**). It is written for **platform owners, SREs, and support engineers** who need to explain or operate the feature.

For **implementation‑level** details (code paths, predicates, topology keys), see [`PODREMEDIATOR_OPERATOR_BEHAVIOR.md`](PODREMEDIATOR_OPERATOR_BEHAVIOR.md).

---

## 1. Business problem (why this exists)

### 1.1 What goes wrong without PodRemediator

Many OpenStack control‑plane and data‑plane components run as **StatefulSets** with **persistent volumes**. When you use **local or topology‑pinned storage** (for example **LVM Storage / LVMS**, **TopoLVM**, or similar CSI drivers), each **PersistentVolume (PV)** is tied to **one worker node** through **node affinity**.

If that **worker fails** (hardware, hypervisor crash, network partition, prolonged outage):

1. Kubernetes marks the node **NotReady** (or `Ready` condition is no longer `True`).
2. Pods that use volumes on that node **cannot run elsewhere** while the old **PersistentVolumeClaim (PVC)** still exists and remains bound to a PV on the dead node.
3. Even after **node remediation** tooling decides the node is unhealthy and remediation runs, the **PVC can remain**, blocking clean **reschedule** and **new volume provisioning** for the same logical claim name (typical StatefulSet pattern).

**Result:** services stay degraded or stuck until an operator **manually** identifies and **deletes** the correct PVCs—a slow, error‑prone process during an outage.

### 1.2 What PodRemediator adds

**PodRemediator** is an **infra‑operator** controller that **automatically deletes** PVCs that are:

- **Bound** to a volume,
- **Pinned to a node** that OpenShift currently considers **unhealthy** (`NodeReady` not `True`),
- Classified as **local / topology‑bound** in the way the controller understands (see technical doc),

**after** it confirms that **Node Health Check (NHC)** and **Self Node Remediation (SNR)** primitives from **medik8s** are present in the cluster.

Deleting the PVC allows the **StatefulSet controller** (or similar) to **recreate** the claim, provision a **new** volume on a **healthy** node, and bring the pod back **without manual PVC surgery**.

**Important:** PodRemediator **does not replace** NHC/SNR. It **complements** them by handling the **storage claim** side that node remediation alone does not fix.

---

## 2. What you must have installed (dependencies)

PodRemediator will **refuse to operate** (it sets its **Ready** condition to **False**) unless **all** of the following are true.

### 2.1 Infra operator with PodRemediator controller

- The **infra‑operator** image and deployment your product ships must include the **PodRemediator** reconciler and **CRD** for `PodRemediator` (`remediation.openstack.org/v1beta1`).
- **RBAC** must allow the controller to **list/watch** `Nodes`, `Pods`, `PVCs`, `PVs`, `StorageClasses`, and to **patch/delete** PVCs in the namespaces you configure. Missing RBAC shows up as reconcile errors in the operator pod logs.

### 2.2 medik8s: Node Health Check (NHC)

- At least one **`NodeHealthCheck`** resource must exist in the cluster (`remediation.medik8s.io/v1alpha1`).
- NHC is what **defines** how worker health is evaluated and ties into remediation. PodRemediator only checks **existence** of an NHC instance, not its internal selectors.

**Customer action:** install the **NHC operator** and apply a **valid `NodeHealthCheck` policy** that matches your worker fleet and operational standards.

### 2.3 medik8s: Self Node Remediation (SNR)

- At least one **`SelfNodeRemediationTemplate`** must exist (`self-node-remediation.medik8s.io/v1alpha1`).

**Customer action:** install the **SNR operator** and create a **template** so that unhealthy nodes can be remediated (reboot, reprovision, or other supported flows—according to your SNR configuration).

### 2.4 Summary dependency checklist

| Item | Customer responsibility |
|------|-------------------------|
| OpenShift cluster with worker nodes | Yes |
| **NHC** CRD + **≥1** `NodeHealthCheck` | Yes |
| **SNR** CRD + **≥1** `SelfNodeRemediationTemplate` | Yes |
| **infra‑operator** with PodRemediator + RBAC | Yes (platform/product install) |
| **PodRemediator** custom resource | Yes (see §4) |

Until this checklist is green, PodRemediator will report **not ready** and **will not delete any PVC**.

---

## 3. What you must configure (PodRemediator CR)

You need **at least one** `PodRemediator` object. Typical placement is the **OpenStack operators** namespace (for example `openstack-operators`), alongside other operator CRs—but the CR can live elsewhere if you align RBAC and watch semantics.

### 3.1 `spec.namespaces` (critical for OpenStack)

This field lists **which namespaces** PodRemediator scans for PVCs when nodes are unhealthy.

| If you set… | Then PodRemediator… |
|-------------|---------------------|
| **Empty list** `[]` or omit the field | Scans **only the namespace where the PodRemediator CR itself lives**. |
| **`["openstack"]`** (example) | Also scans **`openstack`** for PVCs to remediate. |
| **`["openstack-operators","openstack"]`** | Scans **both** (common when the CR is in `openstack-operators` but data‑plane PVCs live in `openstack`). |

**Common customer mistake:** deploying the CR in `openstack-operators` **without** listing `openstack` in `spec.namespaces`. In that case **no RabbitMQ, Galera, or other OpenStack workload PVC** in `openstack` is ever considered—only PVCs in `openstack-operators`.

**Recommendation:** explicitly set **every namespace** that hosts **StatefulSets with node‑local PVCs** you want remediated.

### 3.2 `spec.disabled`

| Value | Behavior |
|-------|----------|
| **`false`** (default) | Applying the CR expects remediation **on** when prerequisites are satisfied (aligned with Instance HA **DISABLED** default **false**). |
| **`true`** | Controller reports **Ready** but **never** deletes PVCs—use for “observe only” or staged rollouts. |

---

## 4. End‑to‑end flow (what the customer should expect)

This is the **logical** sequence; wall‑clock times depend on NHC/SNR timers and storage provisioner speed.

1. **Normal operation** – PodRemediator is **Ready**, nodes are healthy, nothing is deleted.
2. **Worker failure** – A worker becomes **unhealthy**; Kubernetes sets **`NodeReady`** to something other than **`True`** for that node.
3. **NHC / SNR** – Your configured remediation pipeline runs (reboot, fence, replace—**per your SNR template**). PodRemediator does **not** perform these steps.
4. **PodRemediator reconcile** – Triggered by **Node** (and **Pod/PVC** events in watched namespaces), the controller:
   - Confirms NHC + SNR still exist.
   - Builds the set of **unhealthy** node names.
   - For each **Bound** PVC in each **configured namespace**, loads the PV and checks **local/topology** and **node affinity**.
   - If the PV’s node is **unhealthy**, it **annotates** the PVC with a pending‑deletion marker (for crash safety) and **deletes** the PVC.
5. **Workload recovery** – The **StatefulSet** (or other owner) sees the PVC gone, creates a **new** PVC, storage is **provisioned** on a viable node, pod schedules, application starts **cold** on new storage (for that replica).

**Customer expectation:** there **will** be **service disruption** for that replica until the new volume is bound and the application finishes start‑up (e.g. database resync). This is **not** a live migration of data from the dead node.

---

## 5. Workload and storage requirements (what gets remediated)

### 5.1 PVCs that **can** be selected

Roughly, **all** of the following must hold:

1. PVC is in a **watched** namespace (§3.1).
2. PVC is **Bound** (`spec.volumeName` non‑empty). **Pending** claims are **ignored**—fix scheduling/storage issues separately.
3. Backing PV is treated as **“local‑like”** by the controller: **local volume**, or **CSI / hostPath** with **required node affinity** (see technical document for exact rules).
4. The controller can read a **node name** from PV node affinity using known topology keys (e.g. `kubernetes.io/hostname`, `topology.topolvm.io/node`, `topology.lvms.io/node`, and limited CSI fallbacks).
5. That node name is in the **unhealthy** set at reconcile time.

### 5.2 PVCs that **never** get touched

- **Unbound** PVCs.
- PVs **without** the local/topology pattern the controller recognizes.
- PVCs whose PV is on a **healthy** node.
- PVCs in namespaces **not** listed in `spec.namespaces` (when the CR is not in that namespace and the list does not include it).

### 5.3 Implications for applications

- **Data on the old volume is gone** once the PVC is deleted (unless the storage system does something outside Kubernetes—do **not** assume silent recovery).
- **Quorum / clustered** services (RabbitMQ quorum queues, Galera, etc.) may need **enough surviving replicas** and correct **operator** behavior after one member’s storage is wiped; validate per application **before** production.
- **PodRemediator does not delete Pods** in the current implementation path; recovery is driven by **PVC deletion** and higher‑level controllers.

---

## 6. Operational guidance for customers

### 6.1 Day‑one validation (recommended)

After install:

1. `oc get podremediator -A` → confirm **Ready=True** (or understand **False** message).
2. If **False** with **NHC/SNRNotFound** → install/configure NHC and SNR CRs (§2).
3. Confirm **`spec.namespaces`** includes every namespace with **local StatefulSet** PVCs you care about.
4. Review **infra‑operator** pod logs filtered for `PodRemediator` during a **controlled** failure test in a non‑production cluster.

### 6.2 During an incident

1. Identify **unhealthy nodes**: `oc get nodes` and describe the node.
2. Check **PodRemediator** status and message.
3. Check **PVCs** in workload namespaces: are stuck PVCs **Bound** to PVs on the bad node?
4. Confirm **NHC/SNR** events and remediation objects per your runbooks.
5. **After** remediation, verify StatefulSet **replica readiness** and application‑specific **cluster status** (e.g. DB cluster show status).

### 6.3 Annotation `remediation.openstack.org/podremediator-pending-deletion`

If you see this annotation on a PVC, PodRemediator has **marked intent to delete** while the node was unhealthy. If a PVC **remains** with this annotation but was **not** deleted (e.g. API error), treat it as **inconsistent state**: investigate operator logs; **do not** assume partial completion is safe without review.

---

## 7. Limitations and support boundaries (set expectations)

1. **Node health signal is simplified** – PodRemediator uses **`NodeReady != True`**. It does **not** embed NHC’s internal state machine; edge cases depend on how quickly the node object reflects reality.
2. **Not a backup solution** – Remediation **destroys** the claim to the old volume. **Backups and replication** remain the customer’s responsibility.
3. **Operator constraints upstream of PodRemediator** – Some workload operators **forbid** certain scale‑down paths or require manual steps for **split‑brain** scenarios. PodRemediator does not know those rules; **test per workload**.
4. **Versioning** – Behavior matches the **shipped infra‑operator** version. After upgrades, re‑validate CRDs, RBAC, and default field behavior.
5. **Multi‑tenant clusters** – Any principal that can influence **Node** readiness or **PVC** binding in watched namespaces can indirectly affect remediation; use **RBAC** and **namespace scope** deliberately.

---

## 8. FAQ

**Q: Do we still need fencing / ILO / power management?**  
A: PodRemediator does **not** remove the need for your **platform** remediation story; it addresses **Kubernetes PVC stickiness** after (or while) the node is considered unhealthy.

**Q: Will this delete PVCs on a flapping node?**  
A: If the node’s **`NodeReady`** condition is not `True` during reconcile and all PVC rules match, **yes**, deletion can occur. Tune **NHC** and cluster behavior to avoid flapping where that is unsafe.

**Q: Can we limit remediation to specific storage classes?**  
A: The current implementation keys off **PV shape** (local/CSI + affinity), **not** storage class name. Narrowing by class would be a **product enhancement** request.

**Q: Where do we open support cases?**  
A: Follow your **Red Hat / vendor support** process for **OpenStack on OpenShift** and **infra‑operator**; attach `PodRemediator` YAML, `Node` describe, `PVC`/`PV` YAML, NHC/SNR object summaries, and **infra‑operator** logs.

---

## 9. Quick reference – customer checklist

- [ ] **NHC** installed and at least one **`NodeHealthCheck`** exists.  
- [ ] **SNR** installed and at least one **`SelfNodeRemediationTemplate`** exists.  
- [ ] **infra‑operator** running with PodRemediator + **RBAC** for nodes, pods, PVCs, PVs.  
- [ ] **`PodRemediator` CR** applied with correct **`spec.namespaces`**.  
- [ ] **`disabled`** left **false** for active remediation (set **true** only for observe-only).  
- [ ] **StatefulSets** use storage that exposes **node affinity** compatible with the controller.  
- [ ] **Runbooks** updated: PVC deletion is **destructive**; application owners informed.  
- [ ] **Non‑prod test** completed for each critical StatefulSet (RabbitMQ, Galera, etc.).

---

*Document version: aligned with PodRemediator reconciler in infra‑operator; for code‑level updates see `internal/controller/remediation/podremediator_controller.go`.*
