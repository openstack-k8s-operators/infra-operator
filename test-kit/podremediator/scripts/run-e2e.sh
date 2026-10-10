#!/usr/bin/env bash
# Run the E2E PodRemediator playbook **on the jump host** (virsh must run there).
# From your laptop (repo root), with SSH access to the jump host:
#   HAD18=<host> ./test-kit/podremediator/scripts/run-e2e.sh
#
# For password-based SSH, set SSHPASS=<password> (requires sshpass installed):
#   SSHPASS=<password> HAD18=<host> ./test-kit/podremediator/scripts/run-e2e.sh
#   SSHPASS=<password> SYNC=1 HAD18=<host> ./test-kit/podremediator/scripts/run-e2e.sh
#   SYNC=1 E2E_LOCAL_STS=1 SSHPASS=<password> HAD18=<host> ./test-kit/podremediator/scripts/run-e2e.sh
#     (rsync then main E2E playbook with -e e2e_stop_after_sts_deploy=true — bundled STS only, no virsh)
# (Install sshpass if needed, e.g. dnf install sshpass)
#
# Options:
#   HAD18=host               Jump host FQDN or IP (required)
#   SYNC=1                   Sync repo to jump host before running (default: 0)
#   SSHPASS=pwd              Use password auth for SSH/rsync
#   HAD18_REPO_PATH=path     Repo path on jump host (default: /home/zuul/infra-operator)
#   HAD18_USER=user          SSH user for jump host (default: root)
#   E2E_OC_CMD=path          Path to oc on controller if not in PATH (required on many labs);
#                              e.g. E2E_OC_CMD=/path/to/oc
#   E2E_OC_API_SERVER=url    API server URL to avoid oc login hang (e.g. https://api.cluster:6443)
#   E2E_VIRSH_DOMAIN=name    Force libvirt domain (default: prefix + node name, e.g. cifmw-ocp-worker-2)
#   E2E_VIRSH_DOMAIN_PREFIX=prefix Prefix for node name (default: cifmw-ocp-)
#   E2E_PVC_TIMEOUT=sec      Max seconds to wait for PVC deletion (default 720). Use 1200 if NHC/SNR are slow.
#   E2E_RABBITMQ=1           Run OpenStack RabbitMQ E2E (rabbitmq-server-0 in namespace openstack); implies
#                              -e e2e_auto_patch_podremediator=true unless you disable in E2E_EXTRA_ANSIBLE_ARGS.
#   E2E_GALERA=1             Run Galera E2E (openstack-galera-0 by default; see docs/GALERA_E2E_TEST_SCENARIOS.md);
#                              implies e2e_auto_patch_podremediator=true.
#                              Each wsrep snapshot dumps filtered oc get events (Galera/remediation-related; openstack + operators);
#                              E2E_EXTRA_ANSIBLE_ARGS='-e e2e_k8s_events_max_lines=200' or unfiltered: -e e2e_k8s_events_relevant_only=false
#                              or disable events: -e e2e_k8s_events_report=false.
#                              A human-readable timeline (stage + wsrep + events) is appended on the controller to
#                              /tmp/galera-e2e-k8s-events.log by default; override with -e e2e_galera_events_log_path=... or disable with -e e2e_galera_events_log_path="".
#                              After every Galera run this script copies that file to the laptop under
#                              $REPO_ROOT/test-kit/podremediator/.tmp/e2e-controller-logs/ (gitignored). Override remote path/host:
#                                E2E_GALERA_EVENTS_REMOTE=/tmp/other.log E2E_CONTROLLER_HOST=<controller-host>
#                              Disable auto-fetch: E2E_SKIP_FETCH_GALERA_LOG=1
#                              Auto-generated DR markdown: E2E_GALERA_DR_REPORT_REMOTE=/tmp/galera-e2e-dr-report.md
#                              (fetched to test-kit/podremediator/.tmp/e2e-controller-logs/galera-e2e-dr-report-<timestamp>.md).
#                              E2E_PLAYBOOK wins over E2E_LOCAL_STS / E2E_GALERA / E2E_RABBITMQ.
#   E2E_LOCAL_STS=1          Same as main E2E playbook but stop after bundled lab StatefulSet deploy (no virsh).
#   E2E_PLAYBOOK=path        Override playbook path relative to repo root (wins over E2E_LOCAL_STS / E2E_GALERA / E2E_RABBITMQ).
#                              Example: test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-rabbitmq.yml
#   E2E_EXTRA_ANSIBLE_ARGS   Extra args appended to ansible-playbook (quoted string), e.g. '-e e2e_rabbitmq_preflight=false'
#   E2E_ANSIBLE_VERBOSE      ansible-playbook verbosity (default -v). Use -vvv for full task/module dumps.
#   E2E_CONTROLLER_HOST      Controller hostname for Galera log fetch (default: CHANGEME_CONTROLLER_HOST)
set -e

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "$0")/../../.." && pwd)}"
TESTKIT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Auto-source local.env if present (copy from local.env.example and fill in your values)
if [[ -f "${TESTKIT_DIR}/local.env" ]]; then
  set -a; source "${TESTKIT_DIR}/local.env"; set +a
