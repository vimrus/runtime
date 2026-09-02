#!/usr/bin/env bash
#
# Simulate a shared dataset outage and recovery at the mount layer: after a
# read-only remount, events must stay in the node-local spool and business
# requests must continue; after the remount back to read-write, the Publisher
# must replay the spool and publish Parquet without restart.
#
# The container is started with --privileged so the test can remount the
# dataset bind mount; production deployments do not need this capability.
#
# Usage: observability-fault-recovery-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly work="$(mktemp -d)"
readonly container="zentao-obs-fault-${RANDOM}-$$"

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
nodeID = "node-fault"
clusterID = "cluster-fault-recovery"

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

parquet_count() {
    docker exec "${container}" sh -c 'find /shared/observability -type f -name "*.parquet" 2>/dev/null | wc -l'
}

spool_count() {
    docker exec "${container}" sh -c 'find /shared/spool -type f -name "*.json" 2>/dev/null | wc -l'
}

docker run --detach --name "${container}" \
    --privileged \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/shared:/shared/observability:rw" \
    --volume "${work}/runtime.toml:/runtime-config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'mkdir -p /shared/config /shared/logs /shared/spool && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

wait_ready

response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/obs.php)"
echo "${response}" | jq -e '.accepted >= 1' >/dev/null \
    || { echo "baseline event not accepted: ${response}" >&2; exit 1; }

for _ in $(seq 1 10); do
    [[ "$(parquet_count)" -gt 0 ]] && break
    sleep 1
done
baseline_parquet="$(parquet_count)"
[[ "${baseline_parquet}" -gt 0 ]] || { echo "baseline parquet was not published" >&2; exit 1; }

docker exec "${container}" mount -o remount,bind,ro /shared/observability >/dev/null

response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/obs.php)"
echo "${response}" | jq -e '.accepted >= 1' >/dev/null \
    || { echo "event not accepted during dataset outage: ${response}" >&2; exit 1; }
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/index.php >/dev/null \
    || { echo "business request failed during dataset outage" >&2; exit 1; }

for _ in $(seq 1 10); do
    [[ "$(spool_count)" -gt 0 ]] && break
    sleep 1
done
[[ "$(spool_count)" -gt 0 ]] || { echo "no spool files retained during dataset outage" >&2; exit 1; }
fault_parquet="$(parquet_count)"
[[ "${fault_parquet}" == "${baseline_parquet}" ]] || { echo "parquet changed while dataset was read-only" >&2; exit 1; }

docker exec "${container}" mount -o remount,bind,rw /shared/observability >/dev/null

for _ in $(seq 1 15); do
    if [[ "$(spool_count)" == "0" ]] && [[ "$(parquet_count)" -gt "${baseline_parquet}" ]]; then
        break
    fi
    sleep 1
done

[[ "$(spool_count)" == "0" ]] || { echo "spool was not replayed after recovery" >&2; exit 1; }
[[ "$(parquet_count)" -gt "${baseline_parquet}" ]] || { echo "recovered parquet was not published" >&2; exit 1; }

echo "observability fault-recovery smoke passed"
