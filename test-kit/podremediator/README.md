# PodRemediator POC – Ansible lab kit

## Layout

| Directory | Contents |
|-----------|----------|
| `playbooks/` | **Ansible entry-point YAML** (runbook, E2E, lab helpers). See `playbooks/README.md`. |
| `tasks/` | Included task fragments (Galera wsrep, victim selection, monitors). |
| `inventory/` | Ansible inventories (laptop vs had-18). |
| `scripts/` | Laptop/cluster helpers: `run-e2e.sh`, `run-runbook.sh`, `verify-podremediator-poc.sh`, `apply-test-local-pvc.sh`, `galera_e2e_dr_report.py`. |
| `docs/` | Runbooks, test plan, Galera scenarios, operator guides. |
| `tasks/manifests/` | Bundled StatefulSet lab data (`local_pvc_statefulset.yml`) and `hostpath/` YAML for optional shell lab; see `tasks/manifests/README.md`. |
| `.tmp/` | Gitignored; Galera transcripts land here when using `run-e2e.sh`. |

**Main POC playbook:** `playbooks/runbook-podremediator-poc.yml` — runs all steps from [PODREMEDIATOR_POC_RUNBOOK.md](docs/PODREMEDIATOR_POC_RUNBOOK.md): optional SSH bootstrap (from laptop) + steps 1–7. Uses native modules where possible (`kubernetes.core.k8s`, `k8s_info`, `ansible.builtin.*`).

## Requirements

- Ansible 2.14+
- **kubernetes.core** collection (`ansible-galaxy collection install -r requirements.yml`)
- SSH from laptop to the **jump host** (e.g. had-18) and from there to the **controller** (e.g. controller-0 as zuul). For the E2E script from the laptop use `SSHPASS=<password>` if using password auth (see below).
- Run from **repo root** (for file paths)

## Inventory

`inventory/inventory.yml` defines:

- **had18**: had-18 host (ansible_user=root)
- **controller0**: controller-0 (ansible_user=zuul) reachable via **ProxyCommand** through had-18

Ensure `ansible_host` and ProxyCommand match your environment (had-18 hostname/FQDN).

If your SSH keys are not yet authorized for **zuul@controller-0**, run the playbook with **bootstrap** (adds your public key to controller-0 via had-18):

```bash
ansible-playbook -i ... runbook-podremediator-poc.yml -e "bootstrap_controller0_ssh=true" -e "custom_operator_image=..."
```

Or use an already-authorized key: `-e "ansible_ssh_private_key_file=/path/to/key_for_zuul"`.

### Running from had-18

When running the playbook **from had-18** (instead of the laptop), use the inventory without jump:

- **inventory/inventory-from-jumphost.yml**: only `controller0`, direct connection (no ProxyCommand).

Requirements on had-18: infra-operator repo present (clone or rsync), Ansible, `kubernetes.core` collection. Run the playbook as user `zuul` (from root: `su - zuul`), repo in `/home/zuul/infra-operator`.

See **Usage from had-18** below.

## Usage

From repo root:

```bash
# Install collection (once)
ansible-galaxy collection install -r test-kit/podremediator/requirements.yml

# Full run (required: custom_operator_image)
ansible-playbook -i test-kit/podremediator/inventory/inventory.yml \
  test-kit/podremediator/playbooks/runbook-podremediator-poc.yml \
  -e "custom_operator_image=quay.io/<your-org>/infra-operator@sha256:<digest>"

# Without local PVC test and smoke test
ansible-playbook -i test-kit/podremediator/inventory/inventory.yml \
  test-kit/podremediator/playbooks/runbook-podremediator-poc.yml \
  -e "custom_operator_image=..." -e "run_local_pvc_test=false" -e "run_smoke_test=false"
```

### Usage from had-18

1. From the laptop, copy the repo to `<HAD18_REPO_PATH>` on the jump host (default: `/home/zuul/infra-operator`):
   ```bash
   ssh root@<jump-host> 'mkdir -p /home/zuul/infra-operator && chown zuul:zuul /home/zuul/infra-operator'
   rsync -avz --exclude .git /path/to/infra-operator/ root@<jump-host>:/home/zuul/infra-operator/
   ssh root@<jump-host> 'chown -R zuul:zuul /home/zuul/infra-operator'
   ```

2. On had-18, **as user zuul** (from root: `su - zuul`), from repo root:
   ```bash
   cd /home/zuul/infra-operator
   ansible-galaxy collection install -r test-kit/podremediator/requirements.yml
   ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
     test-kit/podremediator/playbooks/runbook-podremediator-poc.yml \
     -e "custom_operator_image=quay.io/...@sha256:YOUR_DIGEST"
   ```
   Or from root in one go:  
   `su - zuul -c "cd /home/zuul/infra-operator && ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml test-kit/podremediator/playbooks/runbook-podremediator-poc.yml -e 'custom_operator_image=...'"`

Verify first from the jump host (as zuul): `ssh zuul@<controller-host> echo ok`.

### E2E test – PodRemediator and real node failure (virsh destroy)

After running the runbook (with Step 6), you can run the E2E test that simulates **real node death** via **virsh destroy** of the VM on had-18, verifies that NHC/SNR detect the node down and PodRemediator deletes the PVC, then restores the VM with **virsh start**.