fi

HAD18="${HAD18:-}"
if [[ -z "$HAD18" ]]; then
  echo "Error: HAD18 is not set. Set it to the FQDN or IP of the libvirt/jump host."
  echo "  export HAD18=<host>"
  exit 1
fi

SYNC="${SYNC:-0}"
HAD18_REPO_PATH="${HAD18_REPO_PATH:-/home/zuul/infra-operator}"
HAD18_USER="${HAD18_USER:-root}"
JUMPHOST_INVENTORY="${JUMPHOST_INVENTORY:-test-kit/podremediator/inventory/inventory-from-jumphost.yml}"
VERB="${E2E_ANSIBLE_VERBOSE:--v}"
EXTRA_VARS=""
[[ -n "${E2E_OC_CMD:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e oc_cmd=${E2E_OC_CMD}"
[[ -n "${E2E_OC_API_SERVER:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e oc_api_server=${E2E_OC_API_SERVER}"
[[ -n "${E2E_VIRSH_DOMAIN:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e e2e_virsh_domain=${E2E_VIRSH_DOMAIN}"
[[ -n "${E2E_VIRSH_DOMAIN_PREFIX:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e e2e_virsh_domain_prefix=${E2E_VIRSH_DOMAIN_PREFIX}"
[[ -n "${E2E_PVC_TIMEOUT:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e e2e_pvc_deleted_timeout_seconds=${E2E_PVC_TIMEOUT}"

# Playbook selection: E2E_PLAYBOOK overrides all; else E2E_LOCAL_STS; else E2E_GALERA / E2E_RABBITMQ; else default virsh E2E.
E2E_PLAYBOOK_REL="${E2E_PLAYBOOK:-}"
if [[ -z "${E2E_PLAYBOOK_REL}" && "${E2E_LOCAL_STS:-0}" == "1" ]]; then
  E2E_PLAYBOOK_REL="test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml"
  EXTRA_VARS="${EXTRA_VARS} -e e2e_stop_after_sts_deploy=true"
fi
if [[ -z "${E2E_PLAYBOOK_REL}" && "${E2E_GALERA:-0}" == "1" ]]; then
  E2E_PLAYBOOK_REL="test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-galera.yml"
  EXTRA_VARS="${EXTRA_VARS} -e e2e_auto_patch_podremediator=true"
fi
if [[ -z "${E2E_PLAYBOOK_REL}" && "${E2E_RABBITMQ:-0}" == "1" ]]; then
  E2E_PLAYBOOK_REL="test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation-rabbitmq.yml"
  EXTRA_VARS="${EXTRA_VARS} -e e2e_auto_patch_podremediator=true"
fi
if [[ -z "${E2E_PLAYBOOK_REL}" ]]; then
  E2E_PLAYBOOK_REL="test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml"
fi

# Use sshpass for SSH/rsync when SSHPASS is set
SSH_CMD="ssh -o StrictHostKeyChecking=no"
RSYNC_SSH="ssh -o StrictHostKeyChecking=no"
if [[ -n "${SSHPASS:-}" ]]; then
  if ! command -v sshpass &>/dev/null; then
    echo "SSHPASS is set but sshpass not found. Install it (e.g. dnf install sshpass) or use SSH keys."
    exit 1
  fi
  SSH_CMD="sshpass -e ssh -o StrictHostKeyChecking=no"
  RSYNC_SSH="sshpass -e ssh -o StrictHostKeyChecking=no"
fi

cd "$REPO_ROOT"

if [[ "$SYNC" == "1" ]]; then
  echo "Syncing repo to ${HAD18_USER}@${HAD18}:${HAD18_REPO_PATH} ..."
  $SSH_CMD "${HAD18_USER}@${HAD18}" "mkdir -p ${HAD18_REPO_PATH} && chown zuul:zuul ${HAD18_REPO_PATH}" || true
  rsync -avz --exclude .git --exclude .tmp -e "$RSYNC_SSH" "$REPO_ROOT/" "${HAD18_USER}@${HAD18}:${HAD18_REPO_PATH}/"
  $SSH_CMD "${HAD18_USER}@${HAD18}" "chown -R zuul:zuul ${HAD18_REPO_PATH}" || true
  echo "Sync done."
fi

echo "Running E2E playbook on ${HAD18}: $E2E_PLAYBOOK_REL (as zuul, -b for virsh, $VERB)..."
set +e
# shellcheck disable=SC2086
$SSH_CMD "${HAD18_USER}@${HAD18}" "su - zuul -c 'cd ${HAD18_REPO_PATH} && ansible-playbook -i ${JUMPHOST_INVENTORY} $E2E_PLAYBOOK_REL $EXTRA_VARS ${E2E_EXTRA_ANSIBLE_ARGS:-} -b $VERB'"
playbook_rc=$?
set -e

# Galera timeline on controller: pull controller -> jump host (zuul scp) -> laptop (rsync), unless disabled.
if [[ "${E2E_SKIP_FETCH_GALERA_LOG:-0}" != "1" ]] && { [[ "${E2E_GALERA:-0}" == "1" ]] || [[ "$E2E_PLAYBOOK_REL" == *galera* ]]; }; then
  LOCAL_LOG_DIR="${REPO_ROOT}/test-kit/podremediator/.tmp/e2e-controller-logs"
  mkdir -p "$LOCAL_LOG_DIR"
  TS="$(date +%Y%m%d-%H%M%S)"
  CONTROLLER_HOST="${E2E_CONTROLLER_HOST:-CHANGEME_CONTROLLER_HOST}"
  REMOTE_LOG="${E2E_GALERA_EVENTS_REMOTE:-/tmp/galera-e2e-k8s-events.log}"
  STAGING="galera-e2e-k8s-events-${TS}.log"
  echo "Fetching Galera timeline: zuul@${CONTROLLER_HOST}:${REMOTE_LOG} -> ${LOCAL_LOG_DIR}/${STAGING} ..."
  if $SSH_CMD "${HAD18_USER}@${HAD18}" "su - zuul -c 'scp -o StrictHostKeyChecking=no zuul@${CONTROLLER_HOST}:${REMOTE_LOG} /home/zuul/${STAGING}'"; then
    rsync -avz -e "$RSYNC_SSH" "${HAD18_USER}@${HAD18}:/home/zuul/${STAGING}" "$LOCAL_LOG_DIR"/
    $SSH_CMD "${HAD18_USER}@${HAD18}" "rm -f /home/zuul/${STAGING}" || true
    echo "Local copy: ${LOCAL_LOG_DIR}/${STAGING}"
  else
    echo "Warning: could not scp Galera timeline from ${CONTROLLER_HOST} (missing file, or zuul@${HAD18} has no SSH to controller). ansible rc was ${playbook_rc}."
  fi
  REMOTE_DR="${E2E_GALERA_DR_REPORT_REMOTE:-/tmp/galera-e2e-dr-report.md}"
  STAGING_DR="galera-e2e-dr-report-${TS}.md"
  echo "Fetching Galera DR report: zuul@${CONTROLLER_HOST}:${REMOTE_DR} -> ${LOCAL_LOG_DIR}/${STAGING_DR} ..."
  if $SSH_CMD "${HAD18_USER}@${HAD18}" "su - zuul -c 'scp -o StrictHostKeyChecking=no zuul@${CONTROLLER_HOST}:${REMOTE_DR} /home/zuul/${STAGING_DR}'"; then
    rsync -avz -e "$RSYNC_SSH" "${HAD18_USER}@${HAD18}:/home/zuul/${STAGING_DR}" "$LOCAL_LOG_DIR"/
    $SSH_CMD "${HAD18_USER}@${HAD18}" "rm -f /home/zuul/${STAGING_DR}" || true
    echo "Local DR report: ${LOCAL_LOG_DIR}/${STAGING_DR}"
  else
    echo "Warning: could not scp Galera DR report from ${CONTROLLER_HOST}:${REMOTE_DR} (run with timeline log enabled; or file missing)."
  fi
fi

exit "$playbook_rc"
