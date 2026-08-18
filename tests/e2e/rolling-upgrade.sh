#!/usr/bin/env bash
#
# R-E2E-01 rolling upgrade scenario: two Runtime nodes share the application
# releases volume but keep independent current pointers. Node B is upgraded
# and verified while node A keeps serving the old release; then node A is
# upgraded and both serve the new release.

set -Eeuo pipefail

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/poc/common.sh"

if ! docker image inspect "${POC_IMAGE}" >/dev/null 2>&1; then
    build_poc_image
fi

readonly tmpdir="$(mktemp -d)"
readonly container_a="zentao-roll-a-${RANDOM}-$$"
readonly container_b="zentao-roll-b-${RANDOM}-$$"
cleanup() {
    docker rm --force "${container_a}" "${container_b}" >/dev/null 2>&1 || true
    docker run --rm --entrypoint sh --volume "${tmpdir}:/shared:rw" "${POC_IMAGE}" -c 'find /shared -mindepth 1 -delete' >/dev/null 2>&1 || true
    rmdir "${tmpdir}" 2>/dev/null || true
}
trap cleanup EXIT

for node in a b; do
    port=8080
    if [[ "${node}" == "b" ]]; then
        port=8081
    fi
    for version in v1 v2; do
        mkdir -p "${tmpdir}/node-${node}/app/releases/${version}/www"
        printf '%s\n' "${version}" > "${tmpdir}/node-${node}/app/releases/${version}/www/version.txt"
    done
    cat > "${tmpdir}/node-${node}/runtime.json" <<EOF
{
  "schemaVersion": 1,
  "runtime": {"controlSocket": "/tmp/${node}.sock", "pidFile": "/tmp/${node}.pid", "drainTimeout": "10s"},
  "web": {"root": "/shared/node-${node}/app/current/www", "listen": "127.0.0.1:${port}", "threads": 4, "readHeaderTimeout": "10s", "idleTimeout": "30s", "maxHeaderBytes": 16384}
}
EOF
    printf '%s' "${port}" > "${tmpdir}/node-${node}/port"
    ln -s "releases/v1" "${tmpdir}/node-${node}/app/current"
done

start_node() {
    local name="$1"
    local config="$2"
    local install_root="$3"
    docker run --detach --name "${name}" \
        --entrypoint sh \
        --volume "${tmpdir}:/shared:rw" \
        "${POC_IMAGE}" \
        -c "exec zentao-runtime serve --config ${config} --install-root ${install_root}" >/dev/null
}

wait_ready() {
    local name="$1"
    local port="$2"
    for _ in $(seq 1 30); do
        if docker exec "${name}" curl --fail --silent "http://127.0.0.1:${port}/_runtime/readiness" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

serve_version() {
    local name="$1"
    local port="$2"
    docker exec "${name}" curl --fail --silent "http://127.0.0.1:${port}/version.txt"
}

start_node "${container_a}" /shared/node-a/runtime.json /shared/node-a
start_node "${container_b}" /shared/node-b/runtime.json /shared/node-b
wait_ready "${container_a}" 8080
wait_ready "${container_b}" 8081

[[ "$(serve_version "${container_a}" 8080)" == "v1" ]]
[[ "$(serve_version "${container_b}" 8081)" == "v1" ]]

# Rolling upgrade node B first.
docker exec "${container_b}" zentao-runtime upgrade \
    --action prepare --release /shared/node-b/app/releases/v2 \
    --control-socket /tmp/b.sock | jq --exit-status '.state == "staged"' >/dev/null
docker exec "${container_b}" zentao-runtime upgrade \
    --action apply --control-socket /tmp/b.sock | jq --exit-status '.state == "committed"' >/dev/null
[[ "$(serve_version "${container_b}" 8081)" == "v2" ]]
[[ "$(serve_version "${container_a}" 8080)" == "v1" ]]

# Then node A.
docker exec "${container_a}" zentao-runtime upgrade \
    --action prepare --release /shared/node-a/app/releases/v2 \
    --control-socket /tmp/a.sock | jq --exit-status '.state == "staged"' >/dev/null
docker exec "${container_a}" zentao-runtime upgrade \
    --action apply --control-socket /tmp/a.sock | jq --exit-status '.state == "committed"' >/dev/null
[[ "$(serve_version "${container_a}" 8080)" == "v2" ]]
[[ "$(serve_version "${container_b}" 8081)" == "v2" ]]

echo "rolling upgrade passed"
