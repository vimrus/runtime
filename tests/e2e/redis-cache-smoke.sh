#!/usr/bin/env bash
#
# Verify the PHP Redis cache backend against a real Redis server from inside
# the runtime image: shared set/get, namespace version invalidation and delete.
#
# Requires Docker. An existing Redis may be provided with ZENTAO_REDIS_HOST /
# ZENTAO_REDIS_PORT, otherwise a temporary redis:7-alpine container is started.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly service_src="${ZENTAO_SERVICE_SRC:-$(dirname "${repo_root}")/zentaopms/service}"
readonly work="$(mktemp -d)"
readonly container="zentao-redis-cache-${RANDOM}-$$"
readonly redis_container="zentao-redis-server-${RANDOM}-$$"
redis_started=0

cleanup() {
    if [[ "${redis_started}" == "1" ]]; then
        docker rm --force "${redis_container}" >/dev/null 2>&1 || true
    fi
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

readonly redis_host="${ZENTAO_REDIS_HOST:-127.0.0.1}"
readonly redis_port="${ZENTAO_REDIS_PORT:-$(shuf -i 20000-30000 -n 1)}"
if [[ -z "${ZENTAO_REDIS_HOST:-}" ]]; then
    redis_started=1
    docker run --detach --name "${redis_container}" -p "${redis_port}:6379" redis:7-alpine >/dev/null
    for _ in $(seq 1 30); do
        if docker exec "${redis_container}" redis-cli ping 2>/dev/null | grep -q PONG; then
            break
        fi
        sleep 1
    done
fi

mkdir -p "${work}/service"
cp -r "${service_src}/." "${work}/service/"
cp "${repo_root}/tests/fixtures/php/redis.php" "${work}/redis.php"

response="$(docker run --rm --name "${container}" \
    --network host \
    --entrypoint sh \
    --env ZT_REDIS_HOST="${redis_host}" \
    --env ZT_REDIS_PORT="${redis_port}" \
    --env ZENTAO_SERVICE_ROOT=/work/service \
    --volume "${work}:/work:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /tmp/php.ini && /opt/zentao/php/bin/php -c /tmp/php.ini /work/redis.php')"
echo "${response}" | jq -e '
    .set == true and
    .get.a == 1 and
    .get.b == "text" and
    .miss == null and
    .shared_get.a == 1 and
    .after_clear == null and
    .re_get == "ok" and
    .final_delete == true' >/dev/null \
    || { echo "redis cache smoke failed: ${response}" >&2; exit 1; }

echo "redis cache smoke passed"
