#!/bin/bash

. /var/lib/operator-scripts/common.sh

if ! generate_configs; then
    echo "ERROR: Configuration generation failed"
    exit 1
fi

# 1. check if a redis cluster is already running by contacting peer sentinels
master=$(wait_for_master)
if [ $? -eq 0 ]; then
    log "Connecting to the existing Redis cluster (master: ${master})"
    exec $SERVER_BIN $REDIS_CONFIG --protected-mode no --replicaof "$master" 6379
fi

# 2. no live master was found; only the bootstrap pod (pod-0) may consider
# starting a brand-new cluster, and only when it is safe to do so. Redis data
# is ephemeral (emptyDir), so a restarting pod never has authoritative role
# information on disk - master identity comes from Sentinel or the operator,
# never from local files.
if is_bootstrap_pod $POD_NAME; then
    # Refuse if any peer redis is alive: an existing master may still be
    # serving and self-promoting here would create a split-brain.
    if has_alive_peers; then
        log_error "Peers are alive but no master found. Refusing to bootstrap to avoid split-brain."
        exit 1
    fi

    # Require the operator's authorization. The operator only authorizes
    # bootstrap when the whole StatefulSet is down (fresh deploy / full outage),
    # which fences against bootstrapping during a network partition.
    if ! is_bootstrap_authorized; then
        log_error "Bootstrap not authorized by operator; refusing to start a new master."
        log_error "This is expected during a partial outage or partition; waiting for a master."
        exit 1
    fi

    log "Bootstrapping a new Redis cluster from ${POD_NAME} (authorized by operator)"
    set_pod_label $POD_NAME redis~1master
    exec $SERVER_BIN $REDIS_CONFIG --protected-mode no
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a redis cluster"
exit 1
