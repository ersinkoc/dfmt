#!/bin/bash
# DFMT Development Script (Linux / macOS / WSL)
#
# Tertemiz reset + build + install + setup + smoke test.
# Default: nukes every dfmt artifact on the system (state, binaries, manifest,
# project state, git hooks) and reinstalls from source.
#
# Usage:
#   ./dev.sh                    # nuke + build + install + setup + init + doctor
#   ./dev.sh --no-clean         # skip the wipe (just build/install/setup)
#   ./dev.sh --skip-setup      # build/install only, skip agent registration
#   ./dev.sh --skip-init       # skip `dfmt init` for this project
#   ./dev.sh --install-hooks   # also install git hooks for this project
#   ./dev.sh --keep-claude     # do NOT auto-kill running Claude processes
#   ./dev.sh --skip-mcp-smoke  # skip the post-build MCP initialize handshake
#   ./dev.sh --skip-doctor     # skip the post-install `dfmt doctor` health check
#   ./dev.sh --skip-tests     # skip the `go test` gate

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY_NAME="dfmt"
TARGET_DIR="${HOME}/.dfmt"
TARGET_PATH="${TARGET_DIR}/${BINARY_NAME}"
MANIFEST_DIR="${HOME}/.local/share/dfmt"
PROJECT_DFMT="${REPO_ROOT}/.dfmt"
DIST_DIR="${REPO_ROOT}/dist"

# Parse flags
NO_CLEAN=false
SKIP_SETUP=false
SKIP_INIT=false
INSTALL_HOOKS=false
KEEP_CLAUDE=false
SKIP_MCP_SMOKE=false
SKIP_DOCTOR=false
SKIP_TESTS=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-clean)       NO_CLEAN=true ;;
        --skip-setup)     SKIP_SETUP=true ;;
        --skip-init)      SKIP_INIT=true ;;
        --install-hooks)  INSTALL_HOOKS=true ;;
        --keep-claude)    KEEP_CLAUDE=true ;;
        --skip-mcp-smoke) SKIP_MCP_SMOKE=true ;;
        --skip-doctor)    SKIP_DOCTOR=true ;;
        --skip-tests)     SKIP_TESTS=true ;;
        *) echo "unknown flag: $1" >&2; exit 1 ;;
    esac
    shift
done

# ---- Helpers --------------------------------------------------------------
step()  { echo "==> $*" >&2; }
info()  { echo "    $*" >&2; }
ok()    { echo "    $*" >&2; }
warn()  { echo "    $*" >&2; }
err()   { echo "error: $*" >&2; exit 1; }

need()  { command -v "$1" >/dev/null 2>&1 || err "$1 not found in PATH"; }

remove_if_exists() {
    [[ -z "$1" ]] && return
    [[ -e "$1" ]] || return
    if rm -rf "$1" 2>/dev/null; then
        info "removed $1"
    else
        warn "could not remove $1"
    fi
}

is_wsl() {
    if [[ "$(uname -s)" == "Linux" ]] && grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
        return 0
    fi
    return 1
}

# ---- 0. Preflight: Go must be available ------------------------------------
step "Checking Go toolchain..."
need go
go_version=$(go version 2>&1)
info "$go_version"

# Minimum: go1.26.2
if [[ "$go_version" =~ go([0-9]+)\.([0-9]+)(?:\.([0-9]+))? ]]; then
    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[2]}"
    patch="${BASH_REMATCH[3]:-0}"
    too_old=false
    [[ "$major" -lt 1 ]] && too_old=true
    [[ "$major" -eq 1 && "$minor" -lt 26 ]] && too_old=true
    [[ "$major" -eq 1 && "$minor" -eq 26 && "$patch" -lt 2 ]] && too_old=true
    if $too_old; then
        warn "Go $major.$minor.$patch is older than 1.26.2 - stdlib CVEs"
        warn "  GO-2026-4866 / 4870 / 4946 / 4947 (crypto/x509, crypto/tls)"
        warn "  remain unpatched in this build. Upgrade: https://go.dev/dl/"
    fi
else
    warn "could not parse Go version from: $go_version"
fi

