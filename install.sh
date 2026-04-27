#!/bin/sh
# DFMT one-command installer (Linux/macOS/FreeBSD).
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.sh | sh
#
# Environment:
#   DFMT_VERSION=v0.1.0       pin a specific release tag (default: latest)
#   DFMT_INSTALL_DIR=/path    override install directory (default: ~/.local/bin)
#   DFMT_FROM_SOURCE=1        skip binary download, go install from source
#   DFMT_DEBUG=1              verbose shell tracing

set -eu

if [ "${DFMT_DEBUG:-0}" = "1" ]; then
    set -x
fi

REPO_OWNER="ersinkoc"
REPO_NAME="dfmt"
RELEASE_TAG="${DFMT_VERSION:-latest}"

# --- colors (only when stdout is a TTY) ------------------------------------
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    C_RED=$(tput setaf 1); C_GRN=$(tput setaf 2); C_YEL=$(tput setaf 3)
    C_CYA=$(tput setaf 6); C_BLD=$(tput bold); C_RST=$(tput sgr0)
else
    C_RED=""; C_GRN=""; C_YEL=""; C_CYA=""; C_BLD=""; C_RST=""
fi

info() { printf "%s%s==>%s %s\n" "$C_BLD" "$C_CYA" "$C_RST" "$*"; }
warn() { printf "%s%swarn:%s %s\n" "$C_BLD" "$C_YEL" "$C_RST" "$*" >&2; }
die()  { printf "%s%serror:%s %s\n" "$C_BLD" "$C_RED" "$C_RST" "$*" >&2; exit 1; }

# --- detect OS / arch ------------------------------------------------------
uname_s=$(uname -s 2>/dev/null || echo unknown)
uname_m=$(uname -m 2>/dev/null || echo unknown)

case "$uname_s" in
    Linux)   OS=linux ;;
    Darwin)  OS=darwin ;;
    FreeBSD) OS=freebsd ;;
    *) die "unsupported OS: $uname_s" ;;
esac

case "$uname_m" in
    x86_64|amd64)       ARCH=amd64 ;;
    aarch64|arm64)      ARCH=arm64 ;;
    *) die "unsupported arch: $uname_m" ;;
esac

# freebsd only ships amd64
if [ "$OS" = "freebsd" ] && [ "$ARCH" != "amd64" ]; then
    die "no prebuilt binary for freebsd/$ARCH. Set DFMT_FROM_SOURCE=1 to build from source."
fi

ASSET="dfmt-${OS}-${ARCH}"

# --- install directory -----------------------------------------------------
if [ -n "${DFMT_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$DFMT_INSTALL_DIR"
else
    INSTALL_DIR="$HOME/.local/bin"
fi
mkdir -p "$INSTALL_DIR"

# --- downloader ------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
    dl() { curl -fsSL --retry 3 --connect-timeout 10 -o "$1" "$2"; }
elif command -v wget >/dev/null 2>&1; then
    dl() { wget -q -O "$1" "$2"; }
else
    dl() { return 127; }
fi

# --- checksum verifier -----------------------------------------------------
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        echo ""
    fi
}

# --- binary download path --------------------------------------------------
download_binary() {
    if [ "$RELEASE_TAG" = "latest" ]; then
        base="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download"
    else
        base="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${RELEASE_TAG}"
    fi

    tmp=$(mktemp -d 2>/dev/null || mktemp -d -t dfmt)
    trap 'rm -rf "$tmp"' EXIT INT TERM

    info "fetching ${ASSET} from release ${RELEASE_TAG}"
    if ! dl "$tmp/$ASSET" "$base/$ASSET"; then
        return 1
    fi

    # checksum is best-effort; only fail on mismatch, not on missing
    if dl "$tmp/sha256sums.txt" "$base/sha256sums.txt" 2>/dev/null; then
        expected=$(grep " ${ASSET}$" "$tmp/sha256sums.txt" 2>/dev/null | awk '{print $1}' | head -n1 || true)
        actual=$(sha256_of "$tmp/$ASSET")
        if [ -n "$expected" ] && [ -n "$actual" ] && [ "$expected" != "$actual" ]; then
            die "checksum mismatch for $ASSET (expected $expected, got $actual)"
        fi
        if [ -n "$expected" ] && [ -n "$actual" ]; then
            info "sha256 verified"
        fi
    fi

    chmod +x "$tmp/$ASSET"
    mv "$tmp/$ASSET" "$INSTALL_DIR/dfmt"
    info "installed to $INSTALL_DIR/dfmt"
    return 0
}

# --- source fallback -------------------------------------------------------
install_from_source() {
    if ! command -v go >/dev/null 2>&1; then
        die "source fallback needs Go toolchain. Install Go (https://go.dev/dl/) or set DFMT_VERSION to a published release."
    fi
    info "building from source via 'go install'"
    if ! go install "github.com/${REPO_OWNER}/${REPO_NAME}/cmd/dfmt@latest"; then
        die "go install failed. Re-run with DFMT_DEBUG=1 for details."
    fi
    gobin=$(go env GOBIN 2>/dev/null || true)
    if [ -z "$gobin" ]; then
        gopath=$(go env GOPATH 2>/dev/null || true)
        [ -n "$gopath" ] && gobin="$gopath/bin"
    fi
    if [ -n "$gobin" ] && [ -x "$gobin/dfmt" ] && [ "$gobin" != "$INSTALL_DIR" ]; then
        cp "$gobin/dfmt" "$INSTALL_DIR/dfmt"
        info "copied $gobin/dfmt -> $INSTALL_DIR/dfmt"
    fi
}

# --- main ------------------------------------------------------------------
if [ "${DFMT_FROM_SOURCE:-0}" = "1" ]; then
    install_from_source
else
    if ! download_binary; then
        warn "binary download failed; trying source build"
        install_from_source
    fi
fi

if [ ! -x "$INSTALL_DIR/dfmt" ]; then
    die "dfmt binary not found at $INSTALL_DIR/dfmt after install"
fi

# --- PATH check ------------------------------------------------------------
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        warn "$INSTALL_DIR is not on your PATH."
        warn "Add it to your shell profile, e.g.:"
        warn "    echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile"
        ;;
esac

# --- dfmt setup ------------------------------------------------------------
info "running 'dfmt setup --force' to wire up detected agents"
if ! "$INSTALL_DIR/dfmt" setup --force; then
    warn "'dfmt setup' returned non-zero. You can re-run it later."
fi

# --- success banner --------------------------------------------------------
cat <<EOF

${C_BLD}${C_GRN}DFMT installed.${C_RST}

Next steps:
  ${C_BLD}1.${C_RST}  cd /path/to/your/project
  ${C_BLD}2.${C_RST}  dfmt quickstart      ${C_CYA}# init + per-agent setup + verify${C_RST}
  ${C_BLD}3.${C_RST}  restart your AI agent (Claude Code, Cursor, VS Code, Codex,
      Gemini, Windsurf, Zed, Continue, OpenCode — whichever you use)

Docs:  https://github.com/${REPO_OWNER}/${REPO_NAME}
EOF