```bash
# From laptop (sync repo then run E2E on the jump host). Use SSHPASS for password auth:
HAD18=<jump-host> SSHPASS=<password> SYNC=1 ./test-kit/podremediator/scripts/run-e2e.sh   # first time (sync + run)
HAD18=<jump-host> SSHPASS=<password> ./test-kit/podremediator/scripts/run-e2e.sh          # repo already on jump host
HAD18=<jump-host> SSHPASS=<password> E2E_GALERA=1 ./test-kit/podremediator/scripts/run-e2e.sh   # Galera exploratory E2E

# Or on had-18 (as zuul), from repo root:
ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
  test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml -b
```

The test: finds the test pod’s node (e.g. worker-0), on had-18 runs **virsh destroy** of the libvirt domain with the same name (e.g. `worker-0`), waits for the control plane to mark the node NotReady and for NHC/SNR to detect it, verifies the PVC was deleted, then **virsh start** to restore the VM. If the libvirt domain name differs from the node: `-e "e2e_virsh_domain=overcloud-worker-0"`. To skip restarting the VM at the end: `-e "e2e_skip_restore=true"`.

## Main variables

| Variable | Description | Default |
|----------|-------------|---------|
| `custom_operator_image` | Custom infra-operator image (required for Step 2) | - |
| `bootstrap_controller0_ssh` | From laptop: add SSH key to zuul@controller-0 before steps | false |
| `run_local_pvc_test` | Run step 6 (local PVC test pod) | true |
| `run_smoke_test` | Run step 7 (verification checks) | true |
| `operator_namespace` | OpenShift namespace for operators | openstack-operators |

## Steps performed

- **(Optional) Bootstrap** – Only when inventory includes `jump_hosts` and `bootstrap_controller0_ssh=true`: loads local public key and adds it to zuul@controller-0 via had-18.
1. **Step 1** – Verify cluster (controller0): `oc login`, `oc get nodes`, assert.
2. **Step 2** – Deploy custom image (controller0): `k8s_info` + `k8s` to scale OSP deployments to 0, `k8s` to patch infra-operator image.
3. **Step 3** – Apply CRD and RBAC (controller0): `lookup('file')` + `from_yaml_all`, then `k8s` per document. Uses `config/podremediator_cluster_setup.yaml` from the repo (CRD only). RBAC is provided by the operator's generated role (kubebuilder markers in the controller).
4. **Step 4** – NHC/SNR (on had-18): copy kubeconfig from controller0, `git` ci-framework, `copy` playbook, `shell` for `ansible-playbook` cifmw_snr_nhc.
5. **Step 5** – PodRemediator CR (controller0): `k8s` with sample from file.
6. **Step 6** – (Optional) Local PVC test: Ansible tasks (worker/first node, `k8s` for PV/PVC/Pod from file with `NODE_NAME` substitution).
7. **Step 7** – (Optional) Smoke test: Ansible tasks (CRD, infra-operator pod, NHC, SNR, PodRemediator, Ready).

## Modules used

- **ansible.builtin**: `set_fact`, `shell`, `command`, `copy`, `assert`, `git`
- **kubernetes.core**: `k8s` (apply/patch CRD, RBAC, Deployment, PodRemediator), `k8s_info` (check existing deployments)

Where no native module exists (e.g. `oc login`, NHC/SNR playbook run), `shell` or `command` is used. Step 6 StatefulSet deploy uses Ansible (`tasks/deploy-local-pvc-statefulset.yml`); Step 7 smoke checks replaced the standalone `verify-podremediator-poc.sh` flow. Optional hostPath Pod lab still uses `scripts/apply-test-local-pvc.sh`.

## Playbooks (high level)

| Path under `playbooks/` | Uso |
|-------------------------|-----|
| `runbook-podremediator-poc.yml` | Runbook principale (step 1–7). |
| `test-e2e-podremediator-pvc-remediation.yml` | Playbook E2E: deploy StatefulSet di lab opzionale (default), poi virsh destroy/start. Solo deploy lab: `-e e2e_stop_after_sts_deploy=true`. |
| `test-e2e-podremediator-pvc-remediation-rabbitmq.yml` | E2E wrapper for RabbitMQ in `openstack`. |
| `test-e2e-podremediator-pvc-remediation-galera.yml` | E2E wrapper for Galera (exploratory; override pod/PVC names to match cluster). |
| `playbook-nhc-snr.yml` | Usato dallo Step 4: copiato in ci-framework ed eseguito per NHC/SNR. |
| `verify-e2e-ready.yml` | Opzionale: verifica prerequisiti prima dell'E2E. |
Altri file utili: `inventory/inventory-from-jumphost.yml` / `inventory/inventory.yml`, `requirements.yml`, `scripts/run-e2e.sh`.

Tutti i path nei comandi sono relativi a `test-kit/podremediator/` (da repo root). Lo script `test-kit/podremediator/scripts/verify-podremediator-poc.sh` è opzionale (verifica da controller-0). Da laptop, `run-e2e.sh` salva i transcript Galera (timeline ed eventuale DR report) in `test-kit/podremediator/.tmp/e2e-controller-logs/` (directory gitignored).
