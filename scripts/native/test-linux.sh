#!/usr/bin/env bash
#
# Run the Classic-mode and ionCube smoke tests on the native Linux
# architecture. Usage: test-linux.sh <amd64|arm64>

set -Eeuo pipefail

if [[ $# -ne 1 ]] || [[ "$1" != "amd64" && "$1" != "arm64" ]]; then
    echo "usage: test-linux.sh <amd64|arm64>" >&2
    exit 2
fi

export POC_ARCH="$1"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/poc/common.sh"

if ! docker image inspect "${POC_IMAGE}" >/dev/null 2>&1; then
    build_poc_image
fi

readonly container="zentao-runtime-native-${POC_ARCH}-${RANDOM}-$$"
readonly synthetic_dir="$(mktemp -d)"
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${synthetic_dir}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${synthetic_dir}" 2>/dev/null || true
}
trap cleanup EXIT

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${repo_root}/tests/fixtures/php:/app:ro" \
    --volume "${repo_root}/tests/fixtures/runtime:/runtime-config:ro" \
    "${POC_IMAGE}" \
    -c 'cp /runtime-config/runtime.json /tmp/runtime.json && exec zentao-runtime serve --config /tmp/runtime.json' >/dev/null

for _ in $(seq 1 30); do
    if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/healthz >/dev/null; then
        break
    fi
    sleep 1
done

docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness \
    | jq --exit-status '.status == "ready"' >/dev/null
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/ \
    | jq --exit-status '.zts == true and .ioncube == true and (.php | startswith("8.4."))' >/dev/null
docker exec "${container}" php --ri 'ionCube Loader' | grep -F 'ionCube Loader'
docker exec "${container}" zentao-runtime version \
    | jq --exit-status '.mode == "classic" and .php_zts == true and .zend_signals == false and .duckdb == "v2.10505.0"' >/dev/null

mkdir -p "${synthetic_dir}/open"
"${repo_root}/scripts/ci/make-synthetic-app-package.sh" "${synthetic_dir}/open" >/dev/null
"${repo_root}/tests/e2e/access-log-smoke.sh" "${synthetic_dir}/open"
"${repo_root}/tests/e2e/jsonl-parquet-smoke.sh"

echo "Linux ${POC_ARCH} smoke tests passed"
