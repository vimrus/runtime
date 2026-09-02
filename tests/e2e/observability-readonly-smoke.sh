#!/usr/bin/env bash
#
# Verify observability degradation when the shared dataset root becomes
# read-only (NFS read-only / mount-lost simulation): business requests keep
# working, accepted events stay in the node-local spool, and no Parquet is
# published into the shared dataset.
#
# Usage: observability-readonly-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly work="$(mktemp -d)"
readonly container="zentao-obs-ro-${RANDOM}-$$"

cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    chmod -R u+rwX "${work}" 2>/dev/null || true
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

mkdir -p "${work}/shared"
cat > "${work}/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"
logPath = "/shared/logs/runtime.log"
nodeID = "node-ro"
clusterID = "cluster-readonly"

[web]
root = "/shared/app/zentaopms/www"
listen = "127.0.0.1:8080"
threads = 2
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
spoolPath = "/shared/spool"
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
    --volume "${work}/shared:/shared/observability:ro" \
    --volume "${work}/runtime.toml:/runtime-config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'mkdir -p /shared/config /shared/logs /shared/spool && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

wait_ready

response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/obs.php)"
echo "${response}" | jq -e '.accepted >= 1' >/dev/null \
    || { echo "event not accepted with read-only dataset: ${response}" >&2; exit 1; }

docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/index.php >/dev/null \
    || { echo "business request failed while dataset was read-only" >&2; exit 1; }

sleep 3

spool_files="$(docker exec "${container}" sh -c 'find /shared/spool -type f -name "*.json" 2>/dev/null | wc -l')"
[[ "${spool_files}" -gt 0 ]] || { echo "no spool files retained while dataset was read-only" >&2; exit 1; }

parquet_files="$(docker exec "${container}" sh -c 'find /shared/observability -type f -name "*.parquet" 2>/dev/null | wc -l')"
[[ "${parquet_files}" == "0" ]] || { echo "parquet published to a read-only dataset" >&2; exit 1; }

echo "observability read-only smoke passed"
