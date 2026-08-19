#!/usr/bin/env bash
#
# Generate a runnable integrated ZenTao environment:
#   runtime + PHP + staged application + rendered config + start scripts
#
# The environment is self-contained under <output-dir> and can be started
# with ./run.sh (native) or docker compose up (container). ZenTao reads its
# database settings from ZT_DB_* environment variables (IN_CONTAINER=true),
# so no install wizard is required.
#
# Usage:
#   build-integrated-env.sh <open|biz|max|ipd> <output-dir>
#
# Env overrides:
#   ZENTAO_RUNTIME_STAGE       default: <repo>/dist/poc-linux-amd64
#   ZENTAO_APP_PACKAGES_DIR    default: <repo>/app-packages

set -Eeuo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: build-integrated-env.sh <open|biz|max|ipd> <output-dir>" >&2
    exit 2
fi

readonly edition="$1"
readonly output_dir="$2"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly runtime_stage="${ZENTAO_RUNTIME_STAGE:-${repo_root}/dist/poc-linux-amd64}"

case "${edition}" in
    open|biz|max|ipd) ;;
    *) echo "unknown edition: ${edition}" >&2; exit 2 ;;
esac

package="$("${repo_root}/scripts/ci/find-app-package.sh" "${edition}")"
if [[ -z "${package}" ]]; then
    echo "application package not found for edition ${edition}" >&2
    exit 1
fi

if [[ ! -x "${runtime_stage}/bin/zentao-runtime" ]]; then
    echo "runtime stage is missing bin/zentao-runtime: ${runtime_stage}" >&2
    echo "build it first with scripts/poc/build-linux-amd64.sh" >&2
    exit 1
fi

if [[ -e "${output_dir}" ]] && [[ -n "$(ls -A "${output_dir}" 2>/dev/null)" ]]; then
    echo "output directory must be empty or missing: ${output_dir}" >&2
    exit 1
fi
mkdir -p "${output_dir}"

cp -a "${runtime_stage}/." "${output_dir}/"
rm -f "${output_dir}/manifest.json" "${output_dir}/sbom.json"

staged_web_root="$("${repo_root}/scripts/ci/stage-app-package.sh" "${package}" "${output_dir}")"
staged_release="$(dirname "${staged_web_root}")"
if [[ -f "${staged_release}/www/index.php" ]]; then
    "${repo_root}/scripts/ci/patch-classic-mode.sh" "${staged_release}" \
        || echo "warning: FrankenPHP Classic compatibility patch not applied" >&2
fi

mkdir -p "${output_dir}/run" "${output_dir}/logs" "${output_dir}/data" \
    "${output_dir}/observability" "${output_dir}/spool/observability" \
    "${output_dir}/backups" "${output_dir}/config/conf.d"

cat > "${output_dir}/config/runtime.json.tpl" <<EOF
{
  "schemaVersion": 1,
  "runtime": {
    "controlSocket": "@ABS_ROOT@/run/runtime.sock",
    "pidFile": "@ABS_ROOT@/run/runtime.pid",
    "drainTimeout": "30s",
    "auditLog": "@ABS_ROOT@/logs/audit.log",
    "logPath": "@ABS_ROOT@/logs/runtime.log",
    "logMaxBytes": 16777216,
    "logMaxBackups": 5
  },
  "web": {
    "root": "@ABS_ROOT@/app/current/www",
    "listen": "0.0.0.0:8080",
    "threads": 8,
    "readHeaderTimeout": "10s",
    "idleTimeout": "30s",
    "maxHeaderBytes": 16384,
    "accessLog": "@ABS_ROOT@/logs/access.log"
  },
  "observability": {
    "enabled": true,
    "datasetRoot": "@ABS_ROOT@/observability",
    "spoolPath": "@ABS_ROOT@/spool/observability",
    "maxSpoolBytes": 1073741824,
    "maxBatchRows": 10000,
    "maxBatchBytes": 16777216,
    "flushInterval": "60s",
    "metricsDays": 30,
    "logDays": 7
  }
}
EOF

cat > "${output_dir}/config/php.ini.tpl" <<'EOF'
zend_extension=@ABS_ROOT@/php/lib/ioncube/ioncube_loader_lin_8.4_ts.so

expose_php=Off
display_errors=Off
log_errors=On
error_log=@ABS_ROOT@/logs/php-error.log
memory_limit=512M
max_execution_time=120
post_max_size=110M
upload_max_filesize=100M
date.timezone=Asia/Shanghai
session.use_strict_mode=1
session.lazy_write=1
opcache.enable=1
opcache.validate_timestamps=0
opcache.memory_consumption=128
opcache.max_accelerated_files=20000
EOF

cat > "${output_dir}/bin/render-config.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
root="$(cd "${root}" && pwd)"
mkdir -p "${root}/run" "${root}/logs" "${root}/config/conf.d"
sed "s|@ABS_ROOT@|${root}|g" "${root}/config/runtime.json.tpl" > "${root}/config/runtime.json"
sed "s|@ABS_ROOT@|${root}|g" "${root}/config/php.ini.tpl" > "${root}/config/php.ini"
chmod 600 "${root}/config/runtime.json" "${root}/config/php.ini"
EOF
chmod +x "${output_dir}/bin/render-config.sh"

