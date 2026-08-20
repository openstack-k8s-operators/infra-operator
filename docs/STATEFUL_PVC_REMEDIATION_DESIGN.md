# Stateful PVC Remediation: Context and Design Reference

This document captures the agreed context for stateful PVC deletion and remediation, describes how the existing BGP controller in infra-operator works (as the reference pattern), and documents the current **PodRemediator** implementation.

**Contents**

| Section | Description |
|---------|-------------|
| 1 | Context and agreed approach (problem, principles, proposal, NHC/SNR dependency); **§1.5** Galera–operator contract; **§1.5.1** exploratory Galera testing (same scenario shape as RabbitMQ) |
| 2 | How the BGP controller works (reference pattern for PodRemediator) |
| 3 | Implementation plan: Phase 1 (prototype) and Phase 2 (service-specific) |
| 4 | **Implementation (Phase 1)** – API, controller flow, watches, local PV detection, RBAC, sample CR, limitations |
| 5 | User guide – prerequisites, install, disable remediation, multiple namespaces |

---

## 1. Context and Agreed Approach

### 1.1 Problem Statement

Stateful workloads that use **local persistent storage** (e.g. Galera, RabbitMQ) face a recurring issue during node failures:

- When a node hosting a pod with a locally bound **PersistentVolumeClaim (PVC)** becomes unavailable, the pod may be rescheduled but the **PVC remains bound to that specific node**.
- Local storage cannot be reattached to another node, so the pod **cannot start elsewhere** and the application stays in a degraded or unrecoverable state.
- **Self Node Remediation (SNR)** with strategies such as **ResourceDeletion** or **Automatic** does **not** remove PVCs; only Pods and VolumeAttachments are affected. Recovery therefore requires **manual PVC deletion** until a dedicated mechanism exists.

Kubernetes and OpenShift do not provide a native way to decide when it is safe to delete and recreate such PVCs, or to coordinate this with node remediation.

### 1.2 Agreed Principles

From the design discussions (including the Galera PVC Deletion meeting of 2025-04-30 and the Generic Design Proposal of 2025-10-09):

1. **Operator-driven authorization**  
   Only the **application operator** (e.g. Galera, RabbitMQ) should decide when PVC deletion is safe. It has the right context (e.g. quorum, replication, corruption) to avoid data loss.

2. **Externalized action**  
   The actual **deletion of the PVC** should be performed by a **separate, generic remediation controller**, not by embedding complex remediation logic inside each application operator. This keeps operators focused and avoids duplication.

3. **Signal-based integration**  
   The application operator exposes an explicit signal (e.g. annotation such as `remediation.openshift.io/safe-to-delete: "true"` on the PVC or Pod). The remediation controller **watches for this signal** and, when combined with node-unhealthiness (e.g. NotReady / unreachable), deletes the PVC so that the StatefulSet can recreate it and reschedule the pod on a healthy node.

4. **Alignment with NHC/SNR**  
   The design should fit the existing OpenShift remediation ecosystem: **Node Health Check (NHC)** and **Self Node Remediation (SNR)**. The remediation controller may react to annotations or conditions set by NHC/SNR (or by the application operator) to know when a pod/node is marked for remediation.

### 1.3 Proposal for infra-operator (from team discussion)

