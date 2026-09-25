# PodRemediator POC – Runbook (recreate from zero)

This document describes how to recreate the PodRemediator proof-of-concept environment from scratch. Use it after reinstalling had-18 or when setting up a new test environment.

**Scope:** had-18 (Jenkins agent / jump host), OpenShift cluster on controller-0 / masters, infra-operator with PodRemediator controller, NHC/SNR, and optional test pod with local PVC.

### Quick path: full runbook after had-18 reinstall

If you have just reinstalled had-18 and want to run the full POC, follow these steps in order (details in the sections below):

| # | Action | See |
|---|--------|-----|
| 0 | Confirm prerequisites (VPN, root@<jump-host>, zuul→controller-0, cluster up, kubeconfig on controller-0) | §1 |
| 1 | Access had-18 and verify cluster login from controller-0 (`oc get nodes`) | §2 |
| 2 | Deploy custom infra-operator image (run runbook with `custom_operator_image`; see [Ansible README](../README.md)) | §3 |
| 3 | Apply PodRemediator CRD (copy `config/podremediator_cluster_setup.yaml` to controller-0, `oc apply -f`) | §4 |
| 4 | Install NHC and SNR on controller-0 (clone ci-framework, run `cifmw_snr_nhc` playbook) | §5 |
| 5 | Create PodRemediator CR (`oc apply -f` sample in openstack-operators) | §6 |
| 6 | Optional: local PVC test — Ansible `playbooks/test-e2e-podremediator-pvc-remediation.yml -e e2e_stop_after_sts_deploy=true` (or shell script track in §7) | §7 |
| 7 | Run smoke test (verification script on controller-0, Option A from laptop) | §9.1 |

All commands are run from your **laptop** (with repo and VPN) unless the section says “on had-18” or “on controller-0”. After step 5, PodRemediator should show Ready=True; step 6 and 7 are optional validation.

---

## 1. Prerequisites

- **Network:** VPN and SSH access to had-18 (e.g. `<jump-host>` or IP).
- **Credentials:** root on had-18 (e.g. <root-password>); zuul on controller-0 with SSH key from had-18.
- **Cluster:** OpenShift cluster reachable from controller-0; zuul has `~/.kube/config` and `~/.kube/kubeadmin-password` on controller-0.
- **Repo:** infra-operator (e.g. `github.com/antonioromito/infra-operator`) and optionally ci-framework for NHC/SNR.

---

## 2. Access and cluster login

From your laptop (with VPN):

```bash
# SSH to had-18
ssh root@<jump-host>
# password: <your-root-password>

# From had-18, as zuul, SSH to controller-0 (where oc runs)
su - zuul -c "ssh <controller-host>"

# On controller-0, log in to the cluster
oc login -u kubeadmin -p $(cat ~/.kube/kubeadmin-password)
oc get nodes
```

Confirm you see the cluster nodes (e.g. master-0, master-1, master-2).

---

## 3. Deploy custom infra-operator image (custom build)

The POC uses an infra-operator image that includes the PodRemediator controller (built from your fork). Deploy it by running the **single runbook** with the variable `custom_operator_image` set to your image. The runbook (Step 2) scales OpenStack operator deployments to 0, patches the infra-operator deployment with your image, and waits for rollout.

**Full instructions:** [Ansible README](../README.md)

### 3.1 Build the image (one-time or when code changes)

- Push your branch to the fork and trigger the CI build (e.g. Quay or your pipeline), or build and push the image manually.
- Note the image reference (e.g. `quay.io/<your-org>/infra-operator@sha256:<digest>` or `...:latest`).

### 3.2 Run the runbook with your image

From **repo root**, install the Ansible collection once, then run the runbook with `custom_operator_image`:

**From laptop** (inventory with had-18 + controller0 via ProxyCommand):

```bash
ansible-galaxy collection install -r test-kit/podremediator/requirements.yml
ansible-playbook -i test-kit/podremediator/inventory/inventory.yml \
  test-kit/podremediator/playbooks/runbook-podremediator-poc.yml \
  -e "custom_operator_image=quay.io/YOUR_ORG/infra-operator@sha256:YOUR_DIGEST"
```

**From had-18** (recommended: repo in `/home/zuul/infra-operator`, run as zuul):

```bash
cd /home/zuul/infra-operator
ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
  test-kit/podremediator/playbooks/runbook-podremediator-poc.yml \
  -e "custom_operator_image=quay.io/YOUR_ORG/infra-operator@sha256:YOUR_DIGEST"
```

The runbook performs steps 1–7 (cluster check, deploy image, CRD, NHC/SNR, PodRemediator CR, optional local PVC test, smoke test). To skip the local PVC and smoke tests: add `-e "run_local_pvc_test=false" -e "run_smoke_test=false"`.

