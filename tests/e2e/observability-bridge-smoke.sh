#!/usr/bin/env bash
#
# Verify the private PHP Observability Bridge: token authentication, schema
# validation, bounded event ingestion, and publication to the node-owned
# Parquet partition.
#
# Usage: observability-bridge-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly work="$(mktemp -d)"
readonly container="zentao-obs-${RANDOM}-$$"

cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

if [[ -n "${app_input}" ]]; then
    web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${app_input}" "${work}/app")"
else
    synthetic="${work}/synthetic-app"
    "${repo_root}/scripts/ci/make-synthetic-app-package.sh" "${synthetic}" >/dev/null
    web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${synthetic}" "${work}/app")"
fi
staged_release="$(dirname "${web_root}")"
if [[ -f "${staged_release}/www/index.php" ]]; then
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${staged_release}" >/dev/null || true
fi
cp "${repo_root}/tests/fixtures/php/obs.php" "${web_root}/obs.php"

cat > "${work}/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"
logPath = "/shared/logs/runtime.log"

[web]
root = "/shared/app/zentaopms/www"
listen = "127.0.0.1:8080"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

# The synthetic app used by this test does not implement Queue Bridge.
[queue]
enabled = false

[observability]
enabled = true
bridgeListen = "127.0.0.1:8082"
datasetRoot = "/shared/observability"
spoolPath = "/shared/spool/observability"
maxSpoolBytes = 1073741824
maxBatchRows = 10000
maxBatchBytes = 16777216
flushInterval = "1s"
maxFlushInterval = "3s"
metricsDays = 7
logDays = 7
jsonlConvertInterval = "1h"
jsonlConvertSources = ["access", "runtime"]
jsonlKeepDays = 7
EOF

wait_ready() {
    ready=0
    for _ in $(seq 1 30); do
        if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness >/dev/null 2>&1; then
            ready=1
            break
        fi
        sleep 1
    done
    [[ "${ready}" == "1" ]]
}

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/runtime.toml:/runtime-config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'mkdir -p /shared/config /shared/logs /shared/observability /shared/spool && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

wait_ready

node_id="$(docker exec "${container}" cat /shared/data/node-id)"
response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/obs.php)"
echo "${response}" | jq -e '.accepted == 2' >/dev/null \
    || { echo "event not accepted: ${response}" >&2; exit 1; }

for _ in $(seq 1 10); do
    if docker exec "${container}" sh -c "find /shared/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=${node_id}/*' 2>/dev/null | grep -q ."; then
        break
    fi
    sleep 1
done
docker exec "${container}" sh -c "find /shared/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=${node_id}/*' 2>/dev/null | grep -q ." \
    || { echo "PHP event was not published to node partition" >&2; exit 1; }
docker exec "${container}" sh -c "find /shared/observability/metrics/schema=v1 -type f -name '*.parquet' -path '*/node=${node_id}/*' 2>/dev/null | grep -q ." \
    || { echo "PHP metric was not published to node partition" >&2; exit 1; }

echo "observability bridge smoke passed"
