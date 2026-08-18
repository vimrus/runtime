#!/usr/bin/env bash
#
# R-CI-04: verify the Linux installer stages the Runtime directories, systemd
# units and logrotate policy without requiring root or a running systemd.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

prefix="/opt/zentao"
mkdir -p "${tmpdir}/opt/zentao/bin"
cp "${repo_root}/dist/poc-linux-amd64/bin/zentao-runtime" "${tmpdir}/opt/zentao/bin/zentao-runtime" 2>/dev/null || \
    touch "${tmpdir}/opt/zentao/bin/zentao-runtime"

ZENTAO_DRY_RUN=1 \
ZENTAO_STAGE_ROOT="${tmpdir}" \
ZENTAO_PREFIX="${prefix}" \
    "${repo_root}/packaging/linux/install.sh" --no-start >/dev/null

for path in \
    "${tmpdir}/opt/zentao/config" \
    "${tmpdir}/opt/zentao/data" \
    "${tmpdir}/opt/zentao/logs" \
    "${tmpdir}/opt/zentao/spool/observability" \
    "${tmpdir}/opt/zentao/observability" \
    "${tmpdir}/opt/zentao/backups" \
    "${tmpdir}/run/zentao" \
    "${tmpdir}/etc/systemd/system" \
    "${tmpdir}/etc/logrotate.d"; do
    [[ -d "${path}" ]] || { echo "missing directory: ${path}" >&2; exit 1; }
done

for unit in zentao-runtime.service zentao-scheduler.service; do
    [[ -f "${tmpdir}/etc/systemd/system/${unit}" ]] || { echo "missing unit: ${unit}" >&2; exit 1; }
done
grep -q "ExecStart=${prefix}/bin/zentao-runtime" "${tmpdir}/etc/systemd/system/zentao-runtime.service"
[[ -f "${tmpdir}/etc/logrotate.d/zentao" ]] || { echo "missing logrotate policy" >&2; exit 1; }

echo "install dry run passed"
