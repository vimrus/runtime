#!/usr/bin/env bash
#
# Build the Linux Runtime for the native host architecture.
# Usage: build-linux.sh [amd64|arm64]

set -Eeuo pipefail

if [[ $# -gt 1 ]] || { [[ $# -eq 1 ]] && [[ "$1" != "amd64" && "$1" != "arm64" ]]; }; then
    echo "usage: build-linux.sh [amd64|arm64]" >&2
    exit 2
fi

case "$(uname -m)" in
    x86_64|amd64) native_arch=amd64 ;;
    aarch64|arm64) native_arch=arm64 ;;
    *)
        echo "unsupported Linux host architecture: $(uname -m)" >&2
        exit 2
        ;;
esac

# Keep the explicit argument for CI compatibility; local invocations default
# to the host architecture. Formal builds must use a native runner.
export RUNTIME_ARCH="${1:-${native_arch}}"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

build_runtime_image

mapfile -t args < <(docker_build_args)
mkdir -p "${repo_root}/dist/runtime-linux-${RUNTIME_ARCH}"
platform_args=()
if [[ -n "${ZENTAO_BUILDX_PLATFORM:-}" ]]; then
    platform_args=(--platform "${ZENTAO_BUILDX_PLATFORM}")
fi
docker buildx build \
    --file "${repo_root}/packaging/runtime/Dockerfile" \
    --target artifact \
    --output "type=local,dest=${repo_root}/dist/runtime-linux-${RUNTIME_ARCH}" \
    "${platform_args[@]}" \
    "${args[@]}" \
    "${repo_root}"

"${repo_root}/scripts/build/verify-artifact.sh" "${repo_root}/dist/runtime-linux-${RUNTIME_ARCH}"
echo "Linux ${RUNTIME_ARCH} Runtime built"
