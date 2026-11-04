#!/usr/bin/env bash
#
# Verify the JSONL -> Parquet pipeline: access and runtime logs are written
# as per-node JSONL files, completed hourly segments can be converted on
# demand, the conversion is idempotent, and the resulting Hive-partitioned
# Parquet is published under the observability dataset.
#
# Usage: jsonl-parquet-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly work="$(mktemp -d)"
readonly container="zentao-jsonl-${RANDOM}-$$"

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

cat > "${work}/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"
logPath = "/shared/logs/runtime.log"
logMaxBytes = 16777216
logMaxBackups = 5

[web]
listen = "127.0.0.1:8080"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[[apps]]
id = "zentao"
path = "/"
dir = "/shared/app/zentaopms"
webRoot = "www"
enabled = true

# The synthetic app used by this test does not implement Queue Bridge.
[queue]
enabled = false

[observability]
enabled = true
datasetRoot = "/shared/observability"
spoolPath = "/shared/spool/observability"
maxSpoolBytes = 1073741824
maxBatchRows = 10000
maxBatchBytes = 16777216
flushInterval = "1s"
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

docker exec "${container}" curl --fail --silent "http://127.0.0.1:8080/index.php?x=1" >/dev/null
docker exec "${container}" curl --fail --silent "http://127.0.0.1:8080/index.php?x=2" >/dev/null
sleep 1

docker exec "${container}" zentao stop --control-socket /tmp/zentao.sock >/dev/null
if ! docker wait "${container}" >/dev/null 2>&1; then
    echo "runtime did not stop cleanly" >&2
    exit 1
fi

for source in access runtime; do
    if [[ "${source}" == "access" ]]; then
        stamp="$(date -u +%Y-%m-%dT%H-%M-%S.000)"
        active="${source}.jsonl"
        segment="${source}-${stamp}-time.jsonl"
    else
        stamp="$(date -u +%Y%m%dT%H%M%S.000000000Z)"
        active="${source}.jsonl"
        segment="${source}.jsonl-${stamp}.log"
    fi
    docker run --rm --entrypoint sh --volume "${work}/app:/shared:rw" "${RUNTIME_IMAGE}" \
        -c "if [ -s '/shared/logs/${active}' ]; then mv '/shared/logs/${active}' '/shared/logs/${segment}'; fi"
done

if ! docker start "${container}" >/dev/null; then
    echo "failed to restart runtime container" >&2
    exit 1
fi
wait_ready

first="$(docker exec "${container}" zentao convert-jsonl --control-socket /tmp/zentao.sock)"
echo "first conversion: ${first}"
echo "${first}" | jq -e '.events > 0 and .segments >= 1 and .malformed == 0' >/dev/null \
    || { echo "expected converted events from access/runtime JSONL" >&2; exit 1; }

docker exec "${container}" sh -c "find /shared/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=/*' | grep -q ." \
    || { echo "no Parquet published for the single-node deployment" >&2; exit 1; }
docker exec "${container}" test -f "/shared/logs/.jsonl-state.json" \
    || { echo "converter state file missing" >&2; exit 1; }

second="$(docker exec "${container}" zentao convert-jsonl --control-socket /tmp/zentao.sock)"
echo "second conversion: ${second}"
echo "${second}" | jq -e '.events == 0 and .segments == 0' >/dev/null \
    || { echo "conversion must be idempotent" >&2; exit 1; }

echo "jsonl -> parquet smoke passed"
