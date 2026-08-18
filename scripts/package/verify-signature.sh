#!/usr/bin/env bash
#
# Verify detached GPG signatures for SHA256SUMS and every artifact in a
# directory.
#
# Usage: verify-signature.sh <artifact-dir> [keyring]
# Keyring: path to a GPG keyring file, or empty to use the default keyring.

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: verify-signature.sh <artifact-dir> [keyring]" >&2
    exit 2
fi

readonly artifact_dir="$1"
keyring_args=()
if [[ -n "${2:-}" ]]; then
    keyring_args=(--keyring "$2")
fi

verify() {
    gpg --batch "${keyring_args[@]}" --verify "$1.sig" "$1"
}

verify "${artifact_dir}/SHA256SUMS"
while IFS= read -r -d '' archive; do
    case "${archive}" in
        *.tar.zst|*.zip) verify "${archive}" ;;
    esac
done < <(find "${artifact_dir}" -maxdepth 1 -type f -print0 | sort -z)

echo "signatures verified in ${artifact_dir}"
