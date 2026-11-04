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
readonly artifact_dir="${repo_root}/dist/runtime-linux-${RUNTIME_ARCH}"
case "${artifact_dir}" in
    "${repo_root}/dist/runtime-linux-"*) ;;
    *) echo "refusing to write outside dist/: ${artifact_dir}" >&2; exit 1 ;;
esac

# Export into a pristine staging directory first. `--output type=local,dest=`
# merges into its destination and never removes stale files, so verifying the
# freshly exported files keeps the "Runtime artifact contains no application
# code" guarantee without touching what the operator installed.
readonly staging_dir="$(mktemp -d "${TMPDIR:-/tmp}/zentao-runtime-${RUNTIME_ARCH}.XXXXXX")"
cleanup_staging() { rm -rf "${staging_dir}"; }
trap cleanup_staging EXIT

platform_args=()
if [[ -n "${ZENTAO_BUILDX_PLATFORM:-}" ]]; then
    platform_args=(--platform "${ZENTAO_BUILDX_PLATFORM}")
fi
docker buildx build \
    --file "${repo_root}/packaging/runtime/Dockerfile" \
    --target artifact \
    --output "type=local,dest=${staging_dir}" \
    "${platform_args[@]}" \
    "${args[@]}" \
    "${repo_root}"

"${repo_root}/scripts/build/verify-artifact.sh" "${staging_dir}"

# Replace only the Runtime-owned files. Everything else below the artifact
# directory (config/runtime.toml, installed applications, data, logs, run,
# sessions, observability) is operator data and must survive a rebuild.
mkdir -p "${artifact_dir}"
for name in bin php lib; do
    [[ -d "${staging_dir}/${name}" ]] || continue
    rm -rf "${artifact_dir:?}/${name}"
    cp -a "${staging_dir}/${name}" "${artifact_dir}/${name}"
done
mkdir -p "${artifact_dir}/config"
for name in php.ini dm_svc.conf runtime.example.toml conf.d; do
    [[ -e "${staging_dir}/config/${name}" ]] || continue
    if [[ "${name}" == "conf.d" ]]; then
        # conf.d is a drop-in directory: refresh the packaged files but keep
        # operator additions such as a local debug ini.
        mkdir -p "${artifact_dir}/config/conf.d"
        cp -a "${staging_dir}/config/conf.d/." "${artifact_dir}/config/conf.d/"
        continue
    fi
    rm -rf "${artifact_dir:?}/config/${name}"
    cp -a "${staging_dir}/config/${name}" "${artifact_dir}/config/${name}"
done

for app_root in app apps; do
    if [[ -d "${artifact_dir}/${app_root}" ]] && find "${artifact_dir}/${app_root}" -type f -name '*.php' -print -quit 2>/dev/null | grep -q .; then
        echo "note: ${artifact_dir}/${app_root} holds an installed application; preserved as installation data" >&2
    fi
done
echo "Linux ${RUNTIME_ARCH} Runtime built"
