#!/usr/bin/env bash
#
# Verify the shared-dataset contract with two Runtime nodes: each node writes
# only its own node=<nodeID> Parquet partition while both publish into one
# shared dataset root (NFS-like topology). The test intentionally uses
# distinct nodeID values and asserts no cross-node part files are produced.
#
# Usage: observability-dual-node-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly work="$(mktemp -d)"
readonly container_a="zentao-obs-a-${RANDOM}-$$"
readonly container_b="zentao-obs-b-${RANDOM}-$$"

cleanup() {
    docker rm --force "${container_a}" >/dev/null 2>&1 || true
    docker rm --force "${container_b}" >/dev/null 2>&1 || true
    chmod -R u+rwX "${work}" 2>/dev/null || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

stage_node() {
    local node_dir="$1"
    if [[ -n "${app_input}" ]]; then
        web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${app_input}" "${node_dir}")"
    else
        synthetic="${work}/synthetic-app"
        if [[ ! -d "${synthetic}" ]]; then
            "${repo_root}/scripts/ci/make-synthetic-app-package.sh" "${synthetic}" >/dev/null
        fi
        web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${synthetic}" "${node_dir}")"
    fi
    staged_release="$(dirname "${web_root}")"
    if [[ -f "${staged_release}/www/index.php" ]]; then
        "${repo_root}/scripts/ci/patch-classic-mode.sh" "${staged_release}" >/dev/null || true
    fi
    cp "${repo_root}/tests/fixtures/php/obs.php" "${web_root}/obs.php"
}

write_runtime_config() {
    local path="$1"
    local node_id="$2"
    cat > "${path}" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"
logPath = "/shared/logs/runtime.log"
nodeID = "${node_id}"

[web]
listen = "127.0.0.1:8080"
threads = 2
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
bridgePort = 8082
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
}

start_node() {
    local container="$1"
    local node_dir="$2"
    local config="$3"
    docker run --detach --name "${container}" \
        --entrypoint sh \
        --volume "${node_dir}:/shared:rw" \
        --volume "${work}/shared:/shared/observability:rw" \
        --volume "${work}/shared-app:/shared/data/apps/zentao/observability:rw" \
        --volume "${config}:/runtime-config/runtime.toml:ro" \
        "${RUNTIME_IMAGE}" \
        -c 'mkdir -p /shared/config /shared/logs /shared/spool && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null
}

wait_ready() {
    local container="$1"
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

mkdir -p "${work}/shared" "${work}/shared-app"
stage_node "${work}/node-a"
stage_node "${work}/node-b"
write_runtime_config "${work}/runtime-a.toml" "node-a"
write_runtime_config "${work}/runtime-b.toml" "node-b"

start_node "${container_a}" "${work}/node-a" "${work}/runtime-a.toml"
start_node "${container_b}" "${work}/node-b" "${work}/runtime-b.toml"
wait_ready "${container_a}"
wait_ready "${container_b}"

for container in "${container_a}" "${container_b}"; do
    response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/obs.php)"
    echo "${response}" | jq -e '.accepted >= 1' >/dev/null \
        || { echo "event not accepted by ${container}: ${response}" >&2; exit 1; }
done

for _ in $(seq 1 10); do
    if [[ -n "$(docker exec "${container_a}" sh -c "find /shared/data/apps/zentao/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=node-a/*' 2>/dev/null")" ]] \
        && [[ -n "$(docker exec "${container_a}" sh -c "find /shared/data/apps/zentao/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=node-b/*' 2>/dev/null")" ]]; then
        break
    fi
    sleep 1
done

node_a_parts="$(docker exec "${container_a}" sh -c "find /shared/data/apps/zentao/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=node-a/*' 2>/dev/null")"
node_b_parts="$(docker exec "${container_a}" sh -c "find /shared/data/apps/zentao/observability/logs/schema=v1 -type f -name '*.parquet' -path '*/node=node-b/*' 2>/dev/null")"
[[ -n "${node_a_parts}" ]] || { echo "node-a published no parquet parts" >&2; exit 1; }
[[ -n "${node_b_parts}" ]] || { echo "node-b published no parquet parts" >&2; exit 1; }

all_parts="$(docker exec "${container_a}" sh -c "find /shared/data/apps/zentao/observability/logs/schema=v1 -type f -name '*.parquet' 2>/dev/null")"
if echo "${all_parts}" | grep -vE '/node=node-[ab]/' | grep -q .; then
    echo "shared dataset contains a partition that is not owned by node-a or node-b" >&2
    exit 1
fi

echo "observability dual-node smoke passed"
