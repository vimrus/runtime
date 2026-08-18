#!/usr/bin/env sh
set -eu

url="${ZENTAO_HEALTH_URL:-http://127.0.0.1:8080/_runtime/readiness}"
curl --fail --silent --show-error --max-time 3 "${url}" >/dev/null
