#!/usr/bin/env bash
#
# Verify an assembled Runtime/Full package: checksum, forbidden contents,
# manifest presence and size report.
#
# Usage: verify-package.sh <package.tar.zst>

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: verify-package.sh <package.tar.zst>" >&2
    exit 2
fi

readonly package="$1"
readonly checksum_file="${package%.tar.zst}.sha256"

if [[ ! -f "${checksum_file}" ]]; then
    echo "missing checksum file: ${checksum_file}" >&2
    exit 1
fi
(
    cd "$(dirname "${checksum_file}")"
    sha256sum --check --strict "$(basename "${checksum_file}")"
)

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT
tar -xf "${package}" -C "${tmpdir}"

root="$(find "${tmpdir}" -mindepth 1 -maxdepth 1 -type d | head -1)"
if [[ ! -f "${root}/manifest.json" ]]; then
    echo "package is missing manifest.json" >&2
    exit 1
fi

for forbidden in .git "__pycache__" "node_modules" ".cache"; do
    if tar -tf "${package}" | grep -q "/${forbidden}/"; then
        echo "package contains forbidden entry: ${forbidden}" >&2
        exit 1
    fi
done

echo "package verified: ${package}"