### 3.3 Verify

From controller-0 (or via had-18 → controller-0):

```bash
oc get deployment -n openstack-operators | grep infra-operator
oc get pods -n openstack-operators -l app.kubernetes.io/name=infra-operator
oc get deployment infra-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}'
```

You should see your custom image and the pod running. If the pod is CrashLoopBackOff or Error, check logs (often missing CRD or RBAC; the runbook applies CRD in Step 3).

---

## 4. Apply PodRemediator CRD

The operator image may not ship the PodRemediator CRD. Apply the CRD from the infra-operator repo. RBAC (nodes, PVCs, podremediators, NHC/SNR) is provided by the operator's generated role (kubebuilder markers in the controller; when infra-operator is installed via openstack-operator, that role is included in the bundle).

**From a host that has the repo and can reach the cluster** (e.g. copy the file to controller-0, or run from your machine if you have `oc` and kubeconfig):

```bash
# Option A: From controller-0 (after copying the file)
oc apply -f /path/to/podremediator_cluster_setup.yaml
```

**Option B: From your laptop via had-18 → controller-0**

```bash
# From repo root (replace with your path)
cd /path/to/infra-operator
scp config/podremediator_cluster_setup.yaml root@<jump-host>:/tmp/
# SSH to had-18, then:
chmod 644 /tmp/podremediator_cluster_setup.yaml
su - zuul -c "scp /tmp/podremediator_cluster_setup.yaml <controller-host>:/tmp/"
su - zuul -c "ssh <controller-host> 'oc login -u kubeadmin -p \$(cat ~/.kube/kubeadmin-password) && oc apply -f /tmp/podremediator_cluster_setup.yaml'"
```

**Expected output:** CRD created/unchanged.

**Verify:**

```bash
oc get crd podremediators.remediation.openstack.org
```

**Manifest location in repo:** `config/podremediator_cluster_setup.yaml` (CRD only).

After applying, the infra-operator pod (deployed in step 3) may need a moment to pick up the new CRD and RBAC; verify with `oc -n openstack-operators get pods -l app.kubernetes.io/name=infra-operator` and check logs for errors.

---

## 5. Install NHC and SNR (Node Health Check, Self Node Remediation)

PodRemediator requires at least one NodeHealthCheck and one SelfNodeRemediationTemplate. Use the ci-framework role `cifmw_snr_nhc`.

**On controller-0 (as zuul):**

```bash
cd /tmp
rm -rf ci-framework
git clone --depth 1 https://github.com/openstack-k8s-operators/ci-framework.git
```

Create a minimal playbook (e.g. `/tmp/ci-framework/playbook-nhc-snr.yml`):

```yaml
- name: Configure SNR and NHC
  hosts: localhost
  connection: local
  roles:
    - role: cifmw_snr_nhc
      cifmw_snr_nhc_kubeconfig: "/home/zuul/.kube/config"
      cifmw_snr_nhc_kubeadmin_password_file: "/home/zuul/.kube/kubeadmin-password"
      cifmw_snr_nhc_namespace: openshift-workload-availability
```

Run:

```bash
cd /tmp/ci-framework
ansible-playbook -i localhost, playbook-nhc-snr.yml
```

**Verify:**

```bash
oc get nodehealthchecks -A
oc get selfnoderemediationtemplates -A
# Expect: nodehealthcheck-sample, self-node-remediation-automatic-strategy-template in openshift-workload-availability
```