cat > "${output_dir}/run.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export IN_CONTAINER="${IN_CONTAINER:-true}"
export ZT_INSTALLED="${ZT_INSTALLED:-true}"
export ZT_DB_DRIVER="${ZT_DB_DRIVER:-mysql}"
export ZT_DB_HOST="${ZT_DB_HOST:-127.0.0.1}"
export ZT_DB_PORT="${ZT_DB_PORT:-3306}"
export ZT_DB_NAME="${ZT_DB_NAME:-zentao}"
export ZT_DB_USER="${ZT_DB_USER:-zentao}"
export ZT_DB_PASSWORD="${ZT_DB_PASSWORD:-zentao}"
export ZT_DB_PREFIX="${ZT_DB_PREFIX:-zt_}"
export ZT_WEB_ROOT="${ZT_WEB_ROOT:-}"
export ZT_REQUEST_TYPE="${ZT_REQUEST_TYPE:-PATH_INFO}"
export ZT_TIMEZONE="${ZT_TIMEZONE:-Asia/Shanghai}"

"${root}/bin/render-config.sh" "${root}"
exec "${root}/bin/zentao-runtime" serve \
    --config "${root}/config/runtime.json" \
    --install-root "${root}"
EOF
chmod +x "${output_dir}/run.sh"

cat > "${output_dir}/compose.yaml" <<'EOF'
services:
  web:
    image: zentao-runtime:poc-linux-amd64
    entrypoint: ["sh", "-c"]
    command:
      - /opt/zentao/bin/render-config.sh /opt/zentao && exec /opt/zentao/bin/zentao-runtime serve --config /opt/zentao/config/runtime.json --install-root /opt/zentao
    environment:
      IN_CONTAINER: "true"
      ZT_INSTALLED: "true"
      ZT_DB_DRIVER: "${ZT_DB_DRIVER:-mysql}"
      ZT_DB_HOST: "${ZT_DB_HOST:-127.0.0.1}"
      ZT_DB_PORT: "${ZT_DB_PORT:-3306}"
      ZT_DB_NAME: "${ZT_DB_NAME:-zentao}"
      ZT_DB_USER: "${ZT_DB_USER:-zentao}"
      ZT_DB_PASSWORD: "${ZT_DB_PASSWORD:-zentao}"
      ZT_DB_PREFIX: "${ZT_DB_PREFIX:-zt_}"
      ZT_WEB_ROOT: "${ZT_WEB_ROOT:-}"
      ZT_REQUEST_TYPE: "${ZT_REQUEST_TYPE:-PATH_INFO}"
      ZT_TIMEZONE: "${ZT_TIMEZONE:-Asia/Shanghai}"
    ports:
      - "8080:8080"
    volumes:
      - .:/opt/zentao:rw
    healthcheck:
      test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:8080/_runtime/readiness"]
      interval: 5s
      timeout: 3s
      retries: 20
      start_period: 10s

  mysql:
    profiles: ["mysql"]
    image: mysql:8.4
    environment:
      MYSQL_DATABASE: "${ZT_DB_NAME:-zentao}"
      MYSQL_USER: "${ZT_DB_USER:-zentao}"
      MYSQL_PASSWORD: "${ZT_DB_PASSWORD:-zentao}"
      MYSQL_ROOT_PASSWORD: "${MYSQL_ROOT_PASSWORD:-zentao-root}"
    volumes:
      - mysql-data:/var/lib/mysql

volumes:
  mysql-data:
EOF

cat > "${output_dir}/db-init.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sql="${root}/app/current/db/zentao.sql"
if [[ ! -f "${sql}" ]]; then
    echo "zentao.sql not found at ${sql}" >&2
    exit 1
fi
export ZT_DB_HOST="${ZT_DB_HOST:-127.0.0.1}"
export ZT_DB_PORT="${ZT_DB_PORT:-3306}"
export ZT_DB_NAME="${ZT_DB_NAME:-zentao}"
export ZT_DB_USER="${ZT_DB_USER:-zentao}"
export ZT_DB_PASSWORD="${ZT_DB_PASSWORD:-zentao}"
mysql --protocol=tcp -h "${ZT_DB_HOST}" -P "${ZT_DB_PORT}" \
    -u "${ZT_DB_USER}" -p"${ZT_DB_PASSWORD}" "${ZT_DB_NAME}" < "${sql}"
echo "database schema loaded into ${ZT_DB_NAME}"
EOF
chmod +x "${output_dir}/db-init.sh"

echo "integrated environment generated: ${output_dir}"
echo "edition: ${edition}"
echo "next steps:"
echo "  1. (optional) ./db-init.sh to load the ZenTao schema into MySQL"
echo "  2. ./run.sh          (native)"
echo "  3. docker compose up  (container; add --profile mysql for bundled MySQL)"
