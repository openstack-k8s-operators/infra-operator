# RHOSSTRAT-1214 Refinement Notes (2026-04-28)

## Session goal

Align scope, estimates, dependencies, and delivery plan for PodRemediator stateful PVC remediation for RHOSO 19.0 GA.

---

## Ticket context

- Feature ticket: `RHOSSTRAT-1214` (Implement PodRemediator for stateful PVC remediation)
- Implementation epic: `OSPRH-27279` (In Progress)
- Child stories:
  - `OSPRH-27280` - PodRemediator controller and CRD (In Progress)
  - `OSPRH-27281` - Runbook and POC workflow (In Progress)
  - `OSPRH-27283` - E2E test: node failure and PVC remediation (Backlog)
  - `OSPRH-27285` - Documentation draft (Backlog)
- Target release: `rhos-19.0.0 GA`

---

## Problem statement (for call intro)

Stateful workloads with local storage (for example Galera and RabbitMQ) do not recover automatically after ungraceful worker failures. Pods remain blocked by PVC/PV locality constraints unless remediation also handles local PVC lifecycle.

PodRemediator addresses this by integrating with Node Health Check (NHC) and Self Node Remediation (SNR): when a node is unhealthy, it identifies affected local PVCs and enables workload rescheduling on healthy workers.

---

## Current implementation status

### Completed / in progress evidence

- PodRemediator CRD/controller and RBAC are implemented in infra-operator (`OSPRH-27280`, in progress).
- POC runbook and automation exist (`OSPRH-27281`, in progress).
- E2E scenarios executed on lab (had-18):
  - RabbitMQ E2E successful
  - Galera E2E successful, including wsrep post-recovery checks
- Documentation set available:
  - `test-kit/podremediator/docs/PODREMEDIATOR_OPERATOR_BEHAVIOR.md`
  - `test-kit/podremediator/docs/PODREMEDIATOR_CUSTOMER_GUIDE.md`
  - `test-kit/podremediator/docs/PODREMEDIATOR_POC_RUNBOOK.md`
  - `test-kit/podremediator/docs/PODREMEDIATOR_POC_VERIFY_TESTS.md`
  - `docs/STATEFUL_PVC_REMEDIATION_DESIGN.md` (product design, repo root)
  - `test-kit/podremediator/docs/STATEFUL_PVC_REMEDIATION_TEST_PLAN.md`
  - `test-kit/podremediator/docs/GALERA_E2E_TEST_SCENARIOS.md`

### Important lab finding to highlight

- Anti-affinity alone is not sufficient with local storage:
  - If two ordinals have PV node affinity on the same worker, they remain co-located.
  - Rebalancing requires controlled PVC/pod recreation (lab-safe only) to reassign local PV topology.

---

## Scope proposal for RHOSSTRAT-1214 (refinement baseline)

### In scope for GA

1. PodRemediator controller lifecycle and Ready condition with NHC/SNR prerequisites.
2. Safe local-PVC remediation path for unhealthy nodes.
3. Reproducible runbook flow from zero to validated E2E.
4. E2E validation for RabbitMQ and Galera scenario shape.
5. Operator and customer documentation for behavior, constraints, and troubleshooting.

### Out of scope / follow-up

1. Changes inside mariadb-operator or rabbitmq-cluster-operator.
2. Advanced policy engine for multi-node correlated failure strategies.
3. Broad scheduler/topology policy redesign across operators.

---

## Open questions for refinement

1. What is the minimum acceptance bar for `OSPRH-27283` (single scenario vs matrix)?
2. Who owns QE execution and sign-off for node-failure E2E?
3. Should we explicitly gate feature activation on NHC+SNR availability, and how should status message it?
4. Which edge cases must be covered before GA:
   - multiple unhealthy workers
   - PVC already pending/unbound
   - stale remediation annotations
5. Is `rhos-18.0.17` still relevant in affected versions or should scope be 19.0-only?

---

## Proposed story-level breakdown for planning

### `OSPRH-27280` (controller/CRD)

- Final readiness conditions and reconciliation corner-case handling
- Eventing/logging hardening
- RBAC final review and least-privilege pass

### `OSPRH-27281` (runbook/POC)

- Stabilize runbook variables and prereq checks
- Ensure reproducibility from clean environment
- Add explicit rollback/recovery notes

### `OSPRH-27283` (E2E)

- RabbitMQ scenario with node kill and PVC remediation assertions
- Galera scenario with wsrep checkpoints and final assert
- Failure diagnostics capture in playbooks

### `OSPRH-27285` (docs)

- Finalize operator behavior contract
- Customer-facing constraints/troubleshooting
- Link all runbook/test artifacts

---

## Risks and mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Local PV topology causes repeated co-location | Misleading resilience expectations | Document explicitly and include rebalance procedure in lab docs |
| NHC/SNR timing variance | E2E flakiness | Timeout tuning + structured diagnostics in playbooks |
| Incomplete QE ownership | Delayed closure | Assign QE owner during refinement |
| Cross-team assumption mismatch | Rework near GA | Share behavior doc early with DB/message teams |

---

## Decisions log (fill during call)

- D1:
- D2:
- D3:

## Action items (fill during call)

- A1:
- A2:
- A3:

## Owners

- Dev owner:
- QE owner:
- Docs owner:
- PM/Release owner:

