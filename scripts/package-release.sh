#!/bin/sh
# Assemble the release artifacts from out/ into dist/: the 8 archives, both
# installers and checksums.txt. Asset names are unversioned.
#
# Usage: scripts/package-release.sh [--snapshot]
#   --snapshot  use version 0.0.0-snapshot and require no tag.
#
# Expected inputs (built by the release workflow or by hand):
#   out/plan-forge-darwin-arm64/plan-forge     out/plan-forge-relay-linux-amd64/plan-forge-relay
#   out/plan-forge-darwin-amd64/plan-forge     out/plan-forge-relay-linux-arm64/plan-forge-relay
#   out/plan-forge-linux-amd64/plan-forge     LICENSE README.md
#   out/plan-forge-linux-arm64/plan-forge      scripts/install.sh scripts/install.ps1
#   out/plan-forge-windows-amd64/plan-forge.exe  integrations/ (repository copy)
set -eu

usage() {
    printf 'usage: %s [--snapshot]\n' "$0" >&2
    exit 2
}

snapshot=0
version=${PLAN_FORGE_VERSION:-}
for arg in "$@"; do
    case "$arg" in
        --snapshot) snapshot=1 ;;
        *) usage ;;
    esac
done

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/out"
dist="$root/dist"

if [ "$snapshot" = 1 ]; then
    version=0.0.0-snapshot
elif [ -z "$version" ]; then
    if command -v git >/dev/null 2>&1 && git -C "$root" describe --exact-match --tags >/dev/null 2>&1; then
        version=$(git -C "$root" describe --exact-match --tags)
    else
        printf '%s\n' "package-release.sh: no version given; pass --snapshot or set PLAN_FORGE_VERSION to a tag" >&2
        exit 2
    fi
fi

rm -rf "$dist"
mkdir -p "$dist"

die() {
    printf '%s\n' "$1" >&2
    exit 1
}

# stage_archive <name> <binary> <installer>
# The client archive layout: plan-forge[.exe], LICENSE, README.md, install.sh
# (Windows: install.ps1), commands/plan-forge.md, commands/plan-config.md,
# hooks/post-merge.sh, integrations/ (per-host templates).
stage_archive() {
    name=$1
    binary=$2
    installer=$3
    stage=$(mktemp -d) || die "mktemp failed"
    mkdir -p "$stage/commands" "$stage/hooks" "$stage/integrations"
    cp "$binary" "$stage/plan-forge$(case $name in *windows*) printf '.exe';; esac)" || die "copy binary failed"
    cp "$root/LICENSE" "$root/README.md" "$stage/" || die "copy docs failed"
    cp "$root/scripts/$installer" "$stage/" || die "copy installer failed"
    # Generic copies of the skill bodies for manual installation.
    sed -e "s|{{PLAN_FORGE_BIN}}|plan-forge|g" \
        -e "s|{{HOST}}|claude|g" \
        -e "s|{{WAIT_TIMEOUT}}|100s|g" \
        -e "s|{{PLAN_MODE_HINT}}|press Shift+Tab until Plan Mode is shown|g" \
        "$root/integrations/skill-plan-forge.md" >"$stage/commands/plan-forge.md" || die "render command failed"
    sed -e "s|{{PLAN_FORGE_BIN}}|plan-forge|g" \
        "$root/integrations/skill-plan-config.md" >"$stage/commands/plan-config.md" || die "render command failed"
    # The guarded git-hook block with a generic binary resolution.
    sed -e "s|{{PLAN_FORGE_BIN}}|\$(command -v plan-forge | tr -d '\n')|g" \
        "$root/integrations/git-hook-post-merge.txt" >"$stage/hooks/post-merge.sh" || die "render hook failed"
    cp "$root"/integrations/*.md "$root"/integrations/*.ts "$root"/integrations/*.txt "$stage/integrations/" || die "copy integrations failed"
    tar -czf "$dist/$name.tar.gz" -C "$stage" . || die "archive failed"
    rm -rf "$stage"
}

# The 8 archives: 5 client tarballs, 1 client zip, 2 relay tarballs.
stage_archive plan-forge-darwin-arm64 "$out/plan-forge-darwin-arm64/plan-forge" install.sh
stage_archive plan-forge-darwin-amd64 "$out/plan-forge-darwin-amd64/plan-forge" install.sh
stage_archive plan-forge-linux-amd64 "$out/plan-forge-linux-amd64/plan-forge" install.sh
stage_archive plan-forge-linux-arm64 "$out/plan-forge-linux-arm64/plan-forge" install.sh
stage_archive plan-forge-windows-amd64 "$out/plan-forge-windows-amd64/plan-forge.exe" install.ps1

# Windows zip (same layout, plan-forge.exe + install.ps1).
stage=$(mktemp -d) || die "mktemp failed"
mkdir -p "$stage/commands" "$stage/hooks" "$stage/integrations"
cp "$out/plan-forge-windows-amd64/plan-forge.exe" "$stage/" || die "copy exe failed"
cp "$root/LICENSE" "$root/README.md" "$root/scripts/install.ps1" "$stage/" || die "copy docs failed"
sed -e "s|{{PLAN_FORGE_BIN}}|plan-forge|g" "$root/integrations/skill-plan-forge.md" >"$stage/commands/plan-forge.md"
sed -e "s|{{PLAN_FORGE_BIN}}|plan-forge|g" "$root/integrations/skill-plan-config.md" >"$stage/commands/plan-config.md"
sed -e "s|{{PLAN_FORGE_BIN}}|\$(command -v plan-forge | tr -d '\n')|g" \
    "$root/integrations/git-hook-post-merge.txt" >"$stage/hooks/post-merge.sh"
cp "$root"/integrations/*.md "$root"/integrations/*.ts "$root"/integrations/*.txt "$stage/integrations/"
if command -v zip >/dev/null 2>&1; then
    (cd "$stage" && zip -qr - .) >"$dist/plan-forge-windows-amd64.zip" || die "zip failed"
else
    die "zip is required to build plan-forge-windows-amd64.zip"
fi
rm -rf "$stage"

# Relay archives: binary + LICENSE + README.md.
for arch in amd64 arm64; do
    stage=$(mktemp -d) || die "mktemp failed"
    cp "$out/plan-forge-relay-linux-$arch/plan-forge-relay" "$stage/" || die "copy relay failed"
    cp "$root/LICENSE" "$root/README.md" "$stage/" || die "copy docs failed"
    tar -czf "$dist/plan-forge-relay-linux-$arch.tar.gz" -C "$stage" . || die "archive failed"
    rm -rf "$stage"
done

# Installers ship as plain release assets.
cp "$root/scripts/install.sh" "$root/scripts/install.ps1" "$dist/" || die "copy installers failed"

# checksums.txt: SHA-256 of every asset by exact name.
(
    cd "$dist"
    : >checksums.txt
    for asset in plan-forge-darwin-arm64.tar.gz \
        plan-forge-darwin-amd64.tar.gz \
        plan-forge-linux-amd64.tar.gz \
        plan-forge-linux-arm64.tar.gz \
        plan-forge-windows-amd64.tar.gz \
        plan-forge-windows-amd64.zip \
        plan-forge-relay-linux-amd64.tar.gz \
        plan-forge-relay-linux-arm64.tar.gz \
        install.sh install.ps1; do
        if command -v sha256sum >/dev/null 2>&1; then
            sha256sum "$asset" >>checksums.txt
        else
            shasum -a 256 "$asset" >>checksums.txt
        fi
    done
) || exit 1

printf 'Packaged %s into dist/:\n' "$version"
ls -1 "$dist"