See also: [STATEFUL_PVC_REMEDIATION_DESIGN.md § 5.2](../../../docs/STATEFUL_PVC_REMEDIATION_DESIGN.md) and [ci-framework cifmw_snr_nhc](https://github.com/openstack-k8s-operators/ci-framework/blob/main/roles/cifmw_snr_nhc/README.md).

---

## 6. Create PodRemediator CR

Create the PodRemediator custom resource in the namespace where you want it to run (e.g. `openstack-operators`). Empty `spec.namespaces` means “watch only this namespace”.

```bash
# From infra-operator repo (copy sample to controller-0 or apply via stdin)
oc apply -f config/samples/remediation_v1beta1_podremediator.yaml -n openstack-operators
```

**Verify:**

```bash
oc get podremediator -n openstack-operators
oc describe podremediator podremediator-sample -n openstack-operators
```

- **Before NHC/SNR:** Status may be False with message that NHC and SNR are required.
- **After NHC/SNR and RBAC for NHC/SNR:** Status should be True, message e.g. “No unhealthy nodes; monitoring”.

If Ready stays False, ensure the operator is deployed with the generated role that includes PodRemediator RBAC (nodes, PVCs, podremediators, NHC/SNR). The role is generated from kubebuilder markers in the controller (`make generate manifests` → `config/rbac/role.yaml`). When installing via openstack-operator, use a bundle that includes this role.

---

## 7. Optional: Local PVC test

StatefulSet track uses bundled manifests in `tasks/manifests/local_pvc_statefulset.yml` (via `test-e2e-podremediator-pvc-remediation.yml` deploy phase, `deploy-local-pvc-statefulset.yml`, or runbook Step 6). HostPath track uses `tasks/manifests/hostpath/`. See [manifests README](../tasks/manifests/README.md).

### 7.A StatefulSet + StorageClass (recommended — same as runbook Step 6 / main E2E)

Deploys `test-podremediator` / `test-podremediator-0` / `data-test-podremediator-0` using `kubernetes.core` (no RabbitMQ/Galera). Uses manifest `statefulset-with-pvc.yaml` (edit `storageClassName` if needed).

**From had-18** (repo at `/home/zuul/infra-operator`, as `zuul`):

```bash
cd /home/zuul/infra-operator
ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
  test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml \
  -e e2e_stop_after_sts_deploy=true
```

This is the **same automation** as runbook Step 6 (`tasks/deploy-local-pvc-statefulset.yml`). While the node is healthy, PodRemediator should **not** delete the PVC (M3.3).

**Verify:**

```bash
oc get statefulset,pvc,pod -n openstack-operators | grep -E 'test-podremediator|data-test-podremediator'
```

### 7.B Manual hostPath PV + Pod (optional shell-only lab)

For a fixed **hostPath** PV + standalone Pod (not the StatefulSet/E2E default), copy manifests + script to controller-0 and run `apply-test-local-pvc.sh`:

```bash
scp test-kit/podremediator/tasks/manifests/hostpath/*.yaml test-kit/podremediator/scripts/apply-test-local-pvc.sh root@<jump-host>:/tmp/podremediator_local_pvc/
# On had-18 → controller-0 as in previous revisions; on controller-0:
./apply-test-local-pvc.sh
```

**Verify (Pod track):**

```bash
oc get pv test-local-pv-podremediator
oc get pvc test-local-pvc-podremediator -n openstack-operators
oc get pod test-pod-local-pvc-podremediator -n openstack-operators -o wide
```

---

## 8. Verification checklist

| Check | Command |
|-------|---------|
| CRD | `oc get crd podremediators.remediation.openstack.org` |
| Infra-operator pod | `oc -n openstack-operators get pods -l app.kubernetes.io/name=infra-operator` |
| NHC | `oc get nodehealthchecks -A` |
| SNR template | `oc get selfnoderemediationtemplates -A` |
| PodRemediator CR | `oc get podremediator -n openstack-operators` (expect Ready=True after NHC/SNR) |
| Test resources (optional) | STS track: `grep test-podremediator`; Pod track: `grep -E 'test-local|test-pod-local'` |

---

## 9. Tests from had-18

You can run these to validate the runbook or after a had-18 reinstall.

### 9.1 Smoke test: verification script

Runs the §8 checks from controller-0 and reports PASS/FAIL.

**Option A – From laptop (one-liner via stdin, no quoting issues):**

```bash
# Copy script to had-18 and to controller-0 first (once)
scp path/to/infra-operator/test-kit/hack/verify-podremediator-poc.sh root@<jump-host>:/tmp/
ssh root@<jump-host> "su - zuul -c 'scp /tmp/verify-podremediator-poc.sh <controller-host>:/tmp/'"

# Run login + script on controller-0 (password read on controller-0)
ssh root@<jump-host> "su - zuul -c 'ssh -o StrictHostKeyChecking=no <controller-host> bash -s'" < <(printf '%s\n' 'oc login -u kubeadmin -p $(cat ~/.kube/kubeadmin-password) --insecure-skip-tls-verify && chmod +x /tmp/verify-podremediator-poc.sh && /tmp/verify-podremediator-poc.sh')
```

**Option B – From had-18 (copy then run on controller-0):**

```bash
# On had-18, after copying script to controller-0 as above:
su - zuul -c 'ssh <controller-host> "oc login -u kubeadmin -p $(cat ~/.kube/kubeadmin-password) --insecure-skip-tls-verify && /tmp/verify-podremediator-poc.sh"'
```

(If Option B fails with “cat: .../kubeadmin-password: No such file or directory”, the `$(cat ...)` is being expanded on had-18; use Option A from the laptop instead.)

**Option C – On controller-0:** after `oc login`, run `./verify-podremediator-poc.sh` (or `/tmp/verify-podremediator-poc.sh` if you copied it there).

Exit code 0 = all required checks passed.

**Script in repo:** `test-kit/hack/verify-podremediator-poc.sh`.

### 9.2 Test: Deploy custom build (Option B)

1. From laptop, copy playbook + template to had-18 (runbook §3.2b step 1).
2. Run the playbook with your custom image (step 2).
3. Verify: from controller-0 (or via had-18 → controller-0), `oc get pods -n openstack-operators -l app.kubernetes.io/name=infra-operator` and confirm the pod image with `oc get deployment infra-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}'`.

### 9.3 Test: Full runbook from zero

With a clean cluster (or after cleanup): run runbook steps 2 → 3 → 4 → 5 → 6 in order, then run the verification script (§9.1) or the checklist (§8). Optionally run step 7 (local PVC test).

### 9.4 Test: Local PVC (e2e)

1. **StatefulSet track (default):** on had-18, `ansible-playbook ... test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml -e e2e_stop_after_sts_deploy=true` (same as §7.A). Verify STS/pod/PVC names `test-podremediator` / `data-test-podremediator-0`.
2. **Pod + hostPath track:** copy `tasks/manifests/hostpath/*.yaml` + `scripts/apply-test-local-pvc.sh` to controller-0 (§7.B), run the script, verify `test-local-*` / `test-pod-local-*` resources.
3. PodRemediator should stay Ready=True and not delete the PVC while the node is healthy (M3.3).

### 9.5 Test: Re-create PodRemediator CR

1. Delete the CR: `oc delete podremediator podremediator-sample -n openstack-operators`.
2. Re-apply the sample: `oc apply -f config/samples/remediation_v1beta1_podremediator.yaml -n openstack-operators`.
3. Verify: `oc get podremediator -n openstack-operators` → Ready=True after a short delay (NHC/SNR already present).

---

## 10. Cleanup (optional)

**StatefulSet test workload (§7.A):**

```bash
oc delete statefulset test-podremediator -n openstack-operators
oc delete pvc data-test-podremediator-0 -n openstack-operators
oc delete svc test-podremediator -n openstack-operators
```

**Pod + hostPath track (§7.B only):**

```bash
oc delete pod test-pod-local-pvc-podremediator -n openstack-operators
oc delete pvc test-local-pvc-podremediator -n openstack-operators
oc delete pv test-local-pv-podremediator
```

**PodRemediator CR (keeps CRD/RBAC/NHC/SNR):**

```bash
oc delete podremediator podremediator-sample -n openstack-operators
```

---

## 11. References

- **Design and user guide:** [STATEFUL_PVC_REMEDIATION_DESIGN.md](../../../docs/STATEFUL_PVC_REMEDIATION_DESIGN.md) (§ 5 for install, NHC/SNR role, disable, namespaces).
- **Test plan:** [STATEFUL_PVC_REMEDIATION_TEST_PLAN.md](./STATEFUL_PVC_REMEDIATION_TEST_PLAN.md).
- **Ansible playbook (full runbook):** [Ansible README](../README.md) – un solo playbook che esegue tutti gli step usando moduli nativi (kubernetes.core, ansible.builtin).
- **Cluster setup (CRD + RBAC):** [PODREMEDIATOR_CLUSTER_SETUP.md](PODREMEDIATOR_CLUSTER_SETUP.md) (if present locally).
- **NHC/SNR role:** [ci-framework cifmw_snr_nhc](https://github.com/openstack-k8s-operators/ci-framework/blob/main/roles/cifmw_snr_nhc/README.md).
- **Local PVC test:** [manifests README](../tasks/manifests/README.md).

---

## 12. Had-18 reinstall notes

After reinstalling had-18:

- **SSH:** Update `known_hosts` on the Jenkins server (or wherever you SSH from) for had-18’s new host key; restore `authorized_keys` on had-18 if you use key-based auth. See `jenkins-agent-had18-online.md` for the Jenkins server procedure (comando unico).
- **Java / agent:** If Jenkins runs jobs that need Java or other tools on had-18, reinstall and configure them (e.g. Java 21 as in your setup).
- **Access to controller-0:** Ensure from had-18 the user that runs the playbooks (e.g. zuul) can still SSH to `<controller-host>` and that `~/.kube/config` and `~/.kube/kubeadmin-password` on controller-0 are valid. If the cluster was recreated, copy the new kubeadmin password to controller-0.

**Then:** Follow the **Quick path** at the top of this document (steps 0–7) to run the full runbook. This runbook assumes the OpenShift cluster (masters) and controller-0 are unchanged; only had-18 is reinstalled. If the cluster is recreated too, repeat from step 1 (cluster login) through step 7 of the quick path.
