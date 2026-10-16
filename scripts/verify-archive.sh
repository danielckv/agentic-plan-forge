#!/bin/sh
# Verify one packaged release archive: extract it to a temp directory, run
# `plan-forge version`, then `plan-forge serve-ui --check`.
#
# Usage: scripts/verify-archive.sh <archive>
set -eu

usage() {
    printf 'usage: %s <archive>\n' "$0" >&2
    exit 2
}

[ $# -eq 1 ] || usage
archive=$1
[ -f "$archive" ] || {
    printf '%s\n' "no such archive: $archive" >&2
    exit 1
}

tmpdir=$(mktemp -d) || {
    printf '%s\n' 'mktemp failed' >&2
    exit 1
}
trap 'rm -rf "$tmpdir"' EXIT INT TERM

case "$archive" in
    *.tar.gz) tar -xzf "$archive" -C "$tmpdir" ;;
    *.zip)
        command -v unzip >/dev/null 2>&1 || {
            printf '%s\n' 'unzip is required to verify .zip archives' >&2
            exit 1
        }
        unzip -q "$archive" -d "$tmpdir"
        ;;
    *) printf '%s\n' 'unsupported archive type (expected .tar.gz or .zip)' >&2; exit 1 ;;
esac

bin="$tmpdir/plan-forge"
[ -f "$bin" ] || {
    printf '%s\n' 'archive does not contain plan-forge' >&2
    exit 1
}
chmod +x "$bin"

"$bin" version || {
    printf '%s\n' 'plan-forge version failed' >&2
    exit 1
}

"$bin" serve-ui --check || {
    printf '%s\n' 'plan-forge serve-ui --check failed' >&2
    exit 1
}

printf 'verified: %s\n' "$archive"
