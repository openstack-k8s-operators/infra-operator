#!/usr/bin/env bash
# Sync repo to the jump host and run the PodRemediator POC runbook **on that host**.
# From your laptop (repo root), with SSH access to the jump host:
#   CUSTOM_OPERATOR_IMAGE=quay.io/.../infra-operator@sha256:... HAD18=<host> \
#     ./test-kit/podremediator/scripts/run-runbook.sh
#
# For password-based SSH, set SSHPASS=<password> (requires sshpass installed):
#   SSHPASS=<password> SYNC=1 CUSTOM_OPERATOR_IMAGE=quay.io/... HAD18=<host> \
#     ./test-kit/podremediator/scripts/run-runbook.sh
#
# Options:
#   HAD18=host                Jump host FQDN or IP (required)
#   SYNC=1                    Sync repo to jump host before running (default: 1 for this script)
#   SSHPASS=pwd               Password auth for SSH/rsync
#   CUSTOM_OPERATOR_IMAGE=... Image for runbook Step 2 (required)
#   HAD18_REPO_PATH=path      Repo path on jump host (default: /home/zuul/infra-operator)
#   OC_CMD=path               Path to oc on controller if not in PATH (e.g. /path/to/oc)
#   OC_API_SERVER=url         API server URL (e.g. https://api.ocp.example.com:6443)
#   RUN_LOCAL_PVC_TEST=false  Skip step 6 (default: true)
#   RUN_SMOKE_TEST=false      Skip step 7 (default: true)
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

SYNC="${SYNC:-1}"
HAD18_REPO_PATH="${HAD18_REPO_PATH:-/home/zuul/infra-operator}"
HAD18_USER="${HAD18_USER:-root}"
JUMPHOST_INVENTORY="${JUMPHOST_INVENTORY:-test-kit/podremediator/inventory/inventory-from-jumphost.yml}"

if [[ -z "${CUSTOM_OPERATOR_IMAGE:-}" ]]; then
  echo "Error: CUSTOM_OPERATOR_IMAGE is not set."
  echo "  export CUSTOM_OPERATOR_IMAGE=quay.io/<org>/infra-operator@sha256:<digest>"
  exit 1
fi

EXTRA_VARS="-e custom_operator_image=${CUSTOM_OPERATOR_IMAGE}"
[[ -n "${OC_CMD:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e oc_cmd=${OC_CMD}"
[[ -n "${OC_API_SERVER:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e oc_api_server=${OC_API_SERVER}"
[[ -n "${RUN_LOCAL_PVC_TEST:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e run_local_pvc_test=${RUN_LOCAL_PVC_TEST}"
[[ -n "${RUN_SMOKE_TEST:-}" ]] && EXTRA_VARS="${EXTRA_VARS} -e run_smoke_test=${RUN_SMOKE_TEST}"

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
  rsync -avz --exclude .git -e "$RSYNC_SSH" "$REPO_ROOT/" "${HAD18_USER}@${HAD18}:${HAD18_REPO_PATH}/"
  $SSH_CMD "${HAD18_USER}@${HAD18}" "chown -R zuul:zuul ${HAD18_REPO_PATH}" || true
  echo "Sync done."
fi

echo "Running runbook on ${HAD18} (as zuul, -b for virsh, -vvv)..."
$SSH_CMD "${HAD18_USER}@${HAD18}" "su - zuul -c 'cd ${HAD18_REPO_PATH} && ansible-galaxy collection install -r test-kit/podremediator/playbooks/requirements.yml && ansible-playbook -i ${JUMPHOST_INVENTORY} test-kit/podremediator/playbooks/runbook-podremediator-poc.yml $EXTRA_VARS -b -vvv'"
