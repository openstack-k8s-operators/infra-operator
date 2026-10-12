#!/bin/bash

. /var/lib/operator-scripts/common.sh

if ! generate_configs; then
    echo "ERROR: Configuration generation failed"
    exit 1
fi

# 1. check if a redis cluster is already running by contacting peer sentinels
master=$(wait_for_master)
if [ $? -eq 0 ]; then
    log "Connecting to the existing sentinel cluster (master: $master)"
    echo "sentinel monitor redis ${master} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec $SENTINEL_BIN $SENTINEL_CONFIG
fi

# 2. no live master was found; only the bootstrap pod (pod-0) may initialize a
# brand-new sentinel quorum, and only when the same safety checks used by the
# redis container pass. This keeps sentinel from monitoring pod-0 as master
# based on anything other than an authorized fresh bootstrap.
if is_bootstrap_pod $POD_NAME; then
    if has_alive_peers; then
        log_error "Peers are alive but no master found. Refusing to bootstrap sentinel to avoid split-brain."
        exit 1
    fi

    if ! is_bootstrap_authorized; then
        log_error "Bootstrap not authorized by operator; sentinel will not initialize a new master."
        log_error "This is expected during a partial outage or partition; waiting for a master."
        exit 1
    fi

    log "Bootstrapping a new sentinel cluster (authorized by operator)"
    echo "sentinel monitor redis ${POD_FQDN} 6379 ${SENTINEL_QUORUM}" >> $SENTINEL_CONFIG
    exec $SENTINEL_BIN $SENTINEL_CONFIG
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a sentinel cluster"
exit 1
