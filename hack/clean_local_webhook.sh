#!/bin/bash
set -ex

oc delete validatingwebhookconfiguration/vopenstackclient.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mopenstackclient.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vmemcached.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mmemcached.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vdnsmasq.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mdnsmasq.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vredis.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mredis.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vnetconfig.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mnetconfig.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vreservation.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mreservation.kb.io --ignore-not-found
oc delete validatingwebhookconfiguration/vipset.kb.io --ignore-not-found
oc delete mutatingwebhookconfiguration/mipset.kb.io --ignore-not-found
