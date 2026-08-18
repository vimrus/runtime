#!/usr/bin/env bash
#
# Sign release artifacts with GPG. Every *.tar.zst plus SHA256SUMS receives a
# detached ASCII-armored signature.
#
# Usage: sign-artifacts.sh <artifact-dir> [key-fingerprint]
# Key selection: ZENTAO_SIGNING_KEY env var, or the first secret key in the
# default keyring. Signing fails loudly when no usable key exists.

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: sign-artifacts.sh <artifact-dir> [key-fingerprint]" >&2
    exit 2
fi

readonly artifact_dir="$1"
key="${ZENTAO_SIGNING_KEY:-${2:-}}"

if [[ ! -d "${artifact_dir}" ]]; then
    echo "artifact directory does not exist: ${artifact_dir}" >&2
    exit 1
fi

if [[ -z "${key}" ]]; then
    key="$(gpg --list-secret-keys --with-colons 2>/dev/null | awk -F: '$1 == "sec" {print $5; exit}')"
fi
if [[ -z "${key}" ]]; then
    echo "no GPG signing key available (set ZENTAO_SIGNING_KEY)" >&2
    exit 1
fi

if [[ ! -f "${artifact_dir}/SHA256SUMS" ]]; then
    echo "SHA256SUMS is required before signing" >&2
    exit 1
fi

sign() {
    gpg --batch --yes --armor --detach-sign \
        --local-user "${key}" \
        --output "$1.sig" "$1"
}

sign "${artifact_dir}/SHA256SUMS"
while IFS= read -r -d '' archive; do
    case "${archive}" in
        *.tar.zst|*.zip) sign "${archive}" ;;
    esac
done < <(find "${artifact_dir}" -maxdepth 1 -type f -print0 | sort -z)

echo "artifacts signed with key ${key}"
