#!/usr/bin/env bash
#
# Joint smoke test for an externally produced ZenTao application package:
# stage it into the Runtime layout, start the Runtime, and verify PHP
# execution and the install entry point. Database install/login flows are
# covered by the joint matrix once a database is provided.
#
# Usage: zentao-app-smoke.sh <app-package|app-dir> [install-root]

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: zentao-app-smoke.sh <app-package|app-dir> [install-root]" >&2
    exit 2
fi

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="$1"
readonly work="$(mktemp -d)"
readonly container="zentao-app-${RANDOM}-$$"
readonly entries_raw="${ZENTAO_APP_ENTRIES:-index.php}"
readonly allowed_statuses_raw="${ZENTAO_APP_ALLOWED_STATUSES:-200,301,302,405}"
IFS=',' read -r -a entries <<<"${entries_raw}"
IFS=',' read -r -a allowed_statuses <<<"${allowed_statuses_raw}"
for entry in "${entries[@]}"; do
    if [[ ! "${entry}" =~ ^(index|api|bridge)\.php$ ]]; then
        echo "unsupported application entry: ${entry}" >&2
        exit 2
    fi
done
for status_code in "${allowed_statuses[@]}"; do
    if [[ ! "${status_code}" =~ ^[1-5][0-9][0-9]$ ]]; then
        echo "application allowed status is invalid: ${status_code}" >&2
        exit 2
    fi
done
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${app_input}" "${work}/app")"
staged_release="$(dirname "${web_root}")"
if [[ -f "${staged_release}/www/index.php" ]]; then
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${staged_release}" >/dev/null
fi

cat > "${work}/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"

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

# This smoke test verifies PHP hosting only. Its staged application need not
# expose the private Queue Bridge, so do not inherit the production default.
[queue]
enabled = false
EOF

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/runtime.toml:/runtime-config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'mkdir -p /shared/config && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

ready=0
for _ in $(seq 1 30); do
    if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 1
done
[[ "${ready}" == "1" ]]

docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness \
    | jq --exit-status '.status == "ready"' >/dev/null
docker exec "${container}" sh -c 'test -f /shared/app/zentaopms/www/install.php'
status_allowed() {
    local candidate="$1" allowed
    for allowed in "${allowed_statuses[@]}"; do
        [[ "${candidate}" == "${allowed}" ]] && return 0
    done
    return 1
}

# PHP executes through FrankenPHP Classic for every configured application entry.
for entry in "${entries[@]}"; do
    path="/"
    [[ "${entry}" != "index.php" ]] && path="/${entry}"
    code="$(docker exec "${container}" curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:8080${path}")"
    if ! status_allowed "${code}"; then
        echo "unexpected ${entry} status code at ${path}: ${code}" >&2
        exit 1
    fi
done

echo "ZenTao application smoke passed: ${web_root}"
