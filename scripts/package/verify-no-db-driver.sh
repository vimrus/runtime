#!/usr/bin/env bash
#
# Verify that the Runtime binary does not embed database drivers, client
# libraries or business SQL. The Go Runtime must never connect to the ZenTao
# database directly.
#
# Usage: verify-no-db-driver.sh <runtime-binary>

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: verify-no-db-driver.sh <runtime-binary>" >&2
    exit 2
fi

binary="$1"
if [[ ! -x "${binary}" ]]; then
    echo "runtime binary not found: ${binary}" >&2
    exit 1
fi

if go version -m "${binary}" | grep -E 'go-sql-driver/mysql|jackc/pgx|/lib/pq|go-mssqldb|go-ora|sijms|denisenkom' ; then
    echo "runtime binary embeds a database driver" >&2
    exit 1
fi

echo "no database drivers embedded in ${binary}"
