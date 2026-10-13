#!/usr/bin/env bash
#
# Queue direct-handler joint smoke: a real ZenTao application, a real MySQL
# database and the Runtime queue engine claim/execute a direct handler task
# (`queue.pruneFinished`) to completion.
#
# Usage:
#   queue-direct-smoke.sh <app-package|app-dir>
# Env:
#   ZENTAO_MYSQL_HOST/PORT/USER/PASSWORD  default 127.0.0.1:3306/root/123456
#   ZENTAO_RUNTIME_IMAGE                  default runtime image from common.sh

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/build/common.sh"

readonly app_input="${1:?usage: queue-direct-smoke.sh <app-package|app-dir>}"
readonly mysql_host="${ZENTAO_MYSQL_HOST:-127.0.0.1}"
readonly mysql_port="${ZENTAO_MYSQL_PORT:-3306}"
readonly mysql_user="${ZENTAO_MYSQL_USER:-root}"
readonly mysql_password="${ZENTAO_MYSQL_PASSWORD:-123456}"
readonly mysql_args=(-h"${mysql_host}" -P"${mysql_port}" -u"${mysql_user}" -p"${mysql_password}")

readonly work="$(mktemp -d)"
readonly db_name="zentao_queue_e2e_${RANDOM}_$$"
readonly container="zentao-queue-smoke-${RANDOM}-$$"
readonly task_uuid="22222222-3333-4444-5555-666666666666"
readonly task_key="queue-direct-smoke-${RANDOM}-$$"

cleanup() {
    docker rm --force "${container}" >/dev/null 2>&1 || true
    mysql "${mysql_args[@]}" -e "DROP DATABASE IF EXISTS \`${db_name}\`" >/dev/null 2>&1 || true
    find "${work}" -mindepth 1 -delete 2>/dev/null || true
    rmdir "${work}" 2>/dev/null || true
}
trap cleanup EXIT

mysql "${mysql_args[@]}" -e "CREATE DATABASE \`${db_name}\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"

# Stage the application. Real working directories may contain .git/tmp, which
# are too large for a container volume; package them first.
if [[ -d "${app_input}" && -d "${app_input}/.git" ]]; then
    package="${work}/app.tar.gz"
    tar -C "${app_input}" --exclude=.git --exclude=tmp --exclude=www/data -czf "${package}" .
    app_source="${package}"
else
    app_source="${app_input}"
fi
web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${app_source}" "${work}/app")"
release_root="$(dirname "${web_root}")"
"${repo_root}/scripts/ci/patch-classic-mode.sh" "${release_root}" >/dev/null

# Install-time artifacts must be absent for a normal installed application.
rm -f "${web_root}/install.php" "${web_root}/upgrade.php"

app_version="$(tr -d '[:space:]' < "${release_root}/VERSION")"
mysql "${mysql_args[@]}" "${db_name}" < <(sed "s/__DATABASE__/${db_name}/g" "${release_root}/db/zentao.sql")
mysql "${mysql_args[@]}" "${db_name}" <<SQL
INSERT INTO zt_company (id, name, phone, fax, address, zipcode, website, backyard, guest, admins, deleted)
VALUES (1, '队列E2E公司', '', '', '', '', '', '', 0, ',admin,', 0);
INSERT INTO zt_config (owner, module, section, \`key\`, value)
VALUES ('system', 'common', 'global', 'version', '${app_version}');
INSERT INTO zt_queue
  (uuid, channel, cron, handler, payload, status, priority, attempts, maxAttempts, availableDate, timeoutSeconds, idempotencyKey, traceID, cancelRequested, createdBy, createdDate, editedDate, version)
VALUES
  ('${task_uuid}', 'default', 0, 'queue.pruneFinished', '{}', 'queued', 0, 0, 3, NOW(), 60, '${task_key}', 'trace-${task_uuid}', 0, 'system', NOW(), NOW(), 1);
SQL

cat > "${release_root}/config/my.php" <<PHP
<?php
\$config->version         = '${app_version}';
\$config->installed       = true;
\$config->debug           = 0;
\$config->requestType     = 'PATH_INFO';
\$config->timezone        = 'Asia/Shanghai';
\$config->db->driver      = 'mysql';
\$config->db->host        = '${mysql_host}';
\$config->db->port        = '${mysql_port}';
\$config->db->name        = '${db_name}';
\$config->db->user        = '${mysql_user}';
\$config->db->encoding    = 'UTF8';
\$config->db->password    = '${mysql_password}';
\$config->db->prefix      = 'zt_';
\$config->webRoot         = getWebRoot();
\$config->default->lang   = 'zh-cn';
\$config->customSession   = true;
PHP

cat > "${work}/runtime.toml" <<'TOML'
schemaVersion = 1

[runtime]
controlSocket = "/tmp/zentao.sock"
pidFile = "/tmp/zentao.pid"
drainTimeout = "10s"

[web]
root = "/shared/app/zentaopms/www"
listen = "127.0.0.1:8080"
threads = 2
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[queue]
enabled = true
bridgeBaseURL = "http://127.0.0.1:8081/internal/runtime/queue/v1"
requestTimeout = "5s"
claimBatch = 8
leaseSeconds = 60
heartbeatInterval = "20s"
reapInterval = "5s"
reapBatch = 100
drainTimeout = "10s"

[[queue.workers]]
name = "default"
concurrency = 2
minPoll = "100ms"
maxPoll = "1s"

[cache]
enabled = false

[observability]
enabled = false
TOML

docker run --detach --name "${container}" \
    --network host \
    --entrypoint sh \
    --volume "${work}/app:/shared:rw" \
    --volume "${work}/runtime.toml:/runtime-config/runtime.toml:ro" \
    "${RUNTIME_IMAGE}" \
    -c 'mkdir -p /shared/config && cp /runtime-config/runtime.toml /shared/config/runtime.toml && sed "s#\${ZENTAO_RUNTIME_PHP_ROOT}#/opt/zentao#g" /opt/zentao/config/php.ini > /shared/config/php.ini && exec zentao serve --install-root /shared' >/dev/null

ready=0
for _ in $(seq 1 40); do
    if docker exec "${container}" curl --fail --silent http://127.0.0.1:8080/_runtime/readiness >/dev/null 2>&1; then
        ready=1
        break
    fi
    sleep 1
done
[[ "${ready}" == "1" ]]

completed=0
for _ in $(seq 1 60); do
    status="$(mysql -N "${mysql_args[@]}" "${db_name}" -e "SELECT status FROM zt_queue WHERE uuid='${task_uuid}'")"
    if [[ "${status}" == "succeeded" ]]; then
        completed=1
        break
    fi
    if [[ "${status}" == "failed" || "${status}" == "dead" ]]; then
        echo "queue task failed: status=${status}" >&2
        exit 1
    fi
    sleep 2
done
[[ "${completed}" == "1" ]]

deep_health="$(docker exec "${container}" zentao health --deep --control-socket /tmp/zentao.sock)"
echo "${deep_health}" | jq -e '.deep == "ok" and (.components[] | select(.name == "app") | .status == "ok")' >/dev/null \
    || { echo "deep health app probe failed: ${deep_health}" >&2; exit 1; }

echo "queue direct-handler smoke passed"