# ---- 1. Stop running dfmt/Claude processes ---------------------------------
if ! $KEEP_CLAUDE; then
    step "Stopping running Claude processes..."
    # Try both process names; don't fail if none running
    (pkill -f "Claude Code" 2>/dev/null || pkill -f "claude " 2>/dev/null || true) && sleep 1
    claude_running=false
    if pgrep -f "Claude" >/dev/null 2>&1; then
        claude_running=true
        info "Claude processes stopped"
    else
        info "no Claude processes running"
    fi
else
    info "skipping Claude stop (--keep-claude)"
fi

step "Stopping running dfmt processes..."
pkill -f "dfmt " 2>/dev/null || true
sleep 1

# ---- 1.5. NUKE prior dfmt artifacts ----------------------------------------
if ! $NO_CLEAN; then
    step "Wiping prior dfmt state..."

    remove_if_exists "$PROJECT_DFMT"
    remove_if_exists "$DIST_DIR"
    remove_if_exists "$TARGET_PATH"
    remove_if_exists "${TARGET_DIR}/daemons.json"
    remove_if_exists "${TARGET_DIR}/config.yaml"
    remove_if_exists "${TARGET_DIR}/journal.jsonl"
    remove_if_exists "${TARGET_DIR}/index.gob"
    remove_if_exists "${TARGET_DIR}/index.cursor"
    remove_if_exists "${TARGET_DIR}/port"
    remove_if_exists "${TARGET_DIR}/lock"
    remove_if_exists "${TARGET_DIR}/daemon.pid"
    remove_if_exists "$MANIFEST_DIR"

    # Git hooks installed by `dfmt install-hooks`
    for hook in post-commit post-checkout pre-push; do
        hook_path="${REPO_ROOT}/.git/hooks/${hook}"
        if [[ -f "$hook_path" ]] && grep -q "dfmt" "$hook_path" 2>/dev/null; then
            remove_if_exists "$hook_path"
        fi
    done

    ok "wipe complete"
else
    info "skipping wipe (--no-clean)"
fi

# ---- 2. Gate tests --------------------------------------------------------
if ! $SKIP_TESTS; then
    step "Running transport + sandbox + config + cli tests (gate)..."
    cd "$REPO_ROOT"
    go test ./internal/transport/... ./internal/sandbox/... ./internal/config/... ./internal/cli/...
    ok "transport + sandbox + config + cli tests pass"
else
    info "skipping tests (--skip-tests)"
fi

# ---- 3. Build to install target --------------------------------------------
step "Building dfmt to ${TARGET_PATH}..."
cd "$REPO_ROOT"
mkdir -p "$TARGET_DIR"

git_rev="dev"
git_rev=$(git rev-parse --short HEAD 2>/dev/null || echo "dev")
ldflags="-X github.com/ersinkoc/dfmt/internal/version.Current=v0.2.7-dev"
go build -ldflags "$ldflags" -o "$TARGET_PATH" ./cmd/dfmt
[[ -f "$TARGET_PATH" ]] || err "build failed"
ok "built ${TARGET_PATH}"

# ---- 4. PATH: ensure TARGET_DIR present ------------------------------------
step "Updating shell PATH..."
if [[ ":$PATH:" != *":${TARGET_DIR}:"* ]]; then
    exported=false
    for rc in "${HOME}/.bashrc" "${HOME}/.zshrc" "${HOME}/.profile"; do
        if [[ -f "$rc" ]]; then
            if ! grep -qF "/${BINARY_NAME}" "$rc" 2>/dev/null; then
                echo "export PATH=\"\${PATH}:${TARGET_DIR}\"" >> "$rc"
                exported=true
                info "added ${TARGET_DIR} to $rc"
            fi
        fi
    done
    if $exported; then
        ok "PATH updated in shell profile(s)"
    else
        info "${TARGET_DIR} already in PATH or no writable profile found"
    fi
else
    info "user PATH already correct"
fi

# ---- 5. Verify the installed binary runs ----------------------------------
step "Verifying installed binary..."
"$TARGET_PATH" --version >/dev/null || err "installed dfmt failed to run"

