#!/usr/bin/env bash
#
# Multi-application routing smoke test for the pluggable Runtime.
#
# Starts one Runtime instance with two applications (root application plus a
# sub-path application) and verifies prefix routing, per-application
# environment injection and readiness aggregation.
#
# Usage:
#   multi-app-smoke.sh [app-package-a] [app-package-b]
#
# Without package arguments two synthetic applications are generated. Real
# packages are staged through scripts/ci/stage-app-package.sh.
#
# Environment:
#   ZENTAO_RUNTIME_BIN  Runtime binary (default: dist/runtime-linux-amd64/bin/zentao)
#   ZENTAO_SMOKE_PORT   listen port (default: 18080)

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly runtime_bin="${ZENTAO_RUNTIME_BIN:-${repo_root}/dist/runtime-linux-amd64/bin/zentao}"
readonly port="${ZENTAO_SMOKE_PORT:-18080}"
readonly app_a_input="${1:-}"
readonly app_b_input="${2:-}"

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

mkdir -p "${work}/config" "${work}/apps"
cp -a "${stage}/config/php.ini" "${work}/config/php.ini"
if [[ -d "${stage}/config/conf.d" ]]; then
    cp -a "${stage}/config/conf.d" "${work}/config/conf.d"
fi

make_synthetic_app() {
    local slot="$1" name="$2"
    mkdir -p "${work}/apps/${slot}/www"
    cat > "${work}/apps/${slot}/www/index.php" <<PHP
<?php
header('Content-Type: application/json');
echo json_encode(array(
    'app'             => getenv('ZT_APP_ID'),
    'webRoot'         => getenv('ZT_WEB_ROOT'),
    'requestUri'      => \$_SERVER['REQUEST_URI'] ?? null,
    'scriptName'      => \$_SERVER['SCRIPT_NAME'] ?? null,
    'pathInfo'        => \$_SERVER['PATH_INFO'] ?? null,
    'phpSelf'         => \$_SERVER['PHP_SELF'] ?? null,
    'documentRoot'    => \$_SERVER['DOCUMENT_ROOT'] ?? null,
    'dataRoot'        => getenv('ZT_DATA_ROOT'),
    'tmpRoot'         => getenv('ZT_TMP_ROOT'),
    'sessionPath'     => getenv('ZT_SESSION_PATH'),
    'apiSessionPath'  => getenv('ZT_APISESSION_PATH'),
    'dbName'          => getenv('ZT_DB_NAME'),
    'expected'        => '${name}',
));
PHP
    cat > "${work}/apps/${slot}/www/health.php" <<'PHP'
<?php
header('Content-Type: application/json');
echo '{"status":"ok"}';
PHP
}

if [[ -n "${app_a_input}" ]]; then
    "${repo_root}/scripts/ci/stage-app-package.sh" "${app_a_input}" "${work}" apps/a >/dev/null
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${work}/apps/a" >/dev/null 2>&1 || true
else
    make_synthetic_app a app-a
fi
if [[ -n "${app_b_input}" ]]; then
    "${repo_root}/scripts/ci/stage-app-package.sh" "${app_b_input}" "${work}" apps/b >/dev/null
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${work}/apps/b" >/dev/null 2>&1 || true
else
    make_synthetic_app b app-b
fi

cat > "${work}/config/runtime.toml" <<EOF
schemaVersion = 1

[runtime]
controlSocket = "run/runtime.sock"
pidFile = "run/runtime.pid"
drainTimeout = "5s"
logPath = "logs/runtime.log"

[web]
listen = "127.0.0.1:${port}"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[queue]
enabled = false

[[apps]]
id = "app-a"
path = "/"
dir = "apps/a"
webRoot = "www"
env = { ZT_DB_NAME = "smoke_a" }

[[apps]]
id = "app-b"
path = "/ipd"
dir = "apps/b"
webRoot = "www"
env = { ZT_DB_NAME = "smoke_b" }
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

fail=0
check() {
    local description="$1" url="$2" expect="$3"
    local body
    body="$(curl --silent --max-time 5 "${url}")"
    if [[ "${body}" != *"${expect}"* ]]; then
        echo "FAIL: ${description}" >&2
        echo "  url:      ${url}" >&2
        echo "  expected: ${expect}" >&2
        echo "  body:     ${body}" >&2
        fail=1
    else
        echo "ok: ${description}"
    fi
}

if [[ -n "${app_a_input}" || -n "${app_b_input}" ]]; then
    # Real application packages: verify that both applications answer through
    # their own prefix without asserting on application-specific HTML.
    status_a="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 10 "${base}/")"
    status_b="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 10 "${base}/ipd/")"
    for pair in "root:${status_a}" "sub-path:${status_b}"; do
        name="${pair%%:*}"
        code="${pair##*:}"
        case "${code}" in
            200|301|302|303) echo "ok: ${name} application answered (HTTP ${code})" ;;
            *) echo "FAIL: ${name} application answered HTTP ${code}" >&2; fail=1 ;;
        esac
    done
    if [[ "${status_a}" == "${status_b}" ]]; then
        echo "note: both applications returned HTTP ${status_a}; verify content manually" >&2
    fi
    location_a="$(curl --silent --head --max-time 10 "${base}/" | awk 'tolower($1) == "location:" {print $2}' | tr -d '\r')"
    location_b="$(curl --silent --head --max-time 10 "${base}/ipd/" | awk 'tolower($1) == "location:" {print $2}' | tr -d '\r')"
    echo "note: root application location: ${location_a:-<none>}"
    echo "note: sub-path application location: ${location_b:-<none>}"
    if [[ -n "${location_b}" && "${location_b}" == /* && "${location_b}" != /ipd* ]]; then
        echo "FAIL: sub-path application generated a URL outside its prefix: ${location_b}" >&2
        fail=1
    fi
    install_html="$(curl --silent --max-time 15 "${base}/ipd/install.php")"
    if [[ "${install_html}" == *'/ipd/'* ]]; then
        echo "ok: sub-path application renders prefix-aware URLs"
    else
        echo "note: sub-path install page has no /ipd/ references; inspect manually"
    fi
    check "readiness" "${base}/_runtime/readiness" '"status":"ready"'
    if [[ "${fail}" == "1" ]]; then
        echo "multi-app smoke failed" >&2
        exit 1
    fi
    echo "multi-app smoke passed with real packages"
    exit 0
fi

check "root application routing" "${base}/" '"app":"app-a"'
check "root application environment" "${base}/" '"dbName":"smoke_a"'
check "sub-path application routing" "${base}/ipd/" '"app":"app-b"'
check "sub-path application environment" "${base}/ipd/" '"dbName":"smoke_b"'
check "sub-path web root injection" "${base}/ipd/" '"webRoot":"ipd"'
check "sub-path PATH_INFO routing" "${base}/ipd/index.php/foo/bar" '"pathInfo":"\/foo\/bar"'
check "sub-path request uri keeps prefix" "${base}/ipd/index.php/foo/bar" '"requestUri":"\/ipd\/index.php\/foo\/bar"'
check "sub-path data root injection" "${base}/ipd/" '"dataRoot"'
check "sub-path session root injection" "${base}/ipd/" 'apisession'

check "sub-path request uri keeps prefix" "${base}/ipd/" '"requestUri":"\/ipd\/"'

if [[ "${fail}" == "1" ]]; then
    echo "multi-app smoke failed" >&2
    exit 1
fi
echo "multi-app smoke passed"
