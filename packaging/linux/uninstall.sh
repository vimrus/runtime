#!/usr/bin/env bash
#
# Remove the ZenTao Runtime systemd services. User data under the prefix is
# preserved; only the service units and logrotate policy are removed.

set -Eeuo pipefail

SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PREFIX="${ZENTAO_PREFIX:-${SCRIPT_ROOT}}"

systemctl disable --now zentao.service 2>/dev/null || true
rm -f /etc/systemd/system/zentao.service /etc/systemd/system/zentao-runtime.service /etc/systemd/system/zentao-scheduler.service
rm -f /etc/logrotate.d/zentao
systemctl daemon-reload

echo "ZenTao Runtime services uninstalled"
echo "Data, configuration and logs were preserved under ${PREFIX}"
echo "Remove them manually only after backing up: ${PREFIX}/data"
