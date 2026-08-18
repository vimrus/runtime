#!/usr/bin/env bash

set -Eeuo pipefail

export POC_ARCH=amd64
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

build_poc_image

mapfile -t args < <(docker_build_args)
mkdir -p "${REPO_ROOT}/dist/poc-linux-amd64"
docker build \
    --file "${REPO_ROOT}/packaging/poc/Dockerfile" \
    --target artifact \
    --output "type=local,dest=${REPO_ROOT}/dist/poc-linux-amd64" \
    "${args[@]}" \
    "${REPO_ROOT}"

"${REPO_ROOT}/scripts/poc/verify-artifact.sh" "${REPO_ROOT}/dist/poc-linux-amd64"
