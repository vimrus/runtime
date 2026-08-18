#!/usr/bin/env bash

set -Eeuo pipefail

export POC_ARCH=amd64
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"

if ! docker image inspect "${POC_IMAGE}" >/dev/null 2>&1; then
    build_poc_image
fi

readonly container="zentao-runtime-poc-${RANDOM}-$$"
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${REPO_ROOT}/tests/fixtures/php:/app:ro" \
    --volume "${REPO_ROOT}/tests/fixtures/runtime:/runtime-config:ro" \
    "${POC_IMAGE}" \
    -c 'cp /runtime-config/runtime.json /tmp/runtime.json && exec zentao-runtime serve --config /tmp/runtime.json' >/dev/null

for _ in $(seq 1 30); do
    if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/healthz >/dev/null; then
        break
    fi
    sleep 1
done

docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/healthz \
    | jq --exit-status '.status == "ok"' >/dev/null
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/liveness \
    | jq --exit-status '.status == "ok"' >/dev/null
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness \
    | jq --exit-status '.status == "ready"' >/dev/null
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/ \
    | jq --exit-status '.zts == true and .ioncube == true and (.php | startswith("8.4."))' >/dev/null

for _ in 1 2 3; do
    docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/state.php \
        | jq --exit-status '.counter == 1' >/dev/null
done

docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/path.php/extra \
    | jq --exit-status '.pathInfo == "/extra"' >/dev/null
docker exec "${container}" sh -c 'printf zentao-poc > /tmp/upload.txt && curl --fail --silent -F payload=@/tmp/upload.txt http://127.0.0.1:8080/upload.php' \
    | jq --exit-status '.name == "upload.txt" and .size == 10 and .error == 0' >/dev/null

docker exec "${container}" stat -c '%a' /tmp/zentao-runtime.sock | grep -Fx '600'
docker exec "${container}" zentao-runtime status --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.lifecycle.state == "ready" and .web.root == "/app"' >/dev/null
docker exec "${container}" zentao-runtime health --deep --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.live == true and .ready == true and .deep == "ok"' >/dev/null
docker exec "${container}" zentao-runtime health --deep --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.components[] | select(.name == "observability") | .status == "ok"' >/dev/null
docker exec "${container}" sed -i 's/"idleTimeout": "30s"/"idleTimeout": "31s"/' /tmp/runtime.json
docker exec "${container}" zentao-runtime reload --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.reloaded == true and .restartRequired == false' >/dev/null
docker exec "${container}" sed -i 's/"threads": 4/"threads": 5/' /tmp/runtime.json
docker exec "${container}" zentao-runtime reload --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.reloaded == false and .restartRequired == true' >/dev/null
docker exec "${container}" zentao-runtime health --deep --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.live == true and .ready == true and .deep == "ok"' >/dev/null
docker exec "${container}" zentao-runtime version \
    | jq --exit-status '.mode == "classic" and .php_zts == true and .zend_signals == false and .duckdb == "v2.10505.0"' >/dev/null
docker exec "${container}" zentao-runtime php-cli -- -r 'echo PHP_VERSION;' \
    | grep -E '^8\.4\.' >/dev/null
docker exec "${container}" zentao-runtime php-cli -- -r 'exit(extension_loaded("ionCube Loader") ? 0 : 1);'
docker exec "${container}" zentao-runtime flush-observability --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.dropped == 0' >/dev/null
docker exec "${container}" zentao-runtime logs --since 5m --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status 'length >= 1 and .[0].message == "runtime started"' >/dev/null
docker exec "${container}" zentao-runtime clean-observability --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.removedFiles >= 0 and .deletedDirs >= 0' >/dev/null
docker exec "${container}" zentao-runtime collect-logs --control-socket /tmp/zentao-runtime.sock \
    | jq -r '.path' > /tmp/collect-path.txt
docker exec "${container}" sh -c "tar -tzf $(cat /tmp/collect-path.txt) | grep -F 'summary.json'" >/dev/null

docker exec "${container}" php --ri 'ionCube Loader' | grep -F 'ionCube Loader'

for removed in \
    /opt/zentao/php/bin/phpdbg \
    /opt/zentao/php/bin/phpize \
    /opt/zentao/php/bin/php-config \
    /opt/zentao/php/include \
    /opt/zentao/php/lib/php/build; do
    if docker exec "${container}" test -e "${removed}"; then
        echo "development-only file was not trimmed: ${removed}" >&2
        exit 1
    fi
done

if [[ -n "${ZENTAO_POC_APP_DIR:-}" ]]; then
    if [[ ! -f "${ZENTAO_POC_APP_DIR}/extension/ipd/budget/model.php" ]]; then
        echo "invalid ZENTAO_POC_APP_DIR: encrypted IPD probe is missing" >&2
        exit 1
    fi
    docker run --rm --entrypoint /opt/zentao/php/bin/php \
        --volume "${ZENTAO_POC_APP_DIR}:/zentaoipd:ro" \
        "${POC_IMAGE}" \
        -l /zentaoipd/extension/ipd/budget/model.php \
        | grep -F 'No syntax errors detected'
fi

docker exec "${container}" zentao-runtime stop --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.result == "stopping"' >/dev/null

echo "Linux amd64 PoC tests passed"
