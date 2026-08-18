#!/usr/bin/env bash
#
# Assemble a Full package (Runtime + MySQL 8.4 LTS) from the locked MySQL
# archive and verify it.
#
# Usage: assemble-full.sh <runtime-staging> <platform> <output-name> <output-dir>
# Platform: linux-amd64 | linux-arm64 | windows-x64

set -Eeuo pipefail

if [[ $# -ne 4 ]]; then
    echo "usage: assemble-full.sh <runtime-staging> <platform> <output-name> <output-dir>" >&2
    exit 2
fi

readonly runtime_staging="$1"
readonly platform="$2"
readonly name="$3"
readonly output_dir="$4"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly lock="${repo_root}/versions.lock.json"
readonly cache_dir="${ZENTAO_BUILD_CACHE:-${repo_root}/.cache/mysql}"
readonly tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

case "${platform}" in
    linux-amd64)   mysql_key="linux_amd64" ;;
    linux-arm64)   mysql_key="linux_arm64" ;;
    windows-x64)   mysql_key="windows_x64" ;;
    *) echo "unknown platform: ${platform}" >&2; exit 2 ;;
esac

if [[ ! -f "${runtime_staging}/manifest.json" ]]; then
    echo "runtime staging with manifest.json is required" >&2
    exit 1
fi

mysql_url="$(jq -r ".mysql.${mysql_key}.url" "${lock}")"
mysql_sha256="$(jq -r ".mysql.${mysql_key}.archive_sha256" "${lock}")"
if [[ -z "${mysql_url}" || -z "${mysql_sha256}" || "${mysql_sha256}" == "null" ]]; then
    echo "MySQL archive is not locked for ${platform}" >&2
    exit 1
fi

mkdir -p "${cache_dir}"
case "${mysql_url}" in
    *.zip) archive_ext="zip" ;;
    *)     archive_ext="tar.xz" ;;
esac
archive="${cache_dir}/mysql-$(jq -r '.mysql.version' "${lock}")-${platform}.${archive_ext}"
if [[ ! -f "${archive}" ]] || ! echo "${mysql_sha256}  ${archive}" | sha256sum --check --strict >/dev/null 2>&1; then
    curl --fail --location --retry 3 --output "${archive}" "${mysql_url}"
fi
echo "${mysql_sha256}  ${archive}" | sha256sum --check --strict

mysql_stage="${tmpdir}/mysql"
mkdir -p "${mysql_stage}"
case "${mysql_url}" in
    *.zip)
        unzip -q "${archive}" -d "${tmpdir}/mysql-unzip"
        mysql_root="$(find "${tmpdir}/mysql-unzip" -mindepth 1 -maxdepth 1 -type d | head -1)"
        ;;
    *.tar.xz|*.tar.gz)
        mkdir -p "${tmpdir}/mysql-tar"
        tar -xf "${archive}" -C "${tmpdir}/mysql-tar"
        mysql_root="$(find "${tmpdir}/mysql-tar" -mindepth 1 -maxdepth 1 -type d | head -1)"
        ;;
    *)
        echo "unsupported MySQL archive format: ${mysql_url}" >&2
        exit 1
        ;;
esac
cp -a "${mysql_root}/." "${mysql_stage}/"

full="${tmpdir}/full"
mkdir -p "${full}"
cp -a "${runtime_staging}/." "${full}/"
cp -a "${mysql_stage}" "${full}/mysql"
if [[ ! -x "${full}/mysql/bin/mysqld" && ! -x "${full}/mysql/bin/mysqld.exe" ]]; then
    echo "MySQL binary is missing from the Full package" >&2
    exit 1
fi

mysql_version="$(jq -r '.mysql.version' "${lock}")"
jq --arg version "${mysql_version}" --arg platform "${platform}" \
    '.components.mysql = {version: $version, platform: $platform}' \
    "${full}/manifest.json" > "${full}/manifest.json.tmp"
mv "${full}/manifest.json.tmp" "${full}/manifest.json"

"${repo_root}/scripts/package/assemble-runtime.sh" "${full}" "${name}" "${output_dir}"
"${repo_root}/scripts/package/verify-package.sh" "${output_dir}/${name}.tar.zst"

echo "full package: ${output_dir}/${name}.tar.zst"
