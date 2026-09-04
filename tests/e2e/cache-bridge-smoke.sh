#!/usr/bin/env bash
#
# Verify the private Runtime Cache Bridge end-to-end from PHP: capabilities,
# set, get and delete through the loopback listener.
#
# Usage: cache-bridge-smoke.sh [app-package|app-dir]

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:-}"
readonly service_src="${ZENTAO_SERVICE_SRC:-$(dirname "${repo_root}")/zentaopms/service}"
readonly work="$(mktemp -d)"
readonly container="zentao-cache-${RANDOM}-$$"

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
cp "${repo_root}/tests/fixtures/php/cache.php" "${web_root}/cache.php"
mkdir -p "$(dirname "${web_root}")/service/cache"
mkdir -p "$(dirname "${web_root}")/service/runtime"
cp "${service_src}/cache/cache.service.interface.php" "$(dirname "${web_root}")/service/cache/"
cp "${service_src}/cache/cache.backend.interface.php" "$(dirname "${web_root}")/service/cache/"
cp "${service_src}/cache/cache.service.php" "$(dirname "${web_root}")/service/cache/"
cp "${service_src}/cache/cached.facade.php" "$(dirname "${web_root}")/service/cache/"
mkdir -p "$(dirname "${web_root}")/service/cache/backend"
cp "${service_src}/cache/backend/runtime.backend.php" "$(dirname "${web_root}")/service/cache/backend/"
cp "${service_src}/cache/backend/null.backend.php" "$(dirname "${web_root}")/service/cache/backend/"
cp "${service_src}/cache/backend/redis.backend.php" "$(dirname "${web_root}")/service/cache/backend/"
cp "${service_src}/runtime/runtime.service.php" "$(dirname "${web_root}")/service/runtime/"

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
threads = 2
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384
[queue]
enabled = false
[cache]
enabled = true
listen = "127.0.0.1:8083"
maxCostBytes = 268435456
maxValueBytes = 1048576
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
    -c 'mkdir -p /shared/config /shared/logs && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

wait_ready

response="$(docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/cache.php)"
echo "${response}" | jq -e '
    .set == true and
    .get == "hello-cache" and
    .delete == true' >/dev/null \
    || { echo "cache bridge smoke failed: ${response}" >&2; exit 1; }

echo "cache bridge smoke passed"
