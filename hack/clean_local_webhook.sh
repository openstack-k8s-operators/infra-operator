#!/bin/bash
set -ex

oc delete validatingwebhookconfiguration/vopenstackclient.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mopenstackclient.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vmemcached.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mmemcached.kb.io --ignore-not-found
