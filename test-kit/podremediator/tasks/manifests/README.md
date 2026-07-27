# Kubernetes manifests for PodRemediator labs

| File / dir | Purpose |
|------------|---------|
| **`local_pvc_statefulset.yml`** | Ansible vars: Service + StatefulSet `test-podremediator` (loaded by `tasks/deploy-local-pvc-statefulset.yml` and inlined in `test-e2e-podremediator-pvc-remediation.yml`; runbook Step 6). |
| **`hostpath/`** | Raw YAML for optional hostPath PV + Pod lab (`scripts/apply-test-local-pvc.sh`). |

Edit **`local_pvc_statefulset.yml`** to change `storageClassName` if your cluster differs from `lvms-local-storage`.
