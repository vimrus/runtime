#!/usr/bin/env bash
#
# Keepalived health check: the node may hold the VIP only while the local
# Caddy Gateway and Runtime Host are alive. Fixed paths only; no user input.
#
# Usage: check_gateway.sh [--diagnose]
#   default: exit 0 when healthy, 1 otherwise (Keepalived track_script)
#   --diagnose: print a structured health summary and exit accordingly

set -Eeuo pipefail

health_url="${ZENTAO_GATEWAY_HEALTH_URL:-http://127.0.0.1:80/_runtime/healthz}"
timeout="${ZENTAO_GATEWAY_HEALTH_TIMEOUT:-3}"

if [[ "${1:-}" == "--diagnose" ]]; then
    body="$(curl --silent --max-time "${timeout}" "${health_url}" 2>/dev/null || true)"
    code="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time "${timeout}" "${health_url}" 2>/dev/null || true)"
    printf '{"url":"%s","statusCode":"%s","body":%s}\n' "${health_url}" "${code}" "$(printf '%s' "${body}" | jq -R -s .)"
    [[ "${code}" == "200" ]]
else
    curl --fail --silent --max-time "${timeout}" "${health_url}" >/dev/null
fi
