#!/bin/sh
# Plan-Forge installer (macOS / Linux). POSIX sh, shellcheck-clean.
#
# Downloads the release archive for this platform, verifies its SHA-256
# against checksums.txt, installs the binary atomically, then registers the
# agent skills via `plan-forge install` (unless PLAN_FORGE_NO_SKILLS=1).
#
# Exit codes: 1 download/checksum/platform errors; the installer's own codes
# (0, 3, 4, 5) are propagated verbatim.
set -eu

REPO="danielckv/agentic-plan-forge"

die() {
    printf '%s\n' "$1" >&2
    exit 1
}

# --- platform mapping ------------------------------------------------------
os=$(uname -s)
case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) die "unsupported operating system: $os (supported: Darwin, Linux)" ;;
esac
arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "unsupported architecture: $arch (supported: x86_64/amd64, arm64/aarch64)" ;;
esac

# --- environment overrides -------------------------------------------------
version=${PLAN_FORGE_VERSION:-}
case "$version" in
    v[0-9]*.[0-9]*.[0-9]*-[0-9A-Za-z.]*) ;;
    v[0-9]*.[0-9]*.[0-9]*) ;;
    [0-9]*.[0-9]*.[0-9]*-[0-9A-Za-z.]*) ;;
    [0-9]*.[0-9]*.[0-9]*) ;;
    "") version=latest ;;
    *) die "PLAN_FORGE_VERSION must look like a semantic version (v1.2.3)" ;;
esac

install_dir=${PLAN_FORGE_INSTALL_DIR:-"$HOME/.local/bin"}
case "$install_dir" in
    /*) ;;
    *) die "PLAN_FORGE_INSTALL_DIR must be an absolute path" ;;
esac

base_url=${PLAN_FORGE_BASE_URL:-}
fetch_cmd=curl-https
if [ -n "$base_url" ]; then
    # Test-only override (loopback / file://). Never documented in the README.
    ctrl_pattern="\$(printf '[\001\002\003\004\005\006\007\010\011\012\013\014\015\016\017\020\021\022\023\024\025\026\027\030\031\032\033\034\035\036\037\177]')"
    if printf '%s' "$base_url" | grep -Eq "$ctrl_pattern"; then
        die "PLAN_FORGE_BASE_URL contains a control character"
    fi
    if ! printf '%s' "$base_url" | grep -Eq '^(http://(127\.0\.0\.1|localhost):[0-9]{1,5}(/[A-Za-z0-9._~/-]*)?|file:///[A-Za-z0-9._~/-]*)$'; then
        die "PLAN_FORGE_BASE_URL must be a loopback http:// or file:// URL"
    fi
    printf 'warning: PLAN_FORGE_BASE_URL is set; using %s without TLS verification\n' "$base_url" >&2
    fetch_cmd=curl-local
fi

# --- download helpers ------------------------------------------------------
fetch() {
    # fetch URL -> stdout. Uses the hardened curl flags (or the loopback
    # variant for the test override); no wget fallback exists for the
    # override because plain http and file:// need it disabled anyway.
    if [ "$fetch_cmd" = curl-local ]; then
        curl --proto '=http,file' -fsSL "$1" || die "download failed: $1"
    else
        curl --proto '=https' --tlsv1.2 -fsSL "$1" || wget --https-only -qO- "$1" || die "download failed: $1"
    fi
}

if [ "$version" = latest ]; then
    release_path="/releases/latest/download/"
else
    release_path="/releases/download/v${version#v}/"
fi
case "$base_url" in
    "") base_path="https://github.com/${REPO}${release_path}" ;;
    *) base_path="${base_url}${release_path}" ;;
esac

archive="plan-forge-${os}-${arch}.tar.gz"

tmpdir=$(mktemp -d) || die "mktemp failed"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

printf 'Downloading %s\n' "$archive"
fetch "${base_path}${archive}" >"$tmpdir/$archive" || die "download failed: $archive"
fetch "${base_path}checksums.txt" >"$tmpdir/checksums.txt" || die "download failed: checksums.txt"

# --- checksum verification -------------------------------------------------
(
    cd "$tmpdir"
    if command -v sha256sum >/dev/null 2>&1; then
        grep " ${archive}\$" checksums.txt | sha256sum -c - || die "checksum mismatch for $archive"
    elif command -v shasum >/dev/null 2>&1; then
        grep " ${archive}\$" checksums.txt | shasum -a 256 -c - || die "checksum mismatch for $archive"
    else
        die "neither sha256sum nor shasum is available; cannot verify the download"
    fi
) || exit 1

# --- extraction and atomic install ----------------------------------------
extractdir="$tmpdir/x"
mkdir -p "$extractdir"
tar -xzf "$tmpdir/$archive" -C "$extractdir" || die "extraction failed"

mkdir -p "$install_dir" || die "cannot create $install_dir"
newbin=$(mktemp "$install_dir/.plan-forge.XXXXXX") || die "mktemp in $install_dir failed"
chmod 755 "$newbin" || die "chmod failed"
if [ "$os" = darwin ]; then
    # Remove the quarantine attribute curl may set on downloaded files.
    xattr -d com.apple.quarantine "$newbin" 2>/dev/null || true
fi
if [ -n "$(find "$extractdir" -maxdepth 1 -name plan-forge -print -quit)" ]; then
    cp "$extractdir/plan-forge" "$newbin" || die "copy failed"
else
    die "archive does not contain a plan-forge binary"
fi
mv -f "$newbin" "$install_dir/plan-forge" || die "install failed"
printf 'Installed %s/plan-forge\n' "$install_dir"

case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
        printf 'Add the install directory to PATH (then restart your shell):\n'
        # shellcheck disable=SC2016 # the $PATH expansion is literal text for the user's shell rc
        printf '  export PATH="%s:$PATH"\n' "$install_dir"
        ;;
esac

# --- skill registration ----------------------------------------------------
if [ "${PLAN_FORGE_NO_SKILLS:-}" = "1" ]; then
    exit 0
fi

set +e
"$install_dir/plan-forge" install --host auto --scope user --yes
code=$?
set -e
case $code in
    0) ;;
    3)
        printf 'plan-forge is installed, but no supported agent (Claude Code, OMP, Antigravity) was found. Install one, then run: %s/plan-forge install\n' "$install_dir"
        exit 3
        ;;
    *)
        printf 'skill registration failed (exit %s); see the installer output above\n' "$code" >&2
        exit "$code"
        ;;
esac
