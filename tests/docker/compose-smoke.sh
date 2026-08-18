#!/usr/bin/env bash
#
# R-DOCKER-01 smoke test: start the Runtime via Docker Compose, wait for the
# healthcheck, verify readiness and Classic PHP over the public port, then
# tear the stack down.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly compose_file="${repo_root}/packaging/docker/compose.smoke.yaml"
readonly port="${ZENTAO_SMOKE_PORT:-18080}"

cleanup() {
    docker compose -f "${compose_file}" down >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose -f "${compose_file}" up -d web >/dev/null

ready=0
for _ in $(seq 1 30); do
    if curl --fail --silent "http://127.0.0.1:${port}/_runtime/readiness" >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 1
done
[[ "${ready}" == "1" ]]

curl --fail --silent "http://127.0.0.1:${port}/_runtime/readiness" \
    | jq --exit-status '.status == "ready"' >/dev/null
curl --fail --silent "http://127.0.0.1:${port}/" \
    | jq --exit-status '.zts == true and .ioncube == true and (.php | startswith("8.4."))' >/dev/null

echo "compose smoke test passed"
