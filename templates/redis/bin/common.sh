# Environment variable common to all scripts
APISERVER=https://kubernetes.default.svc
SERVICEACCOUNT=/var/run/secrets/kubernetes.io/serviceaccount
NAMESPACE=$(cat ${SERVICEACCOUNT}/namespace)
TOKEN=$(cat ${SERVICEACCOUNT}/token)
CACERT=${SERVICEACCOUNT}/ca.crt

TIMEOUT=3

POD_NAME=$HOSTNAME
POD_FQDN=$HOSTNAME.$SVC_FQDN

# Extract pod IP from /etc/hosts
POD_IP=$(grep "$HOSTNAME" /etc/hosts | awk '{print $1}' | head -1)

# Detect complete Redis or Valkey binary set
if command -v redis-cli >/dev/null 2>&1 && \
   command -v redis-server >/dev/null 2>&1 && \
   command -v redis-sentinel >/dev/null 2>&1; then
    CLI_BIN="redis-cli"
    SERVER_BIN="redis-server"
    SENTINEL_BIN="redis-sentinel"
elif command -v valkey-cli >/dev/null 2>&1 && \
     command -v valkey-server >/dev/null 2>&1 && \
     command -v valkey-sentinel >/dev/null 2>&1; then
    CLI_BIN="valkey-cli"
    SERVER_BIN="valkey-server"
    SENTINEL_BIN="valkey-sentinel"
else
    echo "ERROR: Neither complete Redis nor Valkey binary set found in PATH"
    exit 1
fi

if test -d /var/lib/config-data/tls; then
    echo "INFO: TLS mode enabled - using TLS configs"
    REDIS_CLI_CMD="$CLI_BIN --tls"
    REDIS_CONFIG=/var/lib/config-data/generated/var/lib/redis/redis-tls.conf
    SENTINEL_CONFIG=/var/lib/config-data/generated/var/lib/redis/sentinel-tls.conf
else
    echo "INFO: TLS mode disabled - using plain configs"
    REDIS_CLI_CMD=$CLI_BIN
    REDIS_CONFIG=/var/lib/config-data/generated/var/lib/redis/redis.conf
    SENTINEL_CONFIG=/var/lib/config-data/generated/var/lib/redis/sentinel.conf
fi

echo "INFO: Using binary: $SERVER_BIN"
echo "INFO: Config path: $REDIS_CONFIG"

function log() {
    echo "$(date +%F_%H_%M_%S) $*"
}

function log_error() {
    echo "$(date +%F_%H_%M_%S) ERROR: $*"
}

function generate_configs() {
    local tmplist
    local ret=0

    # Create the target directory structure upfront
    mkdir -p /var/lib/config-data/generated/var/lib/redis || {
        log_error "Failed to create /var/lib/config-data/generated/var/lib/redis"
        return 1
    }

    # Change to source directory
    cd /var/lib/config-data/default || {
        log_error "Failed to change directory to /var/lib/config-data/default"
        return 1
    }

    # Create temporary file for file lists
    tmplist=$(mktemp) || {
        log_error "Failed to create temporary file"
        return 1
    }

    # Find all files to copy (except hidden files and templates)
    # Exclude Kubernetes ConfigMap internal directories (..data, ..YYYY_MM_DD_*)
    if ! find . \( -type f -o -type l \) ! -path './\.\.*' ! -name '.*' ! -name '*.in' -print0 > "$tmplist"; then
        log_error "Failed to find configuration files"
        rm -f "$tmplist"
        return 1
    fi

    # Copy each file with error checking
    while IFS= read -r -d '' file; do
        dest_dir="/var/lib/config-data/generated/$(dirname "$file")"
        if ! mkdir -p "$dest_dir"; then
            log_error "Failed to create directory $dest_dir"
            ret=1
            break
        fi
        if ! cp -rfL "$file" "/var/lib/config-data/generated/$file"; then
            log_error "Failed to copy $file"
            ret=1
            break
        fi
    done < "$tmplist"

    if [ $ret -ne 0 ]; then
        rm -f "$tmplist"
        return 1
    fi

    # Find template files
    # Exclude Kubernetes ConfigMap internal directories (..data, ..YYYY_MM_DD_*)
    if ! find -L . ! -path './\.\.*' -name '*.conf.in' -print0 > "$tmplist"; then
        log_error "Failed to find template files"
        rm -f "$tmplist"
        return 1
    fi

    # Process each template with error checking
    while IFS= read -r -d '' cfg; do
        log "Generating config file from template ${cfg}"
        output_file="/var/lib/config-data/generated/${cfg%.in}"
        if ! sed -e "s/{ POD_FQDN }/${POD_FQDN}/g" -e "s/{ POD_IP }/${POD_IP}/g" "${cfg}" > "$output_file"; then
            log_error "Failed to generate config from template ${cfg}"
            ret=1
            break
        fi
    done < "$tmplist"

    rm -f "$tmplist"
    return $ret
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
    # 200: OK, 404: pod not found, 422: label not found
    configure_pod_label $pod "$patch" "(200|404|422)"
}

