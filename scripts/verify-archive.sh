#!/bin/sh
# Verify one packaged release archive.
#
# Always: extract to a temp directory and check the archive layout for its
# kind (client, Windows client, relay).
# Only when the archive matches this machine's OS and architecture: run
# `plan-forge version` and `plan-forge serve-ui --check`. Foreign-platform
# binaries cannot execute here, so they get the layout check only.
#
# Usage: scripts/verify-archive.sh <archive>
set -eu

usage() {
    printf 'usage: %s <archive>\n' "$0" >&2
    exit 2
}

fail() {
    printf '%s\n' "verify-archive.sh: $1" >&2
    exit 1
}

[ $# -eq 1 ] || usage
archive=$1
[ -f "$archive" ] || fail "no such archive: $archive"

tmpdir=$(mktemp -d) || fail "mktemp failed"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

case "$archive" in
    *.tar.gz) tar -xzf "$archive" -C "$tmpdir" ;;
    *.zip)
        command -v unzip >/dev/null 2>&1 || fail "unzip is required to verify .zip archives"
        unzip -q "$archive" -d "$tmpdir"
        ;;
    *) fail "unsupported archive type (expected .tar.gz or .zip)" ;;
esac

# Archive names are plan-forge-<os>-<arch>.<ext> or
# plan-forge-relay-<os>-<arch>.<ext>.
base=$(basename "$archive")
base=${base%.tar.gz}
base=${base%.zip}
case "$base" in
    plan-forge-relay-*) kind=relay; target=${base#plan-forge-relay-} ;;
    plan-forge-*) kind=client; target=${base#plan-forge-} ;;
    *) fail "unrecognized archive name: $base" ;;
esac
target_os=${target%-*}
target_arch=${target#*-}

need() {
    [ -e "$tmpdir/$1" ] || fail "$base is missing $1"
}

need LICENSE
need README.md

case "$kind" in
    relay)
        need plan-forge-relay
        printf 'verified layout: %s\n' "$archive"
        exit 0
        ;;
esac

# Client layout.
if [ "$target_os" = windows ]; then
    need plan-forge.exe
    need install.ps1
else
    need plan-forge
    need install.sh
fi
need commands/plan-forge.md
need commands/plan-config.md
need hooks/post-merge.sh
need integrations/skill-plan-forge.md
need integrations/skill-plan-config.md

# Rendered files must not keep unresolved placeholders.
if grep -rl '{{[A-Z_]*}}' "$tmpdir/commands" "$tmpdir/hooks" >/dev/null 2>&1; then
    fail "$base ships commands/hooks with unresolved {{PLACEHOLDER}} tokens"
fi

host_os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
    x86_64 | amd64) host_arch=amd64 ;;
    arm64 | aarch64) host_arch=arm64 ;;
    *) host_arch=$(uname -m) ;;
esac

if [ "$target_os" != "$host_os" ] || [ "$target_arch" != "$host_arch" ]; then
    printf 'verified layout (not executed on %s-%s): %s\n' "$host_os" "$host_arch" "$archive"
    exit 0
fi

bin="$tmpdir/plan-forge"
chmod +x "$bin"

"$bin" version || fail "plan-forge version failed"
"$bin" serve-ui --check || fail "plan-forge serve-ui --check failed"

printf 'verified: %s\n' "$archive"