- Implement a **new controller in infra-operator** that follows the **same pattern as the existing BGP controller** (see [PR 322](https://github.com/openstack-k8s-operators/infra-operator/pull/322)).
- Introduce a small CR (e.g. **PodRemediator** or similar) that **activates** the controller and provides configuration (e.g. namespace, options). The controller then:
  - **Watches workers** (and/or pods) for **annotations set by SNR/NHC** (and optionally by application operators).
  - **Watches PVCs** in scope.
  - When a **pod is marked for termination** (e.g. node unhealthy, remediation in progress) and the pod uses a local PVC bound to that node, the controller **deletes the PVC** so the workload can respawn on another node.
- **Optional later extension**: the mariadb-operator (or others) could annotate pods depending on role (e.g. master vs replica) to make remediation safer.
- **Scope for the first phase**: Keep Galera/mariadb-specific logic out of the initial implementation. Build a **prototype with a simple Pod + PVC**; once the mechanism is in place, add service-specific behaviour (e.g. “mark node unhealthy when DB is corrupted”) in a generic way.

A relevant SNR behaviour change to take into account: [medik8s/self-node-remediation commit cb31a13f](https://github.com/medik8s/self-node-remediation/commit/cb31a13f35931e5d1c206751c676f91e0105f48e).

#### 1.3.1 Controller behaviour and dependency on SNR/NHC (from team discussion)

The controller must **depend on SNR and NHC** being present; without them it must not proceed.

1. **Initial check**  
   When reconciling the PodRemediator CR, the controller **first checks whether SNR and NHC are installed/present** in the cluster (or in the relevant namespace, depending on how they are deployed).

2. **If SNR or NHC are missing**  
   - **Do not** start monitoring workers or performing any remediation (e.g. PVC deletion).
   - Set the CR **status** so that the resource is clearly not ready: e.g. **ReadyCondition False** (or the equivalent condition used in the operator), with a message stating that **Node Health Check (NHC) and Self Node Remediation (SNR) are required** and that the controller cannot proceed without them.

3. **If SNR and NHC are found**  
   - Proceed to **monitor worker nodes** (and pods/PVCs as designed).
   - The actual remediation (e.g. PVC deletion) should align with **when SNR kicks in** (e.g. node marked for remediation by NHC, then SNR runs). The monitoring logic for "worker unhealthy until SNR starts" may reuse or align with what was already explored during **mariadb-operator testing** (e.g. node health, annotations, or conditions set by NHC/SNR).

### 1.4 Related references

- **Jira EPIC**: [OSPRH-14880](https://issues.redhat.com/browse/OSPRH-14880) – Stateful PVC Deletion Handling and Remediation  
- Node Health Check Operator (NHC), Self Node Remediation (SNR), Kubernetes StatefulSet documentation.

### 1.5 Galera operator integration

Workloads validated with **RabbitMQ** do not prove **Galera** is safe to automate the same way: Galera members hold **replicated cluster state**; naive PVC deletion or eviction ordering can break **quorum**, **bootstrap**, or **data authority** expectations. The generic PodRemediator prototype (Phase 1) deliberately avoids Galera-specific logic; **production Galera remediation requires an explicit contract** with the Galera (MariaDB) operator owners.

**Agreed separation of concerns (engineering discussion, aligned with §1.2):**

1. **Facts vs decisions**  
   PodRemediator (or the broader node-remediation path) should surface **platform facts**—for example that a given member **is no longer reachable**—in a form the **Galera operator** consumes. The **Galera operator** decides **safe recovery**: e.g. which members remain authoritative, whether to **rebootstrap** or reform the cluster on **surviving nodes** (including scenarios where only **two** nodes remain viable).

2. **Integration surface**  
   One concrete direction discussed is for remediation to **set a field or condition on the Galera custom resource** (exact name and semantics TBD) meaning “this pod/member is unreachable,” so the operator can drive bootstrap policy without PodRemediator inferring Galera topology from PVC labels alone.

3. **PVC deletion is a separate policy layer**  
   Whether PVCs are removed **automatically**, **manually**, or by **heuristics** should not be collapsed into Galera bootstrap logic. Policy may depend on **storage class** (e.g. **local / node-bound** vs other **CSI** behaviour), **blast radius** (e.g. avoiding mass automatic deletion of many PVCs of the same class), and signals such as **`safe-to-delete`** (see §1.2 and Phase 2 notes elsewhere in this document).

4. **Operator-side evolution**  
   Component owners described a model where **pods report attributes** and the **Galera operator chooses which pod to bootstrap from**. PodRemediator integration should align with that model once stable; implementation may **overlap other operator reworks**, so **design before code** and **coordination on branch/ownership** are required.

5. **Cluster status**  
   Exposing **authoritative cluster or member status** (e.g. in CR `.status`, or another agreed contract) helps both PodRemediator and the Galera operator avoid guessing from pod logs.

6. **Other data-plane components**  
   **OVN** (raft) was raised as a **possibly different** risk profile than Galera (e.g. eviction of a failed peer); that is **not** a substitute for testing. Treat **per-workload** validation as mandatory.

#### 1.5.1 Agreed next step: exploratory testing (before new contracts)

The team agreed to **reuse the same lab/E2E scenario shape as RabbitMQ** for **Galera** first: same prerequisites (NHC, SNR, PodRemediator with the workload namespace in scope, worker failure such as killing the node hosting a Galera member), then **observe** what happens to **PVCs**, **pods**, **Galera cluster status**, and **application health** with the **current** generic PodRemediator (no Galera-specific signals in code yet). From that baseline, **document corner cases** (quorum, bootstrap, split-brain risk, operator behaviour, recovery time) and only then prioritize **operator contract** changes or heuristics. Automation: **`test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml`** (or **`E2E_GALERA=1`** with `test-kit/podremediator/scripts/run-e2e-on-had18.sh`). See **`test-kit/podremediator/docs/STATEFUL_PVC_REMEDIATION_TEST_PLAN.md`** (M3.5), **`test-kit/podremediator/docs/GALERA_E2E_TEST_SCENARIOS.md`** (lab baselines, topology, wsrep monitoring), and **`CLAUDE.md`** §6.

This subsection does not define API fields; it records **intent** so Phase 2 work and Jira line up with the **operator-driven, signal-based** principles in §1.2.

---

## 2. How the BGP Controller Works (Reference Pattern)

The following section describes the BGP controller in infra-operator in detail. This pattern is the template for the new Pod Remediation controller: a **CR that enables and configures the controller**, **watches on external resources** (Pods, and in BGP’s case FRRConfiguration), and a **Reconcile loop** that computes desired state from the cluster and applies it.

### 2.0 In plain words: what the BGP controller does

- **Without the CR**: The controller is running in infra-operator but **does nothing**. It has no BGPConfiguration to reconcile, so it effectively sits idle.
- **When you create the CR** (e.g. a `BGPConfiguration` in a namespace): The controller **finds** that CR and **starts doing work**. It begins watching Pods (in that namespace), creating/updating/deleting FRRConfiguration resources as needed.
- So the CR is a **switch**: same controller code, no CR ⇒ no activity; create the CR ⇒ the controller starts acting. We can do the same for PodRemediator: create a "PodRemediator" (or similarly named) CR, and only then does the remediation logic run (subject to the SNR/NHC dependency in 1.3.1).

### 2.1 Role of the CR (BGPConfiguration)

The **BGPConfiguration** custom resource has two roles:

1. **Activate the controller**  
   As long as at least one BGPConfiguration exists (typically one per namespace where BGP is used), the controller has “work to do” for that namespace.

2. **Configure behaviour**  
   The spec is minimal: it mainly tells the controller **where** to create downstream resources (e.g. `frrConfigurationNamespace: metallb-system`) and optionally how to match nodes to FRR configs (`frrNodeConfigurationSelector`). The CR does **not** list which Pods to manage; the controller discovers them via watches and list operations.

Example:

```yaml
apiVersion: network.openstack.org/v1beta1
kind: BGPConfiguration
metadata:
  name: bgpconfiguration-sample
spec:
  frrConfigurationNamespace: metallb-system
```

So: “there is a BGPConfiguration in this namespace” means “in this namespace, manage FRRConfiguration objects for Pods that have additional networks”.

### 2.2 Reconcile entry point and ownership

- The **primary resource** reconciled is **BGPConfiguration**. Every reconcile request is keyed by the **BGPConfiguration’s namespaced name**, not by Pod or FRRConfiguration.
- The reconciler:
  - **Fetches** the BGPConfiguration.
  - Ensures **finalizer** and **status conditions** (e.g. Ready, ServiceConfigReady).
  - If the BGPConfiguration is **being deleted**, runs **reconcileDelete** (remove owned FRRConfigurations, then remove finalizer).
  - Otherwise runs **reconcileNormal**, which implements the main logic described below.

So all logic is “from the CR’s perspective”: “for this BGPConfiguration, what is the desired set of FRRConfigurations, and what must I create/update/delete?”

### 2.3 What the controller watches (SetupWithManager)

The controller is registered with **controller-runtime** as follows:

1. **For(&networkv1.BGPConfiguration{})**  
   Any create/update/delete of a BGPConfiguration produces a reconcile request for that BGPConfiguration.

2. **Watches(&corev1.Pod{}, podFN, builder.WithPredicates(pPod))**  
   - When a **Pod** changes, a **map function** `podFN` is invoked with that Pod. It **lists all BGPConfigurations in the Pod’s namespace** and returns one reconcile request per such BGPConfiguration. So: any “relevant” Pod event triggers a reconcile of **every** BGPConfiguration in that namespace.
   - The **predicate** `pPod` filters which Pod events are considered “relevant” (e.g. only Pods with a Multus/NAD annotation so that not every Pod in the cluster triggers reconciles).

3. **Watches(&frrk8sv1.FRRConfiguration{}, frrFN, builder.WithPredicates(pFRR))**  
   - When a **FRRConfiguration** changes, `frrFN` uses a **label** on the FRRConfiguration (encoding the BGPConfiguration’s namespace) to find which BGPConfiguration(s) to reconcile, and returns reconcile requests for those.
   - The **predicate** `pFRR` restricts to FRRConfigurations that are associated with this controller (same label).

So the controller **does not** watch a single resource type; it reacts to:

- Changes to the **primary resource** (BGPConfiguration),
- Changes to **Pods** (in the same namespace as a BGPConfiguration),
- Changes to **downstream resources** it creates (FRRConfiguration), so that external modifications or deletions are reconciled back.

### 2.4 Reconcile flow (reconcileNormal)

When reconciling a **non-deleted** BGPConfiguration, the controller:

1. **Lists Pods** in the BGPConfiguration’s namespace.
2. **Filters** to “interesting” Pods via `getPodNetworkDetails`: only Running Pods with the right network attachment annotation and valid network status; Pods in deletion are skipped so their FRRConfiguration can be cleaned up.
3. **Lists FRRConfigurations** in the namespace specified in `instance.Spec.FRRConfigurationNamespace` (e.g. `metallb-system`).
4. For each such Pod, determines the node it runs on and finds the corresponding “base” FRRConfiguration for that node (from MetallB or from `frrNodeConfigurationSelector`). It then **creates or patches** an **owned** FRRConfiguration that advertises the Pod’s prefixes (one FRRConfiguration per Pod).
5. **Deletes** any FRRConfiguration that is owned by this BGPConfiguration but no longer has a corresponding active Pod (e.g. Pod deleted or not Running).

So the desired state is: “for every relevant Pod in the BGPConfiguration’s namespace, there is exactly one FRRConfiguration reflecting that Pod’s BGP prefixes; all other FRRConfigurations we own that no longer match a Pod must be removed.”

### 2.5 Summary: pattern for a remediation controller

- **CR** (e.g. PodRemediator): exists once per namespace (or cluster), and specifies scope/options (e.g. namespace, which annotations to trust).
- **For(CR)** so that create/update/delete of the CR triggers reconcile.
- **Watches** on:
  - **Pods** (and optionally **Nodes**): map function turns a Pod/Node event into reconcile requests for the CR (e.g. “which PodRemediator instance is responsible for this namespace?”).
  - **PVCs** (optional): to react when a PVC is created/updated/deleted and align state.
- **Predicates** so that only relevant events (e.g. Pod with “remediation” or “safe-to-delete” annotation, Node NotReady) trigger work.
- **Reconcile**: always receives the **CR’s** namespaced name; inside Reconcile, the controller **lists** Pods, Nodes, and PVCs as needed, decides which PVCs to delete based on node health and operator/SNR signals, and performs deletions. Status can be written back on the CR.

This matches how the BGP controller works and provides a clear template for a full design and implementation of the stateful PVC remediation controller in infra-operator.

---

## 3. Implementation Plan: Prototype First, Then Service-Specific

As agreed (lmiccini): *"We can build a prototype with a simple pod with a PVC, once we have the code ironed out we can start building some sort of service-specific knowledge."*

### Phase 1: Prototype with a simple Pod + PVC

**Goal**: Prove the end-to-end flow with **no** Galera/mariadb/application-specific logic. One controller, one CR, one test workload (simple Pod + local PVC). Once this works and the code is stable, we add service-specific behaviour.

**What to do**:

1. **Add API and CR (e.g. PodRemediator)**  
   - New API group/version (or reuse an existing one in infra-operator, e.g. under a remediation/instanceha-style API).  
   - CR with minimal spec: e.g. namespace(s) to watch, optional toggles (e.g. `disabled` omitted or false — remediation enabled by default).  
   - Status: conditions (Ready, and e.g. NHC/SNR dependency met).

2. **Implement the controller (same pattern as BGP)**  
   - **SetupWithManager**: `For(PodRemediator)`, `Watches` on Pods and optionally Nodes/PVCs; map events to PodRemediator reconcile requests; predicates to filter relevant changes.  
   - **Reconcile** (high level):  
     - If CR is being deleted → cleanup, remove finalizer.  
     - **Dependency check**: if NHC or SNR are not present → set ReadyCondition False + message "NHC and SNR required", return (no monitoring, no PVC deletion).  
     - If NHC/SNR are present → set condition that dependency is met, then:  
       - List/watch workers (and pods with PVCs in scope).  
       - When a worker is unhealthy and NHC/SNR have marked it for remediation (e.g. annotations or status we can rely on), find Pods on that node that use **local** PVCs.  
       - For those Pods (and their PVCs), apply the remediation policy: e.g. delete the PVC so the workload (StatefulSet or similar) can recreate it and reschedule the pod on a healthy node.  
   - Reuse or align with the logic already explored in **mariadb-operator testing** for "node unhealthy / SNR kicks in" (see 1.3.1).

3. **Test workload: simple Pod + PVC**  
   - Deploy a **StatefulSet** (or a single Pod with a PVC) that uses a **local** PersistentVolume (e.g. `local` volume type or similar in the test env).  
   - No Galera, no RabbitMQ: just a minimal app (e.g. sleep or a tiny server) so that when the node goes down and the PVC is deleted by the controller, the pod can respawn elsewhere and we can verify the flow.  
   - Run on a cluster where **NHC and SNR** are installed and configured (e.g. ResourceDeletion).  
   - Test: make a worker unhealthy (or simulate), let NHC/SNR react, confirm the controller deletes the stuck PVC and the pod is recreated on a healthy node.

4. **Iron out the code**  
   - Edge cases (e.g. two nodes down, timing, double deletion).  
   - Status and conditions clear and stable.  
   - Docs and (if applicable) unit/integration tests so the behaviour is well defined.

**Out of scope in Phase 1**: Any logic that depends on the *kind* of workload (e.g. "only delete PVC if this is a Galera replica", or "wait for application operator to set safe-to-delete"). For the prototype, the controller can rely only on NHC/SNR and node/pod state.

### Phase 2: Service-specific knowledge

**Goal**: Once the prototype is solid, add **optional** behaviour that depends on the workload type.

**What to do (later)**:

- **Application operator signals**: e.g. mariadb-operator (or Galera operator) sets an annotation like `remediation.openshift.io/safe-to-delete: "true"` on the PVC or Pod when it is safe to discard the volume (e.g. replica, or after quorum check). The remediation controller then **only** deletes PVCs that have this signal (or that match a policy that includes this check).  
- **Role-aware behaviour**: e.g. do not delete a PVC for a "master" pod until the operator has demoted it or marked it safe.  
- **Other stateful workloads**: same pattern for RabbitMQ or others (operator sets signal, generic controller deletes PVC when safe and node is remediated).

This keeps the controller **generic** in Phase 1 and adds service-specific knowledge in Phase 2 without rewriting the core flow.

---

## 4. Implementation (Phase 1) – PodRemediator

This section documents the current implementation of the PodRemediator controller in infra-operator (Phase 1 prototype).

### 4.1 API and Custom Resource

**API group**: `remediation.openstack.org/v1beta1`  
**Kind**: `PodRemediator`  
**Scope**: Namespaced  

**Location in repo**:
- Types: `apis/remediation/v1beta1/podremediator_types.go`
- GroupVersion: `apis/remediation/v1beta1/groupversion_info.go`
- CRD: `config/crd/bases/remediation.openstack.org_podremediators.yaml` (generated by `make manifests`)

#### 4.1.1 Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `namespaces` | `[]string` | (empty) | List of namespaces to watch for PVCs. If empty, only the CR's namespace is watched. |
| `disabled` | `bool` | `false` | When **true**, skips PVC deletion (monitoring only), aligned with Instance HA **DISABLED**. When **false** (default), applying the CR expects remediation **enabled** once other gates pass. |
| `maxUnhealthyNodes` | `int32` | `0` | Cluster-wide **disruption budget**: when **> 0**, skip PVC remediation while the count of **NotReady** nodes is **strictly greater** than this value; **0** means no limit. |

#### 4.1.2 Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | `condition.Conditions` | Standard lib-common conditions. |

**Conditions used**:
- **Ready**: Overall readiness. False when NHC/SNR are missing or when an error occurs; True when the controller is monitoring and (unless `disabled` is true) may delete PVCs.
- **InputReady**: Reflects whether the NHC/SNR dependency is satisfied. False with reason `NHC/SNRNotFound` and message *"Node Health Check (NHC) and Self Node Remediation (SNR) are required; controller cannot proceed without them"* when at least one of NHC or SNR is not present or has no instances.

**Print columns** (in `oc get podremediator`): `Status` (Ready condition status), `Message` (Ready condition message).

### 4.2 Controller: PodRemediatorReconciler

**Location**: `internal/controller/remediation/podremediator_controller.go`

**Dependencies**:
- `client.Client` (controller-runtime)
- `Scheme`, `Kclient` (kubernetes.Interface)
- `DynamicClient` (dynamic.Interface) – used to list NHC and SNR resources without importing medik8s APIs

#### 4.2.1 Reconcile flow (summary)

1. **Fetch** the `PodRemediator` instance; return if not found.
2. **Helper and conditions**: initialise status conditions if new; use a **defer** to restore condition timestamps, mirror Ready from sub-conditions, and **patch status** on exit (any patch error is returned as the reconcile error).
3. **Finalizer**: add the default finalizer if not deleting; first time causes an immediate requeue.
4. **Deletion**: if `DeletionTimestamp` is set, run **reconcileDelete** (remove finalizer only; no owned resources to clean up), then return.
5. **reconcileNormal** (see below).

#### 4.2.2 reconcileNormal (detailed)

1. **NHC/SNR dependency check**  
   The controller uses the **dynamic client** to list:
   - `remediation.medik8s.io/v1alpha1` → resource `nodehealthchecks`
   - `self-node-remediation.medik8s.io/v1alpha1` → resource `selfnoderemediationtemplates`  

   If either API is not present (e.g. CRD not installed) or the list is empty, the controller:
   - Sets **InputReady** and **Ready** to **False** with reason `NHC/SNRNotFound` and the fixed message that NHC and SNR are required.
   - Returns without performing any monitoring or PVC deletion.

2. **disabled true**  
   If `spec.disabled` is true, the controller sets Ready True and returns (monitoring only, no PVC deletion).

3. **Namespaces**  
   If `spec.namespaces` is empty, the controller uses a single namespace: the CR's namespace. Otherwise it uses the given list.

4. **Unhealthy nodes**  
   The controller lists all **Nodes** and considers a node **unhealthy** when its **Ready** condition is not `True` (i.e. `NodeReady` is False or Unknown). No other conditions or NHC/SNR annotations are evaluated in this phase; the intent is to align with “node not ready” as a simple proxy for “node may be remediated or unreachable”.

5. **MaxUnhealthyNodes (disruption budget)**  
   If there are **no** unhealthy nodes, the controller sets Ready True with a monitoring message and returns.  
   If `spec.maxUnhealthyNodes` is **greater than zero** and the count of unhealthy nodes is **strictly greater** than that value, the controller sets Ready True and **skips** PVC remediation for this reconcile (including pending-deletion resume in the same pass).

6. **PVC remediation**  
   For each watched namespace, the controller:
   - Lists **PersistentVolumeClaims** in that namespace.
   - For each PVC that has `spec.volumeName` set, fetches the corresponding **PersistentVolume**.
   - Skips the PVC if the PV is not considered **local** (see 4.3).
   - Extracts the **node name** from the PV’s node affinity (see 4.3).
   - If that node is in the **unhealthy** set, the controller **deletes the PVC**.  
   Deletion is best-effort per PVC (errors are logged; the controller continues and still marks Ready True).

7. **Status**  
   Ready is set True with a message indicating that the controller is monitoring and has remediated PVCs on unhealthy nodes if any.

### 4.3 Local PV and node affinity

A **PersistentVolume** is treated as **local** (and thus eligible for remediation when its node is unhealthy) if it has **required node affinity** and at least one of:

- `spec.local` is set (Kubernetes local volume type), or  
- `spec.csi` is set and `spec.nodeAffinity.required` has at least one node selector term, or  
- `spec.hostPath` is set and `spec.nodeAffinity.required` has at least one node selector term.

The **node name** is taken from the PV’s `spec.nodeAffinity.required.nodeSelectorTerms`: the first occurrence of a **match expression** (or match label) for `kubernetes.io/hostname` is used. Only **MatchExpressions** are currently evaluated; the value is `expr.Values[0]` when the key is `corev1.LabelHostname`.

### 4.4 Watches (SetupWithManager)

The controller registers:

- **For**(`remediationv1.PodRemediator{}`): every create/update/delete of a PodRemediator triggers a reconcile for that CR.
- **Watches**(Pods): map function lists all PodRemediators in the Pod’s **namespace** and enqueues reconcile requests for each. Predicate: `GenerationChangedPredicate` (reduces noise from status-only updates).
- **Watches**(Nodes): map function lists **all** PodRemediators in the cluster (no namespace filter) and enqueues each. No predicate – any node change can trigger reconcile so that Node Ready changes are observed.
- **Watches**(PVCs): map function lists all PodRemediators in the PVC’s **namespace** and enqueues each. Predicate: `GenerationChangedPredicate`.

So: Pod and PVC events drive reconcile only for PodRemediators in the same namespace; Node events drive reconcile for every PodRemediator.

### 4.5 RBAC

The controller requires (generated from kubebuilder markers and reflected in `config/rbac/role.yaml` after `make manifests`):

- **remediation.openstack.org**: podremediators (full) and status/finalizers (update/patch).
- **core**: nodes (get, list, watch); pods (get, list, watch, delete); persistentvolumeclaims (get, list, watch, delete); persistentvolumes (get, list, watch).
- **storage.k8s.io**: storageclasses (get, list, watch).
- **remediation.medik8s.io**: nodehealthchecks (get, list, watch).
- **self-node-remediation.medik8s.io**: selfnoderemediationtemplates (get, list, watch).

### 4.6 Sample CR and usage

Example PodRemediator (see `config/samples/remediation_v1beta1_podremediator.yaml`):

```yaml
apiVersion: remediation.openstack.org/v1beta1
kind: PodRemediator
metadata:
  name: podremediator-sample
spec:
  namespaces: []           # empty = only this namespace
  # disabled defaults false (remediation on when NHC/SNR present)
```

- Create the CR in the namespace where you run stateful workloads with local PVCs (or list multiple namespaces in `spec.namespaces`).
- If NHC or SNR are not installed or have no resources, the CR will show Ready=False and Message that NHC and SNR are required.
- Once NHC and SNR are present, the controller marks Ready=True and, when it sees a node with Ready≠True, deletes any local PVCs (in the watched namespaces) bound to that node so the workload can recreate the PVC and reschedule.

### 4.7 Limitations and future work (Phase 1)

- **Unhealthy node definition**: Only the node’s **Ready** condition is used. There is no integration yet with NHC/SNR annotations or remediation CRs (e.g. “remediation in progress” or “remediated”). So the controller may delete PVCs as soon as the node is NotReady, which can be earlier than when SNR actually fences the node.
- **No application-level signal**: Phase 1 does not require or check any “safe-to-delete” annotation from an application operator. All local PVCs on an unhealthy node are eligible for deletion.
- **Single node name from PV**: Node name is derived only from `kubernetes.io/hostname` in the first matching node selector term; other affinity shapes are not supported.
- **No grace period**: There is no configurable delay or fencing check before deleting a PVC; future work could add a delay or rely on NHC/SNR remediation state.
- **Cluster-wide disruption budget (optional)**: `spec.maxUnhealthyNodes` (non-negative; **0** = off) can **skip all PVC remediation** while the number of **NotReady** nodes is **above** the limit—similar in spirit to a **PodDisruptionBudget** cap, but keyed on unhealthy **nodes** rather than pods. Useful for **3-replica** Galera-style clusters where operators set **1** so volume stripping is only considered when a **single** worker is down, not during **multi-node** incidents (similar in intent to **Instance HA `THRESHOLD`**). When the limit blocks work, the controller does not delete PVCs in that reconcile (and does not advance pending-deletion resume in the same pass).
- **Phase 2**: Service-specific behaviour (e.g. only delete when the application operator sets `remediation.openshift.io/safe-to-delete`) and tighter alignment with “when SNR kicks in” are left for Phase 2.

---

## 5. User guide (quick reference)

### 5.1 Prerequisites

- **infra-operator** installed (with the PodRemediator controller and CRD).
- **Node Health Check (NHC)** and **Self Node Remediation (SNR)** installed and configured in the cluster (at least one NodeHealthCheck and one SelfNodeRemediationTemplate). Without them, the PodRemediator CR will stay Ready=False.

### 5.2 Install and enable

1. Ensure the CRD is applied:  
   `kubectl apply -f config/crd/bases/remediation.openstack.org_podremediators.yaml`  
   (or use the operator’s normal install/bundle).

2. Create a PodRemediator in the desired namespace (or in a dedicated namespace and set `spec.namespaces` to the namespaces where you run stateful workloads with local PVCs):

   ```bash
   kubectl apply -f config/samples/remediation_v1beta1_podremediator.yaml -n <namespace>
   ```

3. Check status:

   ```bash
   kubectl get podremediator -n <namespace>
   kubectl describe podremediator podremediator-sample -n <namespace>
   ```

   - If Ready is False and the message says NHC and SNR are required, install or configure NHC/SNR and ensure at least one NodeHealthCheck and one SelfNodeRemediationTemplate exist.
   - If Ready is True, the controller is monitoring and will delete local PVCs bound to nodes that report Ready≠True.

### 5.3 Disable PVC deletion

Set `spec.disabled: true` to opt out. The controller will still require NHC/SNR and report Ready, but will not delete any PVCs.

### 5.4 Multiple namespaces

Set `spec.namespaces` to the list of namespaces to watch, for example:

```yaml
spec:
  namespaces:
    - openstack
    - my-app
  # disabled defaults false
```

Only PVCs in those namespaces (and, when the CR is in one of them, that namespace) are considered for remediation.
