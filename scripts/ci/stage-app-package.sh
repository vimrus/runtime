#!/usr/bin/env bash
#
# Stage an externally produced, platform-independent ZenTao application
# package into a pluggable application slot:
#   <install-root>/<slot>/www
#
# The package is produced by the ZenTao release pipeline (Z-REL-01); this
# script never merges or rebuilds ZenTao sources.
#
# Usage:
#   stage-app-package.sh <package|app-dir> <install-root> [slot]
#
# The optional slot selects the application directory below the install root
# (default: app/zentaopms, the legacy layout). Pluggable multi-application
# installations use a slot such as apps/zentao-ipd.

set -Eeuo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: stage-app-package.sh <package|app-dir> <install-root> [slot]" >&2
    exit 2
fi

readonly input="$1"
readonly install_root="$2"
readonly slot="${3:-app/zentaopms}"
case "${slot}" in
    /*|*..*)
        echo "application slot must be a relative path without ..: ${slot}" >&2
        exit 2
        ;;
esac
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
            *.zip)
                while IFS= read -r entry; do
                    [[ "${entry}" != /* && "${entry}" != ../* && "${entry}" != */../* && "${entry}" != ".." && "${entry}" != *\\* && "${entry}" != [A-Za-z]:* ]] || {
                        echo "archive contains an unsafe path: ${entry}" >&2
                        exit 1
                    }
                done < <(unzip -Z1 "${input}")
                unzip -q "${input}" -d "${tmpdir}/app"
                ;;
            *)
                while IFS= read -r entry; do
                    [[ "${entry}" != /* && "${entry}" != ../* && "${entry}" != */../* && "${entry}" != ".." && "${entry}" != *\\* && "${entry}" != [A-Za-z]:* ]] || {
                        echo "archive contains an unsafe path: ${entry}" >&2
                        exit 1
                    }
                done < <(tar -tf "${input}")
                tar -xf "${input}" -C "${tmpdir}/app"
                ;;
        esac
        if find "${tmpdir}/app" -type l -print -quit | grep -q .; then
            echo "application archive must not contain symbolic links" >&2
            exit 1
        fi
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

if [[ -L "${app_root}" ]] || find "${app_root}" -type l -print -quit | grep -q .; then
    echo "application input must not contain symbolic links" >&2
    exit 1
fi

if [[ ! -f "${app_root}/www/index.php" ]]; then
    echo "application package is missing www/index.php: ${app_root}" >&2
    exit 1
fi

app_dir="${install_root}/${slot}"
if [[ -e "${app_dir}" ]]; then
    echo "application directory already exists: ${app_dir}; back it up and remove it before staging" >&2
    exit 1
fi
mkdir -p "$(dirname "${app_dir}")"
cp -a "${app_root}" "${app_dir}"

echo "${app_dir}/www"
