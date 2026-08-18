#!/usr/bin/env bash
#
# End-to-end release supply chain check for one package:
#   checksum, manifest, SBOM, size report, and GPG signature.
#
# Usage: verify-supply-chain.sh <package.tar.zst> [gpg-keyring]

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: verify-supply-chain.sh <package.tar.zst> [gpg-keyring]" >&2
    exit 2
fi

readonly package="$1"
readonly dir="$(dirname "${package}")"
readonly base="$(basename "${package%.tar.zst}")"
readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

[[ -f "${package}" ]]
[[ -f "${dir}/${base}.sha256" ]]
[[ -f "${dir}/${base}.size.json" ]]
[[ -f "${dir}/SHA256SUMS" ]]
[[ -f "${dir}/SHA256SUMS.sig" ]]

(
    cd "${dir}"
    sha256sum --check --strict "$(basename "${base}.sha256")"
)

"${script_dir}/verify-signature.sh" "${dir}" "${2:-}" >/dev/null

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT
tar -xf "${package}" -C "${tmpdir}"
root="$(find "${tmpdir}" -mindepth 1 -maxdepth 1 -type d | head -1)"

[[ -f "${root}/manifest.json" ]]
[[ -f "${root}/sbom.json" ]]
jq -e '.schema == 1 and (.components.php.version | length > 0)' "${root}/manifest.json" >/dev/null
jq -e '.bomFormat == "CycloneDX" and (.components | length > 0)' "${root}/sbom.json" >/dev/null

echo "supply chain verified: ${package}"
