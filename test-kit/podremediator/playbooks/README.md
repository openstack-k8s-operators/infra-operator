# Ansible playbooks (entry points)

Run these from the **infra-operator repo root** (paths below assume that cwd).

Shared task fragments live in **`../tasks/`**. Bundled Kubernetes object lists for labs live in **`../tasks/manifests/`** (e.g. `local_pvc_statefulset.yml`).

| Playbook | Role |
|----------|------|
| `runbook-podremediator-poc.yml` | Full POC: optional SSH bootstrap, deploy operator image, CRD/RBAC, NHC/SNR, PodRemediator CR, optional local PVC test, smoke checks. |
| `playbook-nhc-snr.yml` | Medik8s NHC/SNR only; copied into ci-framework during the runbook. |
| `playbook-galera-rebalance-workers-lab.yml` | Lab: rebalance Galera STS members across workers (dry-run unless `galera_rebalance_execute=true`). |
| `test-e2e-podremediator-pvc-remediation.yml` | Core E2E: optional bundled lab StatefulSet deploy (`e2e_deploy_bundled_test_statefulset`, default true), virsh destroy/start, PVC remediation. Workload-only: `-e e2e_stop_after_sts_deploy=true`. |
| `test-e2e-podremediator-pvc-remediation-rabbitmq.yml` | E2E wrapper for OpenStack RabbitMQ workload. |
| `test-e2e-podremediator-pvc-remediation-galera.yml` | E2E wrapper for Galera (exploratory). |
| `test-e2e-podremediator-pvc-remediation-sequential.yml` | Chains bundled lab STS + virsh E2E, then RabbitMQ E2E (bundled deploy skipped on second import). |
| `verify-e2e-ready.yml` | Preconditions before running E2E. |
| `e2e-galera-wsrep-*.yml` | Standalone Galera wsrep monitoring / assert helpers (optional side flows). |

Inventory: **`../inventory/inventory.yml`** (laptop) or **`../inventory/inventory-from-jumphost.yml`** (on had-18).
