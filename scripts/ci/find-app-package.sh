#!/usr/bin/env bash
#
# Resolve the externally provided ZenTao application package for an edition
# from the agreed package directory layout:
#
#   <ZENTAO_APP_PACKAGES_DIR>/
#     opensource/zentao-app.zip      (or any single *.zip)
#     biz/zentao-app.zip
#     max/zentao-app.zip
#     ipd/zentao-app.zip
#
# Default package directory is <repo-root>/../app-packages, i.e.
# /home/z/dev/app-packages. A direct file path also works:
#   find-app-package.sh /path/to/zentao-app-ipd-22.0.zip
#
# Usage: find-app-package.sh <opensource|biz|max|ipd|path-to-package>

set -Eeuo pipefail

edition="$1"
if [[ -f "${edition}" ]]; then
    echo "${edition}"
    exit 0
fi

case "${edition}" in
    opensource|biz|max|ipd) ;;
    *) echo "unknown edition: ${edition}" >&2; exit 2 ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
packages_dir="${ZENTAO_APP_PACKAGES_DIR:-$(dirname "${repo_root}")/app-packages}"
edition_dir="${packages_dir}/${edition}"

for candidate in \
    "${edition_dir}/zentao-app.zip" \
    "${edition_dir}/zentao-app-${edition}.zip"; do
    if [[ -f "${candidate}" ]]; then
        echo "${candidate}"
        exit 0
    fi
done

matches=()
while IFS= read -r -d '' zip; do
    matches+=("${zip}")
done < <(find "${edition_dir}" -maxdepth 1 -name '*.zip' -print0 2>/dev/null || true)

if [[ ${#matches[@]} -eq 1 ]]; then
    echo "${matches[0]}"
    exit 0
fi
if [[ ${#matches[@]} -gt 1 ]]; then
    echo "multiple packages found in ${edition_dir}; keep exactly one or set ZENTAO_APP_PACKAGES_DIR" >&2
    exit 1
fi

echo "no application package found for ${edition} in ${edition_dir}" >&2
echo "place the zip at: ${edition_dir}/zentao-app.zip" >&2
exit 1
