#!/bin/bash

. /var/lib/operator-scripts/common.sh

generate_configs
sudo -E kolla_set_configs

# 1. check if a redis cluster is already running by contacting peer sentinels
master=$(wait_for_master)
if [ $? -eq 0 ]; then
    log "Connecting to the existing Redis cluster (master: ${master})"
    exec redis-server $REDIS_CONFIG --protected-mode no --replicaof "$master" 6379
fi

# 2. else bootstrap a new cluster if no peers are alive (fresh deployment)
if is_bootstrap_pod $POD_NAME; then
    if has_alive_peers; then
        log_error "Peers are alive but no master found. Refusing to bootstrap to avoid split-brain."
        exit 1
    fi
    log "Bootstrapping a new Redis cluster from ${POD_NAME}"
    set_pod_label $POD_NAME redis~1master
    exec redis-server $REDIS_CONFIG --protected-mode no
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a redis cluster"
exit 1
