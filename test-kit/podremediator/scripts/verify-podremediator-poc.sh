#!/usr/bin/env bash
# Run on controller-0 (e.g. as zuul) after oc login, or pipe via: ssh controller-0 'bash -s' < verify-podremediator-poc.sh
# Requires: oc and cluster access (oc login already done, or set KUBECONFIG).
# Exits 0 if all checks pass, 1 otherwise.
set -e
FAIL=0
NS="openstack-operators"

check() {
  local name="$1"
  local cmd="$2"
  if eval "$cmd" >/dev/null 2>&1; then
    echo "PASS  $name"
  else
    echo "FAIL  $name"
    FAIL=1
  fi
}

# If no kubeconfig context, try login (for use when script is run directly on controller-0)
if ! oc get ns "$NS" >/dev/null 2>&1; then
  if [[ -f ~/.kube/kubeadmin-password ]]; then
    oc login -u kubeadmin -p "$(cat ~/.kube/kubeadmin-password)" --insecure-skip-tls-verify >/dev/null 2>&1 || true
  fi
fi

echo "PodRemediator POC verification (run on controller-0 or via had-18 -> controller-0)"
echo "---"

check "CRD podremediators.remediation.openstack.org"        "oc get crd podremediators.remediation.openstack.org"
check "Infra-operator pod Running"                         "oc -n $NS get pods -l app.kubernetes.io/name=infra-operator --no-headers | grep -q Running"
check "NodeHealthCheck exists"                             "oc get nodehealthchecks -A --no-headers | grep -q ."
check "SelfNodeRemediationTemplate exists"                 "oc get selfnoderemediationtemplates -A --no-headers | grep -q ."
check "PodRemediator CR exists"                            "oc get podremediator -n $NS --no-headers | grep -q ."

# Optional: PodRemediator Ready=True (may be False if NHC/SNR not ready yet)
READY=$(oc get podremediator -n $NS -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
if [[ "$READY" == "True" ]]; then
  echo "PASS  PodRemediator Ready=True"
else
  echo "INFO  PodRemediator Ready=$READY (expected True after NHC/SNR; run again after a moment)"
fi

echo "---"
if [[ $FAIL -eq 0 ]]; then
  echo "All required checks passed."
  exit 0
else
  echo "One or more checks failed."
  exit 1
fi
