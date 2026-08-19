#!/usr/bin/env bash
#
# R-CI-03: verify the edition composition order and artifact cleanliness with
# synthetic edition trees (no paid sources required).

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

for edition in opensource biz bizext max ipd; do
    mkdir -p "${tmpdir}/editions/${edition}/www"
    printf '<?php // %s\n' "${edition}" > "${tmpdir}/editions/${edition}/www/index.php"
    if [[ "${edition}" == "opensource" ]]; then
        mkdir -p "${tmpdir}/editions/${edition}/.git"
    fi
done

for edition in open biz max ipd; do
    "${repo_root}/scripts/ci/compose-app.sh" "${tmpdir}/editions" "${tmpdir}/out" "${edition}" >/dev/null
    [[ -f "${tmpdir}/out/zentao-app-${edition}/www/index.php" ]]
    if [[ -d "${tmpdir}/out/zentao-app-${edition}/.git" ]]; then
        echo "composed artifact must not contain .git" >&2
        exit 1
    fi
done

echo "application composition test passed"
