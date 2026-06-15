#!/bin/bash

. /var/lib/operator-scripts/common.sh

generate_configs
sudo -E kolla_set_configs

# 1. check if a redis cluster is already running by contacting peer sentinels
master=$(wait_for_master)
if [ $? -eq 0 ]; then
    log "Connecting to the existing sentinel cluster (master: $master)"
    echo "sentinel monitor redis ${master} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec redis-sentinel $SENTINEL_CONFIG
fi

# 2. else let the pod's redis server bootstrap a new cluster and monitor it
# (only if no peers are alive, meaning this is a fresh deployment)
if is_bootstrap_pod $POD_NAME; then
    if has_alive_peers; then
        log_error "Peers are alive but no master found. Refusing to bootstrap sentinel to avoid split-brain."
        exit 1
    fi
    log "Bootstrapping a new sentinel cluster"
    echo "sentinel monitor redis ${POD_FQDN} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec redis-sentinel $SENTINEL_CONFIG
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a sentinel cluster"
exit 1