# ---- 6. Configure detected agents ------------------------------------------
if ! $SKIP_SETUP; then
    step "Registering dfmt MCP server with detected agents..."
    "$TARGET_PATH" setup --force || err "dfmt setup failed (exit $?)"

    step "Verifying setup wrote expected files..."
    "$TARGET_PATH" setup --verify || err "setup verification failed"

    # ---- 6b. MCP smoke test -------------------------------------------------
    if ! $SKIP_MCP_SMOKE; then
        step "Running MCP smoke test (initialize + tools/call)..."

        smoke_req='
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"dev.sh-smoke","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dfmt_stats","arguments":{}}}
'
        smoke_wd=$(mktemp -d)
        smoke_out=$("$TARGET_PATH" mcp <<< "$smoke_req" 2>&1) || true
        rm -rf "$smoke_wd"

        init_resp=""
        call_resp=""
        while IFS= read -r line; do
            [[ "$line" =~ ^[[:space:]]*\{ ]] || continue
            parsed=$(echo "$line" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin)))" 2>/dev/null) || continue
            id=$(echo "$parsed" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
            if [[ "$id" == "1" ]]; then
                init_resp="$parsed"
            elif [[ "$id" == "2" ]]; then
                call_resp="$parsed"
            fi
        done <<< "$smoke_out"

        if [[ -z "$init_resp" ]]; then
            err "MCP smoke: no response with id=1 (initialize)"
        fi

        init_result=$(echo "$init_resp" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('result',{})))" 2>/dev/null)
        server_name=$(echo "$init_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('serverInfo',{}).get('name',''))" 2>/dev/null)
        if [[ -z "$server_name" ]]; then
            err "MCP initialize response missing result.serverInfo.name"
        fi

        caps=$(echo "$init_result" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('capabilities',{})))" 2>/dev/null)
        has_tools=$(echo "$caps" | python3 -c "import sys,json; d=json.load(sys.stdin); print('true' if d.get('tools') else 'false')" 2>/dev/null)
        if [[ "$has_tools" != "true" ]]; then
            err "MCP initialize response is MISSING capabilities.tools -- Claude will not load dfmt tools"
        fi
        ok "MCP handshake OK -- serverInfo.name='${server_name}', capabilities.tools present"

        if [[ -z "$call_resp" ]]; then
            err "MCP smoke: no response with id=2 (tools/call)"
        fi

        call_result=$(echo "$call_resp" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('result',{})))" 2>/dev/null)
        content=$(echo "$call_result" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin).get('content',[])))" 2>/dev/null)
        if [[ -z "$content" ]] || [[ "$content" == "null" ]]; then
            err "MCP tools/call result is MISSING the 'content' array"
        fi
        first_type=$(echo "$content" | python3 -c "import sys,json; c=json.load(sys.stdin); print(c[0].get('type','') if c else '')" 2>/dev/null)
        if [[ "$first_type" != "text" ]]; then
            err "MCP tools/call result.content[0].type='${first_type}' -- expected 'text'"
        fi
        ok "MCP tools/call envelope OK -- result.content[0].type='${first_type}'"
    else
        info "skipping MCP smoke test (--skip-mcp-smoke)"
    fi
else
    info "skipping setup (--skip-setup)"
fi

# ---- 7. Initialize the current project -------------------------------------
if ! $SKIP_INIT; then
    step "Initializing project at ${REPO_ROOT}..."
    cd "$REPO_ROOT"
    "$TARGET_PATH" init || err "dfmt init failed (exit $?)"
else
    info "skipping init (--skip-init)"
fi

# ---- 8. Optional: install git hooks ----------------------------------------
if $INSTALL_HOOKS; then
    step "Installing git hooks..."
    cd "$REPO_ROOT"
    "$TARGET_PATH" install-hooks || warn "install-hooks failed -- non-fatal, continuing"
fi

# ---- 9. Show daemon status -------------------------------------------------
step "Status:"
"$TARGET_PATH" status || true

# ---- 10. Doctor -----------------------------------------------------------
if ! $SKIP_DOCTOR; then
    step "Running doctor..."
    if ! "$TARGET_PATH" doctor; then
        warn "doctor reported issues -- review the output above"
    else
        ok "all health checks passed"
    fi
else
    info "skipping doctor (--skip-doctor)"
fi

echo ""
ok "Install complete."
echo "    dfmt binary  : ${TARGET_PATH}"
echo "    MCP command  : pinned (absolute path, PATH-independent)"
echo ""
echo "    Health check any time:    dfmt doctor"
echo "    Setup files only:         dfmt setup --verify"
echo "    Daemon/project status:    dfmt status"
