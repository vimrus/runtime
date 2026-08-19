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
source "${repo_root}/scripts/poc/common.sh"

readonly app_input="$1"
readonly work="$(mktemp -d)"
readonly container="zentao-app-${RANDOM}-$$"
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

cat > "${work}/runtime.json" <<EOF
{
  "schemaVersion": 1,
  "runtime": {"controlSocket": "/tmp/zentao.sock", "pidFile": "/tmp/zentao.pid", "drainTimeout": "10s"},
  "web": {"root": "/shared/app/current/www", "listen": "127.0.0.1:8080", "threads": 4, "readHeaderTimeout": "10s", "idleTimeout": "30s", "maxHeaderBytes": 16384}
}
EOF

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/runtime.json:/tmp/runtime.json:ro" \
    "${POC_IMAGE}" \
    -c 'exec zentao-runtime serve --config /tmp/runtime.json --install-root /shared/app' >/dev/null

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
docker exec "${container}" sh -c 'test -f /shared/app/current/www/install.php'
# PHP executes through FrankenPHP Classic: index.php must return a PHP
# response (ZenTao typically redirects to install/login before DB setup).
code="$(docker exec "${container}" curl --silent --output /dev/null --write-out '%{http_code}' http://127.0.0.1:8080/)"
if [[ "${code}" != "200" && "${code}" != "301" && "${code}" != "302" ]]; then
    echo "unexpected index.php status code: ${code}" >&2
    exit 1
fi

echo "ZenTao application smoke passed: ${web_root}"
