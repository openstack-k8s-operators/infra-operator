# Environment variable common to all scripts
APISERVER=https://kubernetes.default.svc
SERVICEACCOUNT=/var/run/secrets/kubernetes.io/serviceaccount
NAMESPACE=$(cat ${SERVICEACCOUNT}/namespace)
TOKEN=$(cat ${SERVICEACCOUNT}/token)
CACERT=${SERVICEACCOUNT}/ca.crt

TIMEOUT=3

POD_NAME=$HOSTNAME
POD_FQDN=$HOSTNAME.$SVC_FQDN

if test -d /var/lib/config-data/tls; then
    REDIS_CLI_CMD="valkey-cli --tls"
    REDIS_CONFIG=/var/lib/valkey/valkey-tls.conf
    SENTINEL_CONFIG=/var/lib/valkey/sentinel-tls.conf
else
    REDIS_CLI_CMD=valkey-cli
    REDIS_CONFIG=/var/lib/valkey/valkey.conf
    SENTINEL_CONFIG=/var/lib/valkey/sentinel.conf
fi

function log() {
    echo "$(date +%F_%H_%M_%S) $*"
}

function log_error() {
    echo "$(date +%F_%H_%M_%S) ERROR: $*"
}

function generate_configs() {
    # Copying config files except template files
    tar -C /var/lib/config-data --exclude '..*' --exclude '*.in' -h -c default | tar -C /var/lib/config-data/generated -x --strip=1
    # Generating config files from templates
    cd /var/lib/config-data/default
    for cfg in $(find -L * -name '*.conf.in'); do
        log "Generating config file from template $PWD/${cfg}"
        sed -e "s/{ POD_FQDN }/${POD_FQDN}/" "${cfg}" > "/var/lib/config-data/generated/${cfg%.in}"
    done
}

function is_bootstrap_pod() {
    echo "$1" | grep -qe '-0$'
}

function extract() {
    local var="$1"
    local output="$2"
    # parse curl vars as well as kube api error fields
    echo "$output" | awk -F'[:,]' "/\"?${var}\"?:/ {print \$2; exit}"
}

function configure_pod_label() {
    local pod="$1"
    local patch="$2"
    local success="$3"
    local curlvars="\nexitcode:%{exitcode}\nerrormsg:%{errormsg}\nhttpcode:%{response_code}\n"

    response=$(curl -s -w "${curlvars}" --cacert ${CACERT} --header "Content-Type:application/json-patch+json" --header "Authorization: Bearer ${TOKEN}" --request PATCH --data "$patch" ${APISERVER}/api/v1/namespaces/${NAMESPACE}/pods/${pod})

    exitcode=$(extract exitcode "$response")
    if [ $exitcode -ne 0 ]; then
        errormsg=$(extract errormsg "$response")
        log_error "Error when running curl: ${errormsg} (${exitcode})"
        return 1
    fi

    httpcode=$(extract httpcode "$response")
    if echo "${httpcode}" | grep -v -E "^${success}$"; then
        message=$(extract message "$response")
        log_error "Error when calling API server: ${message} (${httpcode})"
        return 1
    fi
}

function remove_pod_label() {
    local pod="$1"
    local label="$2"
    local patch="[{\"op\": \"remove\", \"path\": \"/metadata/labels/${label}\"}]"
    # 200: OK, 422: not found
    configure_pod_label $pod "$patch" "(200|422)"
}

function set_pod_label() {
    local pod="$1"
    local label="$2"
    local patch="[{\"op\": \"add\", \"path\": \"/metadata/labels/${label}\", \"value\": \"true\"}]"
    # 200: OK
    configure_pod_label $pod "$patch" "200"
}
