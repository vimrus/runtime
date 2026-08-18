#!/usr/bin/env bash

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: verify-artifact.sh <artifact-root>" >&2
    exit 2
fi

readonly artifact_root="$1"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly runtime_bin="${artifact_root}/bin/zentao-runtime"
readonly libphp="${artifact_root}/php/lib/libphp.so"
readonly loader="${artifact_root}/php/lib/ioncube/ioncube_loader_lin_8.4_ts.so"

for required in "${runtime_bin}" "${libphp}" "${loader}" "${artifact_root}/config/php.ini"; do
    if [[ ! -f "${required}" ]]; then
        echo "missing artifact file: ${required}" >&2
        exit 1
    fi
done

for removed in \
    "${artifact_root}/php/bin/phpdbg" \
    "${artifact_root}/php/bin/phpize" \
    "${artifact_root}/php/bin/php-config" \
    "${artifact_root}/php/include" \
    "${artifact_root}/php/lib/php/build"; do
    if [[ -e "${removed}" ]]; then
        echo "development-only artifact was not trimmed: ${removed}" >&2
        exit 1
    fi
done

expected=$(printf '%s\n' zend_signal_globals_id zend_signal_globals_offset zend_signal_handler_unblock)
actual=$(nm -D "${libphp}" | awk '$3 ~ /^zend_signal_(globals_id|globals_offset|handler_unblock)$/ {print $3}' | sort -u)
if [[ "${actual}" != "${expected}" ]]; then
    echo "libphp zend_signal ABI symbols do not match the ionCube contract" >&2
    diff -u <(printf '%s\n' "${expected}") <(printf '%s\n' "${actual}") || true
    exit 1
fi

"${repo_root}/scripts/package/verify-no-db-driver.sh" "${runtime_bin}"

echo "artifact verified: ${artifact_root}"
