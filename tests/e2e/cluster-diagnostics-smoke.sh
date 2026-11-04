#!/usr/bin/env bash
#
# Verify multi-node diagnostics never block readiness, business routes or the
# Queue, while the private Profile bridge still enforces authentication and
# never writes its token to the access log.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly work="$(mktemp -d)"
readonly container="zentao-cluster-${RANDOM}-$$"
cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "${work}/app/www" "${work}/config" "${work}/data" "${work}/logs"
printf 'static-ok\n' >"${work}/app/www/static.txt"
printf 'mismatch\n' >"${work}/mode"

cat >"${work}/app/www/index.php" <<'PHP'
<?php
$mode = trim((string)file_get_contents('/shared/mode'));
if(($_GET['m'] ?? '') === 'runtime' && ($_GET['f'] ?? '') === 'profile')
{
    if(($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST')
    {
        http_response_code(405);
        header('Allow: POST');
        header('Content-Type: application/json');
        echo '{"error":{"code":"invalid_request"}}';
        exit;
    }

    $remote = $_SERVER['REMOTE_ADDR'] ?? '';
    $token  = $_SERVER['HTTP_X_ZENTAO_RUNTIME_PROFILE_TOKEN'] ?? '';
    $schema = $_SERVER['HTTP_X_ZENTAO_RUNTIME_PROFILE_SCHEMA'] ?? '';
    if(!in_array($remote, array('127.0.0.1', '::1'), true) ||
       $token === '' || !hash_equals((string)getenv('ZENTAO_RUNTIME_PROFILE_TOKEN'), $token) ||
       $schema !== (string)getenv('ZENTAO_RUNTIME_PROFILE_SCHEMA'))
    {
        http_response_code(401);
        header('Content-Type: application/json');
        echo '{"error":{"code":"unauthenticated"}}';
        exit;
    }

    file_put_contents('/shared/profile-token-seen', $token);
    header('Content-Type: application/json');
    if($mode === 'ready') echo '{"schemaVersion":1,"status":"ready","profileGeneration":1,"retryAfterSeconds":1}';
    else echo '{"schemaVersion":1,"status":"mismatch","profileGeneration":1,"mismatchDomains":["app.database"],"retryAfterSeconds":1}';
    exit;
}

header('Content-Type: text/plain');
echo "business-ok\n";
PHP

cat >"${work}/config/runtime.toml" <<'TOML'
schemaVersion = 1

[runtime]
nodeID = "node-a"
logPath = "/shared/logs/runtime.log"
controlSocket = "/shared/run/runtime.sock"
pidFile = "/shared/run/runtime.pid"
drainTimeout = "5s"

[web]
listen = "0.0.0.0:8080"
threads = 2
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[[apps]]
id = "zentao"
path = "/"
dir = "/shared/app"
webRoot = "www"
dataDir = "/shared/data"
tmpDir = "/shared/tmp"
enabled = true

[queue]
enabled = false

[observability]
enabled = false

[olap]
enabled = false
TOML

docker run --detach --name "${container}" \
    --volume "${work}:/shared:rw" \
    --volume "${work}/config/runtime.toml:/opt/zentao/config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" serve >/dev/null

http_status() {
    docker exec "${container}" curl --silent --output /dev/null --write-out '%{http_code}' "$1" 2>/dev/null || true
}

wait_status() {
    local url="$1"
    local expected="$2"
    local attempts="${3:-30}"
    local status=""
    for _ in $(seq 1 "${attempts}"); do
        status="$(http_status "${url}")"
        [[ "${status}" == "${expected}" ]] && return 0
        sleep 1
    done
    echo "expected ${url} to return ${expected}, got ${status}" >&2
    docker logs "${container}" >&2 || true
    return 1
}

wait_status "http://127.0.0.1:8080/_runtime/readiness" "200"
wait_status "http://127.0.0.1:8080/index.php?m=my&f=index" "200"
wait_status "http://127.0.0.1:8080/static.txt" "200"

profile_get="$(http_status "http://127.0.0.1:8080/index.php?m=runtime&f=profile")"
[[ "${profile_get}" == "405" ]] || {
    echo "GET Profile request must reach PHP and return 405, got ${profile_get}" >&2
    exit 1
}

bad_auth="$(docker exec "${container}" curl --silent --output /dev/null --write-out '%{http_code}' \
    -X POST -H 'X-Zentao-Runtime-Profile-Token: invalid' \
    -H 'X-Zentao-Runtime-Profile-Schema: 1' \
    'http://127.0.0.1:8080/index.php?m=runtime&f=profile')"
[[ "${bad_auth}" == "401" ]] || {
    echo "invalid Profile token must return 401, got ${bad_auth}" >&2
    exit 1
}

for _ in $(seq 1 10); do
    [[ -s "${work}/profile-token-seen" ]] && break
    sleep 1
done
[[ -s "${work}/profile-token-seen" ]] || {
    echo "Runtime did not send a Profile token" >&2
    exit 1
}

printf 'ready\n' >"${work}/mode"
wait_status "http://127.0.0.1:8080/_runtime/readiness" "200"
wait_status "http://127.0.0.1:8080/index.php?m=my&f=index" "200"

access_log="${work}/logs/access-node-a.jsonl"
for _ in $(seq 1 10); do
    [[ -s "${access_log}" ]] && break
    sleep 1
done
[[ -s "${access_log}" ]] || {
    echo "access log was not created" >&2
    exit 1
}

if docker exec "${container}" grep -q 'X-Zentao-Runtime-Profile-Token' /shared/logs/access-node-a.jsonl; then
    echo "Profile token header leaked into access log" >&2
    exit 1
fi
if docker exec "${container}" grep -Fq "$(cat "${work}/profile-token-seen")" /shared/logs/access-node-a.jsonl; then
    echo "Profile token value leaked into access log" >&2
    exit 1
fi

echo "cluster diagnostics smoke passed"
