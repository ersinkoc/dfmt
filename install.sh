#!/bin/sh
# DFMT one-command installer (Linux/macOS).
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ersinkoc/dfmt/main/install.sh | sh
#
# Environment:
#   DFMT_DEBUG=1  - print every command as it runs.

set -eu

if [ "${DFMT_DEBUG:-0}" = "1" ]; then
    set -x
fi

REPO="github.com/ersinkoc/dfmt/cmd/dfmt@latest"

# --- colors (only when stdout is a TTY) ------------------------------------
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    C_RED=$(tput setaf 1); C_GRN=$(tput setaf 2); C_YEL=$(tput setaf 3)
    C_CYA=$(tput setaf 6); C_BLD=$(tput bold); C_RST=$(tput sgr0)
else
    C_RED=""; C_GRN=""; C_YEL=""; C_CYA=""; C_BLD=""; C_RST=""
fi

say()  { printf "%s\n" "$*"; }
info() { printf "%s%s==>%s %s\n" "$C_BLD" "$C_CYA" "$C_RST" "$*"; }
warn() { printf "%s%swarn:%s %s\n" "$C_BLD" "$C_YEL" "$C_RST" "$*" >&2; }
die()  { printf "%s%serror:%s %s\n" "$C_BLD" "$C_RED" "$C_RST" "$*" >&2; exit 1; }

# --- step 1: go available? -------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
    cat >&2 <<EOF
${C_BLD}${C_RED}error:${C_RST} Go toolchain not found on PATH.

DFMT installs via \`go install\`. Install Go first:

  macOS:  brew install go
  Linux:  https://go.dev/dl/  (or your distro's package manager)

Then re-run this installer.
EOF
    exit 1
fi

GO_VERSION=$(go version 2>/dev/null || echo "go version unknown")
info "using ${GO_VERSION}"

# --- step 2: go install ----------------------------------------------------
info "installing dfmt from ${REPO}"
if ! go install "$REPO"; then
    die "go install failed. Re-run with DFMT_DEBUG=1 for details."
fi

# --- step 3: locate binary -------------------------------------------------
GOBIN=$(go env GOBIN 2>/dev/null || true)
if [ -z "$GOBIN" ]; then
    GOPATH=$(go env GOPATH 2>/dev/null || true)
    [ -n "$GOPATH" ] && GOBIN="$GOPATH/bin"
fi

if ! command -v dfmt >/dev/null 2>&1; then
    if [ -n "$GOBIN" ] && [ -x "$GOBIN/dfmt" ]; then
        warn "dfmt installed to $GOBIN but that directory is not on your PATH."
        warn "Add it to your shell profile, e.g.:"
        warn "    echo 'export PATH=\"$GOBIN:\$PATH\"' >> ~/.profile"
    else
        die "dfmt binary not found after install. Check go env GOBIN / GOPATH."
    fi
else
    info "dfmt binary: $(command -v dfmt)"
fi

# --- step 4: dfmt setup ----------------------------------------------------
info "running 'dfmt setup --force' to wire up detected agents"
if command -v dfmt >/dev/null 2>&1; then
    if ! dfmt setup --force; then
        warn "'dfmt setup' returned non-zero. You can re-run it later."
    fi
else
    # Try the GOBIN path explicitly.
    if [ -n "$GOBIN" ] && [ -x "$GOBIN/dfmt" ]; then
        if ! "$GOBIN/dfmt" setup --force; then
            warn "'dfmt setup' returned non-zero. You can re-run it later."
        fi
    fi
fi

# --- step 5: success banner ------------------------------------------------
cat <<EOF

${C_BLD}${C_GRN}DFMT installed.${C_RST}

Next steps:
  ${C_BLD}1.${C_RST}  cd /path/to/your/project
  ${C_BLD}2.${C_RST}  dfmt init
  ${C_BLD}3.${C_RST}  restart Claude Code (or your agent) and you're done

Docs:  https://github.com/ersinkoc/dfmt
EOF
