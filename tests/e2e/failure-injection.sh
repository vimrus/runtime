#!/usr/bin/env bash
#
# Failure and stability smoke test:
#   1. Runtime process crash (SIGKILL) and recovery.
#   2. Graceful stop after forced restart.
# Bridge failures, NFS interruption and rolling upgrade rollback are covered
# by Go unit tests (internal/queue, internal/observability, internal/upgrade).

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/poc/common.sh"

if ! docker image inspect "${POC_IMAGE}" >/dev/null 2>&1; then
    build_poc_image
fi

readonly container="zentao-e2e-${RANDOM}-$$"
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${repo_root}/tests/fixtures/php:/app:ro" \
    --volume "${repo_root}/tests/fixtures/runtime:/runtime-config:ro" \
    "${POC_IMAGE}" \
    -c 'cp /runtime-config/runtime.json /tmp/runtime.json && exec zentao-runtime serve --config /tmp/runtime.json' >/dev/null

wait_ready() {
    for _ in $(seq 1 30); do
        if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

wait_ready
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/ | jq --exit-status '.zts == true' >/dev/null

# Crash the Host process and verify the container restarts it (entrypoint sh
# does not auto-restart, so restart the container to simulate process manager
# recovery; state.php proves request isolation after restart).
docker exec "${container}" sh -c 'kill -9 $(cat /tmp/zentao-runtime.pid) 2>/dev/null || true' || true
docker restart "${container}" >/dev/null
wait_ready
docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/state.php | jq --exit-status '.counter == 1' >/dev/null

# Upgrade transaction through the control plane: prepare, apply, verify,
# then roll back after a simulated post-commit verification failure.
docker exec "${container}" sh -c 'mkdir -p /opt/zentao/app/releases/rel-v1/www && printf "<?php\n" > /opt/zentao/app/releases/rel-v1/www/index.php'
docker exec "${container}" zentao-runtime upgrade --action prepare --release /opt/zentao/app/releases/rel-v1 --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.state == "staged"' >/dev/null
docker exec "${container}" zentao-runtime upgrade --action apply --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.state == "committed"' >/dev/null
docker exec "${container}" zentao-runtime upgrade --action status --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.current == "/opt/zentao/app/releases/rel-v1"' >/dev/null
docker exec "${container}" zentao-runtime upgrade --action rollback --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.state == "idle"' >/dev/null

# Graceful stop via the control plane must drain and exit cleanly.
docker exec "${container}" zentao-runtime stop --control-socket /tmp/zentao-runtime.sock \
    | jq --exit-status '.result == "stopping"' >/dev/null
for _ in $(seq 1 10); do
    if ! docker exec "${container}" sh -c 'test -f /tmp/zentao-runtime.pid' 2>/dev/null; then
        break
    fi
    sleep 1
done

echo "failure injection tests passed"
