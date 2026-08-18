#!/usr/bin/env bash

set -Eeuo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly VERSION_LOCK="${REPO_ROOT}/versions.lock.json"
readonly POC_ARCH="${POC_ARCH:-amd64}"
readonly POC_IMAGE="zentao-runtime:poc-linux-${POC_ARCH}"
readonly POC_GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
readonly POC_DEBIAN_MIRROR="${DEBIAN_MIRROR:-http://mirrors.ustc.edu.cn/debian}"
readonly POC_DEBIAN_SECURITY_MIRROR="${DEBIAN_SECURITY_MIRROR:-http://mirrors.ustc.edu.cn/debian-security}"

lock_value() {
    jq --exit-status --raw-output "$1" "${VERSION_LOCK}"
}

verify_lock() {
    jq --exit-status '
        .schema == 1 and
        (.go.version | type == "string" and length > 0) and
        (.images.builder | startswith("mcr.microsoft.com/")) and
        (.images.runtime | startswith("mcr.microsoft.com/")) and
        (.php.version | type == "string" and length > 0) and
        (.php.url | startswith("https://")) and
        (.php.sha256 | test("^[0-9a-f]{64}$")) and
        (.php.windows.runtime.url | startswith("https://")) and
        (.php.windows.runtime.sha256 | test("^[0-9a-f]{64}$")) and
        (.php.windows.development.url | startswith("https://")) and
        (.php.windows.development.sha256 | test("^[0-9a-f]{64}$")) and
        (.frankenphp.version | type == "string" and length > 0) and
        (.frankenphp.commit | test("^[0-9a-f]{40}$")) and
        (.caddy.version | type == "string" and length > 0) and
        (.ioncube.linux_amd64.url | startswith("https://")) and
        (.ioncube.linux_amd64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.ioncube.linux_amd64.loader_sha256 | test("^[0-9a-f]{64}$")) and
        (.ioncube.linux_arm64.url | startswith("https://")) and
        (.ioncube.linux_arm64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.ioncube.linux_arm64.loader_sha256 | test("^[0-9a-f]{64}$")) and
        (.ioncube.windows_x64.url | startswith("https://")) and
        (.ioncube.windows_x64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.ioncube.windows_x64.loader_sha256 | test("^[0-9a-f]{64}$")) and
        (.duckdb.go_binding.version | type == "string" and length > 0) and
        (.duckdb.commit | test("^[0-9a-f]{40}$")) and
        (.phpredis.commit | test("^[0-9a-f]{40}$")) and
        (.mysql.linux_amd64.url | startswith("https://")) and
        (.mysql.linux_amd64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.mysql.linux_arm64.url | startswith("https://")) and
        (.mysql.linux_arm64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.mysql.windows_x64.url | startswith("https://")) and
        (.mysql.windows_x64.archive_sha256 | test("^[0-9a-f]{64}$")) and
        (.php.extensions | type == "array" and length > 0)
    ' "${VERSION_LOCK}" >/dev/null
}

docker_build_args() {
    local ioncube_key="linux_amd64"
    local php_libdir="lib/x86_64-linux-gnu"
    if [[ "${POC_ARCH}" == "arm64" ]]; then
        ioncube_key="linux_arm64"
        php_libdir="lib/aarch64-linux-gnu"
    fi
    printf '%s\n' \
        --build-arg "GO_VERSION=$(lock_value '.go.version')" \
        --build-arg "GOPROXY=${POC_GOPROXY}" \
        --build-arg "DEBIAN_MIRROR=${POC_DEBIAN_MIRROR}" \
        --build-arg "DEBIAN_SECURITY_MIRROR=${POC_DEBIAN_SECURITY_MIRROR}" \
        --build-arg "BUILDER_IMAGE=$(lock_value '.images.builder')" \
        --build-arg "RUNTIME_IMAGE=$(lock_value '.images.runtime')" \
        --build-arg "PHP_VERSION=$(lock_value '.php.version')" \
        --build-arg "PHP_URL=$(lock_value '.php.url')" \
        --build-arg "PHP_SHA256=$(lock_value '.php.sha256')" \
        --build-arg "PHP_LIBDIR=${php_libdir}" \
        --build-arg "FRANKENPHP_VERSION=$(lock_value '.frankenphp.version')" \
        --build-arg "CADDY_VERSION=$(lock_value '.caddy.version')" \
        --build-arg "IONCUBE_VERSION=$(lock_value '.ioncube.version')" \
        --build-arg "IONCUBE_URL=$(lock_value ".ioncube.${ioncube_key}.url")" \
        --build-arg "IONCUBE_ARCHIVE_SHA256=$(lock_value ".ioncube.${ioncube_key}.archive_sha256")" \
        --build-arg "IONCUBE_LOADER=$(lock_value ".ioncube.${ioncube_key}.loader")" \
        --build-arg "IONCUBE_LOADER_SHA256=$(lock_value ".ioncube.${ioncube_key}.loader_sha256")" \
        --build-arg "LOADER_ARCH=${POC_ARCH}"
}

build_poc_image() {
    verify_lock
    mapfile -t args < <(docker_build_args)
    local platform_args=()
    if [[ -n "${ZENTAO_BUILDX_PLATFORM:-}" ]]; then
        platform_args=(--platform "${ZENTAO_BUILDX_PLATFORM}")
    fi
    docker buildx build \
        --file "${REPO_ROOT}/packaging/poc/Dockerfile" \
        --target runtime \
        --tag "${POC_IMAGE}" \
        "${platform_args[@]}" \
        "${args[@]}" \
        "${REPO_ROOT}"
}
