#!/usr/bin/env bash
#
# Apply the FrankenPHP Classic-mode compatibility patch to a staged ZenTao
# application copy (never to the provided zip). ZenTao's www/index.php calls
# frankenphp_handle_request() whenever php_sapi_name() == 'frankenphp', but
# that function is worker-mode only. The correct detection is the
# FRANKENPHP_WORKER server variable.
#
# The upstream fix is tracked in zentao-application-adaptation-plan.md §18.6;
# this patch is a temporary integration shim applied only to staged copies.
#
# Usage: patch-classic-mode.sh <app-release-root>

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: patch-classic-mode.sh <app-release-root>" >&2
    exit 2
fi

readonly app_root="$1"
readonly index="${app_root}/www/index.php"

if [[ ! -f "${index}" ]]; then
    echo "www/index.php not found: ${index}" >&2
    exit 1
fi

cp "${index}" "${index}.zentao-orig"

# 1. Only treat FrankenPHP as worker-mode when FRANKENPHP_WORKER is present.
sed -i \
    "s|if(php_sapi_name() == 'frankenphp')|if(php_sapi_name() == 'frankenphp' \&\& isset(\$_SERVER['FRANKENPHP_WORKER']))|" \
    "${index}"

# 2. In worker mode, loop like the official FrankenPHP profile.
sed -i \
    "s|\\\\frankenphp_handle_request(\$handler);|while(\\\\frankenphp_handle_request(\$handler)) {}|" \
    "${index}"

if ! grep -q "FRANKENPHP_WORKER" "${index}"; then
    echo "FrankenPHP Classic compatibility patch could not be applied (pattern mismatch)" >&2
    echo "original copy kept at ${index}.zentao-orig" >&2
    exit 1
fi

echo "FrankenPHP Classic compatibility patch applied to ${index}"
