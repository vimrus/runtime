#!/usr/bin/env bash
# Test the Go Runtime Host without FrankenPHP's optional Watcher C extension.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
php_config="${PHP_CONFIG:-}"
if [[ -z "${php_config}" ]]; then
    if command -v php-config >/dev/null 2>&1; then
        php_config="$(command -v php-config)"
    elif [[ -x /opt/zentao/php/bin/php-config ]]; then
        php_config=/opt/zentao/php/bin/php-config
    fi
fi

if [[ -z "${php_config}" ]]; then
    echo "php-config is required to compile the FrankenPHP Host" >&2
    echo "set PHP_CONFIG or install PHP 8.4 development headers" >&2
    exit 1
fi

readonly php_cflags="$(${php_config} --includes)"
cd "${repo_root}"
CGO_CFLAGS="${CGO_CFLAGS:-} ${php_cflags}" \
    go test -tags nowatcher ./cmd/zentao-runtime
