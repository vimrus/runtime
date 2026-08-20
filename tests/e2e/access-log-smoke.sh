#!/usr/bin/env bash
#
# Verify automatic request logging: every request should produce a JSON
# access log entry with URL, response time, status code, and (on errors) the
# error message.
#
# Usage: access-log-smoke.sh <app-package|app-dir>

set -Eeuo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: access-log-smoke.sh <app-package|app-dir>" >&2
    exit 2
fi

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/poc/common.sh"

readonly app_input="$1"
readonly work="$(mktemp -d)"
readonly container="zentao-access-${RANDOM}-$$"
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${app_input}" "${work}/app")"
staged_release="$(dirname "${web_root}")"
if [[ -f "${staged_release}/www/index.php" ]]; then
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${staged_release}" >/dev/null || true
fi

cat > "${work}/runtime.json" <<EOF
{
  "schemaVersion": 1,
  "runtime": {
    "controlSocket": "/tmp/zentao.sock",
    "pidFile": "/tmp/zentao.pid",
    "drainTimeout": "10s",
    "logPath": "/shared/logs/runtime.log"
  },
  "web": {
    "root": "/shared/app/current/www",
    "listen": "127.0.0.1:8080",
    "threads": 4,
    "readHeaderTimeout": "10s",
    "idleTimeout": "30s",
    "maxHeaderBytes": 16384
  }
}
EOF

docker run --detach --name "${container}" \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/runtime.json:/tmp/runtime.json:ro" \
    "${POC_IMAGE}" \
    -c 'mkdir -p /shared/logs && exec zentao-runtime serve --config /tmp/runtime.json --install-root /shared/app' >/dev/null

ready=0
for _ in $(seq 1 30); do
    if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 1
done
[[ "${ready}" == "1" ]]

docker exec "${container}" curl --silent -o /dev/null "http://127.0.0.1:8080/index.php?x=1"
docker exec "${container}" curl --silent -o /dev/null "http://127.0.0.1:8080/not-found.php"
docker exec "${container}" curl --silent -o /dev/null "http://127.0.0.1:8080/index.php?fatal=1"
sleep 1

node_id="$(docker exec "${container}" cat /shared/app/data/node-id)"
access_log="/shared/logs/access-${node_id}.jsonl"
docker exec "${container}" sh -c "grep -c 'http.log.access' '${access_log}' >/dev/null" \
    || { echo "no access log entries found" >&2; exit 1; }
docker exec "${container}" cat "${access_log}" \
    | jq -e 'select(.logger | contains("http.log.access")) | (.request.uri != null) and (.status != null) and (.duration != null)' >/dev/null \
    || { echo "access log entries missing uri/status/duration" >&2; exit 1; }
docker exec "${container}" cat "${access_log}" \
    | jq -e 'select(.logger | contains("http.log.access")) | select(.request.uri == "/index.php?fatal=1") | .status == 500' >/dev/null \
    || { echo "fatal request must be logged with status 500" >&2; exit 1; }
docker exec "${container}" cat /opt/zentao/logs/php-error.log 2>/dev/null \
    | grep -q "synthetic fatal" \
    || { echo "PHP error message missing from php-error.log" >&2; exit 1; }

echo "access log smoke passed"
