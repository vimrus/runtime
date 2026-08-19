#!/usr/bin/env bash
#
# Stage an externally produced, platform-independent ZenTao application
# package into the Runtime application layout:
#   <install-root>/app/releases/<version>/www
#   <install-root>/app/current -> releases/<version>
#
# The package is produced by the ZenTao release pipeline (Z-REL-01); this
# script never merges or rebuilds ZenTao sources.
#
# Usage:
#   stage-app-package.sh <package|app-dir> <install-root> [version]

set -Eeuo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: stage-app-package.sh <package|app-dir> <install-root> [version]" >&2
    exit 2
fi

readonly input="$1"
readonly install_root="$2"
readonly requested_version="${3:-}"
readonly tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

if [[ ! -e "${input}" ]]; then
    echo "application package does not exist: ${input}" >&2
    exit 1
fi

app_root=""
mkdir -p "${tmpdir}/app"
case "${input}" in
    *.tar.zst|*.tar.gz|*.tgz|*.tar.xz|*.tar|*.zip)
        case "${input}" in
            *.zip) unzip -q "${input}" -d "${tmpdir}/app" ;;
            *)     tar -xf "${input}" -C "${tmpdir}/app" ;;
        esac
        if [[ -d "${tmpdir}/app/www" ]]; then
            app_root="${tmpdir}/app"
        else
            app_root="$(find "${tmpdir}/app" -mindepth 1 -maxdepth 1 -type d | head -1)"
        fi
        if [[ -z "${app_root}" ]]; then
            app_root="${tmpdir}/app"
        fi
        ;;
    *)
        app_root="$(cd "$(dirname "${input}")" && pwd)/$(basename "${input}")"
        ;;
esac

if [[ ! -f "${app_root}/www/index.php" ]]; then
    echo "application package is missing www/index.php: ${app_root}" >&2
    exit 1
fi

version="${requested_version}"
if [[ -z "${version}" ]]; then
    version="$(cat "${app_root}/VERSION" 2>/dev/null || true)"
fi
if [[ -z "${version}" ]]; then
    version="provided"
fi
version="$(printf '%s' "${version}" | tr -c 'A-Za-z0-9._-' '_')"

release_dir="${install_root}/app/releases/${version}"
mkdir -p "$(dirname "${release_dir}")"
if [[ -e "${release_dir}" ]]; then
    echo "release directory already exists: ${release_dir}" >&2
    exit 1
fi
cp -a "${app_root}" "${release_dir}"

ln -sfn "releases/${version}" "${install_root}/app/current"

echo "${release_dir}/www"