# Wait for a peer sentinel to report a valid master for the cluster.
# Contacts each peer pod individually by FQDN (skipping self) to avoid
# the headless service DNS resolving to our own uninitialized sentinel.
# If a peer still reports US as master (stale info before
# down-after-milliseconds triggers failover), keeps retrying until
# failover completes and a different master is elected.
# Falls back to querying peer redis directly via the ROLE command
# when sentinel output cannot be parsed.
# Prints the master address on success (FQDN or IP).
function wait_for_master() {
    local retries=${SENTINEL_RETRIES:-10}
    local delay=${SENTINEL_RETRY_DELAY:-3}
    local pod_ordinal=${POD_NAME##*-}
    local pod_base=${POD_NAME%-*}
    local max_ordinal=$(( ${REPLICAS:-3} - 1 ))

    for i in $(seq 1 $retries); do
        local ordinal=0
        while [ $ordinal -le $max_ordinal ]; do
            if [ "$ordinal" != "$pod_ordinal" ]; then
                local peer="${pod_base}-${ordinal}.${SVC_FQDN}"
                local output
                output=$(timeout ${TIMEOUT} $REDIS_CLI_CMD --raw -h ${peer} -p 26379 sentinel master redis 2>/dev/null)
                if [ $? -eq 0 ] && [ -n "$output" ]; then
                    local master
                    master=$(echo "$output" | tr -d '\r' | awk '/^ip$/{getline; print; exit}')
                    if [ -n "$master" ] && [ "$master" != "$POD_IP" ] && ! echo "$master" | grep -q "^${POD_NAME}\."; then
                        echo "$master"
                        return 0
                    fi
                    log "Peer ${peer} sentinel reports master=${master} (stale, skipping)" >&2
                else
                    # Sentinel unreachable; try redis ROLE as fallback
                    local role
                    role=$(timeout ${TIMEOUT} $REDIS_CLI_CMD --raw -h ${peer} -p 6379 role 2>/dev/null | head -1 | tr -d '\r')
                    if [ "$role" = "master" ]; then
                        echo "$peer"
                        return 0
                    fi
                fi
            fi
            ordinal=$((ordinal + 1))
        done
        log "Attempt $i/$retries: no valid master found, retrying in ${delay}s..." >&2
        sleep $delay
    done
    return 1
}

# Check if any peer redis is alive (responds to PING on port 6379).
# Used as a safety net before bootstrapping to distinguish a fresh
# deployment (no peers) from a pod restart (peers alive).
function has_alive_peers() {
    local pod_ordinal=${POD_NAME##*-}
    local pod_base=${POD_NAME%-*}
    local max_ordinal=$(( ${REPLICAS:-3} - 1 ))
    local ordinal=0
    while [ $ordinal -le $max_ordinal ]; do
        if [ "$ordinal" != "$pod_ordinal" ]; then
            local peer="${pod_base}-${ordinal}.${SVC_FQDN}"
            if timeout ${TIMEOUT} $REDIS_CLI_CMD -h ${peer} -p 6379 ping 2>/dev/null | grep -q PONG; then
                return 0
            fi
        fi
        ordinal=$((ordinal + 1))
    done
    return 1
}

# Path to the operator-maintained cluster-state ConfigMap mount.
BOOTSTRAP_AUTH_FILE=/var/lib/redis-state/bootstrap-authorized

# Check if bootstrapping a new master is authorized by the operator.
# The operator writes "true" to the mounted cluster-state ConfigMap only
# while the whole StatefulSet is down (a fresh deployment or a full outage;
# Redis storage here is ephemeral, so a full outage is effectively a fresh
# start). While any pod is Ready an existing master may still be serving, so
# the operator writes "false" and unilateral bootstrap is refused. Reading a
# mounted file (rather than the Kubernetes API) keeps this decision available
# even when the API server is momentarily unreachable and needs no extra RBAC.
function is_bootstrap_authorized() {
    local auth=""
    if [ -r "$BOOTSTRAP_AUTH_FILE" ]; then
        auth=$(tr -d '[:space:]' < "$BOOTSTRAP_AUTH_FILE" 2>/dev/null)
    fi

    if [ "$auth" = "true" ]; then
        return 0
    fi
    log "Bootstrap not authorized by operator (state='${auth:-<missing>}')" >&2
    return 1
}

function set_pod_label() {
    local pod="$1"
    local label="$2"
    local patch="[{\"op\": \"add\", \"path\": \"/metadata/labels/${label}\", \"value\": \"true\"}]"
    # 200: OK
    configure_pod_label $pod "$patch" "200"
}
