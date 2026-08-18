#!/usr/bin/env bash
#
# Assemble a versioned Runtime package (tar.zst), its SHA256SUMS and a size
# report from a staging directory.
#
# Usage: assemble-runtime.sh <staging-dir> <output-name> <output-dir>

set -Eeuo pipefail

if [[ $# -ne 3 ]]; then
    echo "usage: assemble-runtime.sh <staging-dir> <output-name> <output-dir>" >&2
    exit 2
fi

readonly staging="$1"
readonly name="$2"
readonly output_dir="$3"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ ! -d "${staging}" ]]; then
    echo "staging directory is required" >&2
    exit 1
fi

mkdir -p "${output_dir}"
if [[ ! -f "${staging}/manifest.json" ]]; then
    "${repo_root}/scripts/package/generate-manifest.sh" "${staging}"
fi

tar --sort=name --owner=0 --group=0 --numeric-owner \
    --use-compress-program="zstd -19" \
    -cf "${output_dir}/${name}.tar.zst" \
    -C "$(dirname "${staging}")" "$(basename "${staging}")"

(
    cd "${output_dir}"
    sha256sum "${name}.tar.zst" > "${name}.sha256"
)

tar -tf "${output_dir}/${name}.tar.zst" > /dev/null
echo "runtime package: ${output_dir}/${name}.tar.zst"
cat "${output_dir}/${name}.sha256"
