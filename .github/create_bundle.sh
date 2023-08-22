#!/bin/bash
set -e

echo "Creating infra operator bundle"
cd ..
echo "${GITHUB_SHA}"
echo "${BASE_IMAGE}"

RELEASE_VERSION=$(grep "^VERSION" Makefile | awk -F'?= ' '{ print $2 }')
echo "Release Version: $RELEASE_VERSION"

#FIXME(dprince) just a test to see if this works in CI
skopeo inspect docker://registry.redhat.io/rhel9/redis-6:latest

echo "Creating bundle image..."
USE_IMAGE_DIGESTS=true VERSION=$RELEASE_VERSION IMG=${REGISTRY}/${BASE_IMAGE}:${GITHUB_SHA} make bundle
