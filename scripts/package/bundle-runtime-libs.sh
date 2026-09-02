#!/usr/bin/env bash
# Bundle non-glibc shared libraries required by the Linux Runtime artifact.

set -Eeuo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: bundle-runtime-libs.sh <runtime-root>" >&2
    exit 2
fi

readonly root="$1"
readonly libdir="${root}/php/lib"
mkdir -p "${libdir}"

declare -A queued=()
queue=()

queue_file() {
    local file="$1"
    [[ -f "${file}" ]] || return 0
    [[ -n "${queued[${file}]:-}" ]] && return 0
    queued["${file}"]=1
    queue+=("${file}")
}

is_system_core() {
    case "$(basename "$1")" in
        libc.so.*|libm.so.*|libpthread.so.*|libdl.so.*|librt.so.*|libresolv.so.*|libutil.so.*|libcrypt.so.*|ld-linux*.so.*)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

queue_file "${root}/bin/zentao"
queue_file "${root}/php/bin/php"
queue_file "${root}/php/lib/libphp.so"
for loader in "${root}/php/lib/ioncube"/*.so; do
    queue_file "${loader}"
done

while ((${#queue[@]})); do
    file="${queue[0]}"
    queue=("${queue[@]:1}")

    while read -r dependency; do
        [[ -n "${dependency}" ]] || continue
        real="$(readlink -f "${dependency}")"
        [[ -f "${real}" ]] || continue
        is_system_core "${real}" && continue

        case "${real}" in
            "${libdir}"/*)
                ;;
            *)
                target="${libdir}/$(basename "${real}")"
                [[ -f "${target}" ]] || cp -L "${real}" "${target}"
                link="${libdir}/$(basename "${dependency}")"
                if [[ "$(basename "${dependency}")" != "$(basename "${real}")" ]]; then
                    ln -sfn "$(basename "${real}")" "${link}"
                fi
                ;;
        esac

        queue_file "${real}"
    done < <(LD_LIBRARY_PATH="${libdir}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}" ldd "${file}" 2>/dev/null | awk '$3 ~ /^\// {print $3}')
done
