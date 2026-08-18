#!/usr/bin/env bash
#
# Install the ZenTao Runtime as a systemd service. The Runtime itself must
# already be staged under /opt/zentao (or the configured ZENTAO_PREFIX).
#
# Usage: install.sh [--prefix /opt/zentao] [--no-start]
#
# Test mode: set ZENTAO_DRY_RUN=1 to stage files without touching systemd or
# creating users; ZENTAO_STAGE_ROOT overrides the filesystem root used by
# dry-run staging.

set -Eeuo pipefail

PREFIX="${ZENTAO_PREFIX:-/opt/zentao}"
START_SERVICE=1
DRY_RUN="${ZENTAO_DRY_RUN:-0}"
STAGE_ROOT="${ZENTAO_STAGE_ROOT:-/}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --prefix)
            PREFIX="$2"
            shift 2
            ;;
        --no-start)
            START_SERVICE=0
            shift
            ;;
        *)
            echo "unknown option: $1" >&2
            exit 2
            ;;
    esac
done

if [[ ! -x "${PREFIX}/bin/zentao-runtime" ]] && [[ "${DRY_RUN}" != "1" ]]; then
    echo "zentao-runtime binary not found under ${PREFIX}/bin" >&2
    exit 1
fi
if [[ "${PREFIX}" != /* ]]; then
    echo "prefix must be an absolute path" >&2
    exit 2
fi

stage() {
    if [[ "${DRY_RUN}" == "1" ]]; then
        printf '%s\n' "${STAGE_ROOT}${1}"
    else
        printf '%s\n' "$1"
    fi
}

if [[ "${DRY_RUN}" != "1" ]]; then
    id zentao >/dev/null 2>&1 || useradd --system --home-dir "${PREFIX}" --shell /usr/sbin/nologin zentao
fi

prefix_stage="$(stage "${PREFIX}")"
install -d -m 0750 "${prefix_stage}/config" \
    "${prefix_stage}/data" \
    "${prefix_stage}/logs" \
    "${prefix_stage}/spool/observability" \
    "${prefix_stage}/observability" \
    "${prefix_stage}/backups"
install -d -m 0700 "$(stage /run/zentao)"
install -d -m 0755 "$(stage /etc/systemd/system)" "$(stage /etc/logrotate.d)"

sed -e "s|/opt/zentao|${PREFIX}|g" \
    "$(dirname "$0")/zentao-runtime.service" \
    > "$(stage /etc/systemd/system)/zentao-runtime.service"
sed -e "s|/opt/zentao|${PREFIX}|g" \
    "$(dirname "$0")/zentao-scheduler.service" \
    > "$(stage /etc/systemd/system)/zentao-scheduler.service"
install -m 0644 "$(dirname "$0")/zentao.logrotate" "$(stage /etc/logrotate.d)/zentao"

if [[ "${DRY_RUN}" == "1" ]]; then
    echo "dry run complete: units staged under ${STAGE_ROOT}"
    exit 0
fi

systemctl daemon-reload
systemctl enable zentao-runtime.service zentao-scheduler.service
if [[ "${START_SERVICE}" == "1" ]]; then
    systemctl start zentao-runtime.service
fi

echo "ZenTao Runtime installed: ${PREFIX}"
echo "Manage with: systemctl status zentao-runtime zentao-scheduler"
