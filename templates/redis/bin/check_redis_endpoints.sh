#!/bin/bash

. /var/lib/operator-scripts/common.sh

# When the master changed because of a failover, redis notifies this
# script with the following arguments:
# <master-name> <current-role> <state> <old-master> <from-port> <new-master> <to-port>

log "$0 called with arguments: $*"

CLUSTER_NAME=$1
POD_ROLE=$2
STATE=$3
OLD_MASTER=$4
NEW_MASTER=$6

OLD_POD=$(echo $OLD_MASTER | cut -d. -f1)
NEW_POD=$(echo $NEW_MASTER | cut -d. -f1)

if [ "$POD_ROLE" = "leader" ]; then
    log "Preparing the endpoint for the failover ${OLD_POD} -> ${NEW_POD}"

    log "Removing ${OLD_POD} from the Redis service's endpoint"
    remove_pod_label $OLD_POD redis~1master
    if [ $? != 0 ]; then
        log_error "Could not remove service endpoint. Aborting"
        exit 1
    fi

    log "Setting ${NEW_POD} as the new endpoint for the Redis service"
    set_pod_label $NEW_POD redis~1master
    if [ $? != 0 ]; then
        log_error "Could not add service endpoint. Aborting"
        exit 1
    fi
else
    log "No action taken since we were an observer during the failover"
fi
