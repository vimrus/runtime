#!/usr/bin/env bash
#
# End-to-end smoke test for Runtime-managed resident PHP processes.
#
# Usage:
#   process-smoke.sh
#
# Environment:
#   ZENTAO_RUNTIME_BIN  Runtime binary
#                       (default: dist/runtime-linux-amd64/bin/zentao)
#   ZENTAO_SMOKE_PORT   listen port (default: 18084)

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly runtime_bin="${ZENTAO_RUNTIME_BIN:-${repo_root}/dist/runtime-linux-amd64/bin/zentao}"
readonly port="${ZENTAO_SMOKE_PORT:-18084}"

if [[ ! -x "${runtime_bin}" ]]; then
    echo "runtime binary not found or not executable: ${runtime_bin}" >&2
    echo "build it first with scripts/native/build-linux.sh" >&2
    exit 1
fi

readonly stage="$(cd "$(dirname "${runtime_bin}")/.." && pwd)"
work="$(mktemp -d)"
pid=""
cleanup() {
    if [[ -n "${pid}" ]]; then
        kill "${pid}" >/dev/null 2>&1 || true
        wait "${pid}" 2>/dev/null || true
    fi
    rm -rf "${work}"
}
trap cleanup EXIT

mkdir -p \
    "${work}/config" \
    "${work}/lib/php" \
    "${work}/apps/zentao/www" \
    "${work}/apps/zentao/service"
ln -s "${stage}/php" "${work}/php"
cp -a "${stage}/config/php.ini" "${work}/config/php.ini"
if [[ -d "${stage}/config/conf.d" ]]; then
    cp -a "${stage}/config/conf.d" "${work}/config/conf.d"
fi
cp "${stage}/lib/php/runtime-process.php" "${work}/lib/php/runtime-process.php"

cat > "${work}/apps/zentao/service/worker.php" <<'PHP'
<?php
require getenv('ZENTAO_PROCESS_SDK');

$worker = RuntimeProcessWorker::fromEnvironment();
$worker->run(function (array $request): array {
    if(($request['method'] ?? '') !== 'echo') {
        throw new RuntimeProcessMethodNotFound();
    }
    return ['value' => $request['payload']['value'] ?? null];
});
PHP

cat > "${work}/apps/zentao/www/process.php" <<'PHP'
<?php
header('Content-Type: application/json');
require getenv('ZENTAO_PROCESS_SDK');
$client = RuntimeProcessClient::forCurrentApp();
echo json_encode(
    $client->call('event-worker', 'echo', ['value' => 'from-web'], 3000),
    JSON_THROW_ON_ERROR
);
PHP

cat > "${work}/apps/zentao/www/health.php" <<'PHP'
<?php
header('Content-Type: application/json');
echo '{"status":"ok"}';
PHP

cat > "${work}/config/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "run/runtime.sock"
pidFile = "run/runtime.pid"
drainTimeout = "10s"
logPath = "logs/runtime.log"

[web]
listen = "127.0.0.1:${port}"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[queue]
enabled = false

[observability]
enabled = false

[olap]
enabled = false

[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
enabled = true

  [[apps.processes]]
  name = "event-worker"
  script = "service/worker.php"
  required = true
  readyTimeout = "8s"
  requestTimeout = "3s"
  stopTimeout = "5s"
  maxPending = 4
EOF

"${runtime_bin}" serve --install-root "${work}" >"${work}/serve.log" 2>&1 &
pid=$!

base="http://127.0.0.1:${port}"
ready=0
for _ in $(seq 1 60); do
    if curl --fail --silent --max-time 2 "${base}/_runtime/readiness" >/dev/null 2>&1; then
        ready=1
        break
    fi
    if ! kill -0 "${pid}" >/dev/null 2>&1; then
        echo "runtime exited during startup" >&2
        cat "${work}/serve.log" >&2
        exit 1
    fi
    sleep 0.5
done
if [[ "${ready}" != "1" ]]; then
    echo "runtime did not become ready" >&2
    cat "${work}/serve.log" >&2
    exit 1
fi

body="$(curl --fail --silent --max-time 5 "${base}/process.php")"
if [[ "${body}" != '{"value":"from-web"}' ]]; then
    echo "unexpected process response: ${body}" >&2
    cat "${work}/serve.log" >&2
    exit 1
fi

status="$("${runtime_bin}" processes --control-socket "${work}/run/runtime.sock")"
if [[ "${status}" != *'"state":"ready"'* || "${status}" != *'"name":"event-worker"'* ]]; then
    echo "unexpected process status: ${status}" >&2
    exit 1
fi
old_instance="$(jq -r '.[0].instanceId' <<<"${status}")"

"${runtime_bin}" process-restart \
    --app zentao \
    --process event-worker \
    --control-socket "${work}/run/runtime.sock" >/dev/null

for _ in $(seq 1 60); do
    status="$("${runtime_bin}" processes --control-socket "${work}/run/runtime.sock")"
    instance="$(jq -r '.[0].instanceId // ""' <<<"${status}")"
    if [[ "${status}" == *'"state":"ready"'* && -n "${instance}" && "${instance}" != "${old_instance}" ]]; then
        break
    fi
    sleep 0.2
done
instance="$(jq -r '.[0].instanceId // ""' <<<"${status}")"
if [[ "${status}" != *'"state":"ready"'* || -z "${instance}" || "${instance}" == "${old_instance}" ]]; then
    echo "process did not recover after restart: ${status}" >&2
    exit 1
fi

body="$(curl --fail --silent --max-time 5 "${base}/process.php")"
if [[ "${body}" != '{"value":"from-web"}' ]]; then
    echo "process call failed after restart: ${body}" >&2
    exit 1
fi

echo "resident process smoke passed"
