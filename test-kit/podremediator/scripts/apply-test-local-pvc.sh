#!/usr/bin/env bash
# Optional lab: hostPath PV + standalone Pod (§7.B). For StatefulSet + SC (same as main E2E / runbook Step 6), use:
#   ansible-playbook -i test-kit/podremediator/inventory/inventory-from-jumphost.yml \
#     test-kit/podremediator/playbooks/test-e2e-podremediator-pvc-remediation.yml -e e2e_stop_after_sts_deploy=true
# Deploy test PV, PVC and Pod (local PVC) in openstack-operators.
# Manifests: same directory as this script when copied to controller, or ../tasks/manifests/hostpath from repo.
# Uses a worker node by default (same placement as MariaDB/RabbitMQ in production).
# Requires oc and cluster access (run from any cwd).
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_FIXTURES="${SCRIPT_DIR}/../tasks/manifests/hostpath"
if [[ -f "${SCRIPT_DIR}/pv-local.yaml" ]]; then
  FIXTURES_DIR="$SCRIPT_DIR"
elif [[ -f "${REPO_FIXTURES}/pv-local.yaml" ]]; then
  FIXTURES_DIR="$REPO_FIXTURES"
else
  echo "Cannot find pv-local.yaml (expected next to this script or under tasks/manifests/hostpath/)." >&2
  exit 1
fi
cd "$FIXTURES_DIR"
# Prefer worker nodes (where MariaDB, RabbitMQ and stateful workloads run)
if [[ -z "$NODE_NAME" ]]; then
  NODE_NAME=$(oc get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
fi
if [[ -z "$NODE_NAME" ]]; then
  NODE_NAME=$(oc get nodes -o jsonpath='{.items[0].metadata.name}')
fi
echo "Using node: $NODE_NAME"
echo "Creating PV..."
sed "s/NODE_NAME/${NODE_NAME}/g" pv-local.yaml | oc apply -f -
echo "Creating PVC in openstack-operators..."
oc apply -f pvc-local.yaml -n openstack-operators
echo "Creating Pod on node $NODE_NAME..."
sed "s/NODE_NAME/${NODE_NAME}/g" pod-with-local-pvc.yaml | oc apply -f - -n openstack-operators
echo "Done. Check: oc get pv,pvc,pod -n openstack-operators | grep -E 'test-local|test-pod-local'"
