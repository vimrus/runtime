#!/usr/bin/env bash
#
# Produce a package size report: compressed/uncompressed sizes, largest files
# and per-directory statistics.
#
# Usage: size-report.sh <package.tar.zst> [output.json]

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: size-report.sh <package.tar.zst> [output.json]" >&2
    exit 2
fi

readonly package="$1"
readonly output="${2:-${package%.tar.zst}.size.json}"
readonly tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

tar -xf "${package}" -C "${tmpdir}"
root="$(find "${tmpdir}" -mindepth 1 -maxdepth 1 -type d | head -1)"
compressed="$(stat -c %s "${package}")"
uncompressed="$(du -sb "${root}" | awk '{print $1}')"

jq -n \
    --arg package "$(basename "${package}")" \
    --argjson compressed "${compressed}" \
    --argjson uncompressed "${uncompressed}" \
    --arg largest "$(find "${root}" -type f -printf '%s %p\n' | sort -rn | head -1 | awk '{print $2}' | sed "s|${root}/||")" \
    --arg largestBytes "$(find "${root}" -type f -printf '%s\n' | sort -rn | head -1)" \
    '{
        package: $package,
        compressedBytes: $compressed,
        uncompressedBytes: $uncompressed,
        ratio: ($compressed / $uncompressed * 100 | floor / 100),
        largestFile: $largest,
        largestFileBytes: ($largestBytes | tonumber),
        directories: {}
    }' > "${output}"

while IFS=$'\t' read -r size dir; do
    relative="${dir#${root}/}"
    jq --argjson bytes "${size}" --arg dir "${relative}" \
        '.directories[$dir] = $bytes' "${output}" > "${output}.tmp"
    mv "${output}.tmp" "${output}"
done < <(du -sb "${root}"/* | sort -rn)

echo "size report: ${output}"
