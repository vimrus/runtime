#!/usr/bin/env bash
#
# Compose a platform-independent ZenTao application artifact from edition
# source directories following the include order:
#   open(opensource) -> biz -> max -> ipd
#
# NOTE: production application packages are provided by the ZenTao release
# pipeline (Z-REL-01). This script is only a synthetic fallback for contract
# tests; use stage-app-package.sh to consume provided packages.
#
# Usage: compose-app.sh <editions-dir> <output-dir> <edition>
# Editions: open|biz|max|ipd

set -Eeuo pipefail

if [[ $# -ne 3 ]]; then
    echo "usage: compose-app.sh <editions-dir> <output-dir> <edition>" >&2
    exit 2
fi

readonly editions_dir="$1"
readonly output_dir="$2"
readonly edition="$3"
readonly output_stage="${output_dir}/zentao-app-${edition}"

rm -rf "${output_stage}"
mkdir -p "${output_stage}"

apply_edition() {
    local source_dir="${editions_dir}/$1"
    if [[ -d "${source_dir}" ]]; then
        cp -a "${source_dir}/." "${output_stage}/"
        echo "merged: $1"
    fi
}

case "${edition}" in
    open)
        apply_edition opensource
        ;;
    biz)
        apply_edition opensource
        apply_edition biz
        apply_edition bizext
        ;;
    max)
        apply_edition opensource
        apply_edition biz
        apply_edition bizext
        apply_edition max
        ;;
    ipd)
        apply_edition opensource
        apply_edition biz
        apply_edition bizext
        apply_edition max
        apply_edition ipd
        ;;
    *)
        echo "unknown edition: ${edition}" >&2
        exit 2
        ;;
esac

if [[ ! -f "${output_stage}/www/index.php" ]]; then
    echo "composed application is missing www/index.php" >&2
    exit 1
fi

find "${output_stage}" \( -name '.git' -o -name '__pycache__' -o -name '.cache' \) -prune -exec rm -rf {} +
echo "application composed: ${output_stage}"
