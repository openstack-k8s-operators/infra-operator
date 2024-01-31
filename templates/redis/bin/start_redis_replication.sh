#!/bin/bash

. /var/lib/operator-scripts/common.sh

generate_configs
sudo -E kolla_set_configs

# 1. check if a redis cluster is already running by contacting sentinel
output=$(timeout ${TIMEOUT} redis-cli -h ${SVC_FQDN} -p 26379 sentinel master redis)
if [ $? -eq 0 ]; then
    master=$(echo "$output" | awk '/^ip$/ {getline; print $0; exit}')
    # TODO skip if no master was found
    log "Connecting to the existing Redis cluster (master: ${master})"
    exec redis-server /var/lib/redis/redis.conf --protected-mode no --replicaof "$master" 6379
fi

# 2. else bootstrap a new cluster (assume we should be the first redis pod)
if is_bootstrap_pod $POD_NAME; then
    log "Bootstrapping a new Redis cluster from ${POD_NAME}"
    set_pod_label $POD_NAME redis~1master
    exec redis-server /var/lib/redis/redis.conf --protected-mode no
fi

# 3. else this is an error, exit and let the pod restart and try again
echo "Could not connect to a redis cluster"
exit 1
