#!/usr/bin/env bash
#
# Build the Linux Runtime for the native runner architecture.
# Usage: build-linux.sh <amd64|arm64>

set -Eeuo pipefail

if [[ $# -ne 1 ]] || [[ "$1" != "amd64" && "$1" != "arm64" ]]; then
    echo "usage: build-linux.sh <amd64|arm64>" >&2
    exit 2
fi

export POC_ARCH="$1"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/poc/common.sh"

build_poc_image

mapfile -t args < <(docker_build_args)
mkdir -p "${repo_root}/dist/poc-linux-${POC_ARCH}"
platform_args=()
if [[ -n "${ZENTAO_BUILDX_PLATFORM:-}" ]]; then
    platform_args=(--platform "${ZENTAO_BUILDX_PLATFORM}")
fi
docker buildx build \
    --file "${repo_root}/packaging/poc/Dockerfile" \
    --target artifact \
    --output "type=local,dest=${repo_root}/dist/poc-linux-${POC_ARCH}" \
    "${platform_args[@]}" \
    "${args[@]}" \
    "${repo_root}"

"${repo_root}/scripts/poc/verify-artifact.sh" "${repo_root}/dist/poc-linux-${POC_ARCH}"
echo "Linux ${POC_ARCH} Runtime built"
