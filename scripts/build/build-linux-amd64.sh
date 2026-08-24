#!/usr/bin/env bash
# Deprecated compatibility entry point. Use scripts/native/build-linux.sh.

set -Eeuo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
exec "${repo_root}/scripts/native/build-linux.sh" amd64
