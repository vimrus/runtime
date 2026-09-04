#!/usr/bin/env bash
#
# Generate manifest.json for a staged Runtime artifact from the version lock
# and the current Git metadata. Output is written to
# <staging>/manifest.json and must not contain secrets or absolute paths.

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: generate-manifest.sh <staging-dir>" >&2
    exit 2
fi

readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly staging="$1"
readonly lock="${repo_root}/versions.lock.json"

dm8_arch="amd64"
case "$(basename "${staging}")" in
    *-arm64) dm8_arch="arm64" ;;
    *-amd64) dm8_arch="amd64" ;;
esac
readonly dm8_lock_key="linux_${dm8_arch}"

if [[ ! -d "${staging}" ]]; then
    echo "staging directory does not exist: ${staging}" >&2
    exit 1
fi

git_commit="unknown"
git_ref="unknown"
if git -C "${repo_root}" rev-parse --verify HEAD >/dev/null 2>&1; then
    git_commit="$(git -C "${repo_root}" rev-parse HEAD)"
    git_ref="$(git -C "${repo_root}" symbolic-ref --short HEAD 2>/dev/null || echo detached)"
fi

manifest="$(
    jq -n \
        --arg schema "$(jq -r '.schema' "${lock}")" \
        --arg phpVersion "$(jq -r '.php.version' "${lock}")" \
        --arg phpSha256 "$(jq -r '.php.sha256' "${lock}")" \
        --arg frankenphpVersion "$(jq -r '.frankenphp.version' "${lock}")" \
        --arg frankenphpCommit "$(jq -r '.frankenphp.commit' "${lock}")" \
        --arg caddyVersion "$(jq -r '.caddy.version' "${lock}")" \
        --arg duckdbVersion "$(jq -r '.duckdb.go_binding.version' "${lock}")" \
        --arg ioncubeVersion "$(jq -r '.ioncube.version' "${lock}")" \
        --arg dm8Version "$(jq -r '.dm8.version' "${lock}")" \
        --arg dm8Package "$(jq -r ".dm8.${dm8_lock_key}.package" "${lock}")" \
        --arg dm8DriverSha256 "$(jq -r ".dm8.${dm8_lock_key}.pdo_driver_sha256" "${lock}")" \
        --arg dm8DpiSha256 "$(jq -r ".dm8.${dm8_lock_key}.dpi_sha256" "${lock}")" \
        --argjson dm8Components "$(jq -r ".dm8.${dm8_lock_key}.client_components" "${lock}")" \
        --arg dm8Arch "${dm8_arch}" \
        --arg goVersion "$(jq -r '.go.version' "${lock}")" \
        --arg gitCommit "${git_commit}" \
        --arg gitRef "${git_ref}" \
        --arg builtAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        '{
            schema: ($schema | tonumber),
            git: {commit: $gitCommit, ref: $gitRef},
            builtAt: $builtAt,
            components: {
                php: {version: $phpVersion, sha256: $phpSha256},
                frankenphp: {version: $frankenphpVersion, commit: $frankenphpCommit},
                caddy: {version: $caddyVersion},
                duckdb: {goBinding: $duckdbVersion},
                ioncube: {version: $ioncubeVersion},
                dm8: {version: $dm8Version, arch: $dm8Arch, package: $dm8Package, pdoDriverSha256: $dm8DriverSha256, dpiSha256: $dm8DpiSha256, clientComponents: $dm8Components},
                go: {version: $goVersion}
            },
            licenses: {
                php: "PHP License 3.01",
                frankenphp: "MIT",
                caddy: "Apache-2.0",
                duckdb: "MIT",
                ioncube: "proprietary-redistribution-approval-required",
                dm8: "proprietary-redistribution-approval-required",
                mysql: "GPL-2.0-with-linking-exception"
            }
        }'
)"

printf '%s\n' "${manifest}" > "${staging}/manifest.json"
echo "manifest written: ${staging}/manifest.json"
