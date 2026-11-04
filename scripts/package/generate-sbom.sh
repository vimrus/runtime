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
php_api="$(jq -r '.php.api' "${lock}")"
frankenphp_version="$(jq -r '.frankenphp.version' "${lock}")"
caddy_version="$(jq -r '.caddy.version' "${lock}")"
duckdb_version="$(jq -r '.duckdb.go_binding.version' "${lock}")"
ioncube_version="$(jq -r '.ioncube.version' "${lock}")"
dm8_version="$(jq -r '.dm8.version' "${lock}")"
dm8_enabled="$(jq -r '.dm8.packaging_enabled // false' "${lock}")"
php_patch_applied=false
case "$(basename "${staging}")" in
    *-amd64) php_patch_applied=true ;;
esac

(
    cd "${repo_root}"
    go list -m -json all 2>/dev/null
) | jq -s \
    --arg php "${php_version}" \
    --arg php_api "${php_api}" \
    --arg frankenphp "${frankenphp_version}" \
    --arg caddy "${caddy_version}" \
    --arg duckdb "${duckdb_version}" \
    --arg ioncube "${ioncube_version}" \
    --arg dm8 "${dm8_version}" \
    --argjson dm8_enabled "${dm8_enabled}" \
    --argjson php_patch_applied "${php_patch_applied}" \
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
        ) + ([
            {type: "library", name: "php", version: $php, properties: [{name: "module-api", value: $php_api}]},
            {type: "library", name: "frankenphp", version: $frankenphp},
            {type: "library", name: "caddy", version: $caddy},
            {type: "library", name: "duckdb-go", version: $duckdb},
            {type: "library", name: "ioncube-loader", version: $ioncube}
        ] + (if $php_patch_applied then [{type: "library", name: "zentao-zend-signal-shim", version: "1"}] else [] end)
          + (if $dm8_enabled then [{type: "library", name: "dm8-pdo-driver", version: $dm8}] else [] end)))
    }' > "${staging}/sbom.json"

echo "SBOM written: ${staging}/sbom.json"
