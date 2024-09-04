#!/bin/bash

. /var/lib/operator-scripts/common.sh

generate_configs
sudo -E kolla_set_configs

# 1. check if a redis cluster is already running by contacting sentinel
output=$(timeout ${TIMEOUT} $REDIS_CLI_CMD -h ${SVC_FQDN} -p 26379 sentinel master redis)
if [ $? -eq 0 ]; then
    master=$(echo "$output" | awk '/^ip$/ {getline; print $0; exit}')
    # TODO skip if no master was found
    log "Connecting to the existing sentinel cluster (master: $master)"
    echo "sentinel monitor redis ${master} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec redis-sentinel $SENTINEL_CONFIG
fi

# 2. else let the pod's redis server bootstrap a new cluster and monitor it
# (assume we should be the first redis pod)
if is_bootstrap_pod $POD_NAME; then
    log "Bootstrapping a new sentinel cluster"
    echo "sentinel monitor redis ${POD_FQDN} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec redis-sentinel $SENTINEL_CONFIG
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a sentinel cluster"
exit 1
