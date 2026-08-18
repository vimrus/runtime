#!/usr/bin/env bash
#
# Generate a CycloneDX-format SBOM for the Runtime from go.mod and the
# version lock. Output: <staging>/sbom.json

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: generate-sbom.sh <staging-dir>" >&2
    exit 2
fi

readonly staging="$1"
readonly repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly lock="${repo_root}/versions.lock.json"

if [[ ! -d "${staging}" ]]; then
    echo "staging directory does not exist: ${staging}" >&2
    exit 1
fi

php_version="$(jq -r '.php.version' "${lock}")"
frankenphp_version="$(jq -r '.frankenphp.version' "${lock}")"
caddy_version="$(jq -r '.caddy.version' "${lock}")"
duckdb_version="$(jq -r '.duckdb.go_binding.version' "${lock}")"
ioncube_version="$(jq -r '.ioncube.version' "${lock}")"

(
    cd "${repo_root}"
    go list -m -json all 2>/dev/null
) | jq -s \
    --arg php "${php_version}" \
    --arg frankenphp "${frankenphp_version}" \
    --arg caddy "${caddy_version}" \
    --arg duckdb "${duckdb_version}" \
    --arg ioncube "${ioncube_version}" \
    '{
        bomFormat: "CycloneDX",
        specVersion: "1.5",
        version: 1,
        metadata: {
            timestamp: (now | todate),
            component: {type: "application", name: "zentao-runtime", version: "dev"}
        },
        components: ((
            map({type: "library", name: .Path, version: .Version})
        ) + [
            {type: "library", name: "php", version: $php},
            {type: "library", name: "frankenphp", version: $frankenphp},
            {type: "library", name: "caddy", version: $caddy},
            {type: "library", name: "duckdb-go", version: $duckdb},
            {type: "library", name: "ioncube-loader", version: $ioncube}
        ])
    }' > "${staging}/sbom.json"

echo "SBOM written: ${staging}/sbom.json"
