#!/usr/bin/env bash
# DFMT Development Script (Linux / macOS / WSL)
#
# Tertemiz reset + build + install + setup + smoke test.
# Default: nukes every dfmt artifact on the system (state, binaries, manifest,
# project state, git hooks) and reinstalls from source.
#
# Usage:
#   ./dev.sh                  # nuke + build + install + setup + init + doctor
#   ./dev.sh -NoClean         # skip the wipe (just build/install/setup)
#   ./dev.sh -SkipSetup       # build/install only, skip agent registration
#   ./dev.sh -SkipInit        # skip `dfmt init` for this project
#   ./dev.sh -InstallHooks    # also install git hooks for this project
#   ./dev.sh -KeepClaude      # do NOT auto-kill running Claude Code processes
#   ./dev.sh -SkipMCPSmoke    # skip the post-build MCP initialize handshake
#   ./dev.sh -SkipDoctor      # skip the post-install `dfmt doctor` health check
#   ./dev.sh -SkipTests       # skip the `go test ./internal/...` gate

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY_NAME="dfmt"
IS_WSL=false
IS_DARWIN=false

# Detect platform
if [[ "$(uname -s)" == "Darwin" ]]; then
    IS_DARWIN=true
    TARGET_DIR="$HOME/.dfmt"
    MANIFEST_DIR="$HOME/.local/share/dfmt"
elif grep -qi 'microsoft\|wsl' /proc/version 2>/dev/null; then
    IS_WSL=true
    TARGET_DIR="$HOME/.dfmt"
    MANIFEST_DIR="$HOME/.local/share/dfmt"
else
    TARGET_DIR="$HOME/.dfmt"
    MANIFEST_DIR="$HOME/.local/share/dfmt"
fi

TARGET_PATH="$TARGET_DIR/$BINARY_NAME"
PROJECT_DFMT="$REPO_ROOT/.dfmt"
DIST_DIR="$REPO_ROOT/dist"
CLAUDE_JSON="$HOME/.claude.json"

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
        -NoClean)       NO_CLEAN=true; shift ;;
        -SkipSetup)     SKIP_SETUP=true; shift ;;
        -SkipInit)      SKIP_INIT=true; shift ;;
        -InstallHooks)  INSTALL_HOOKS=true; shift ;;
        -KeepClaude)    KEEP_CLAUDE=true; shift ;;
        -SkipMCPSmoke)  SKIP_MCP_SMOKE=true; shift ;;
        -SkipDoctor)    SKIP_DOCTOR=true; shift ;;
        -SkipTests)     SKIP_TESTS=true; shift ;;
        *) echo "unknown flag: $1"; exit 1 ;;
    esac
done

# ---- helpers ---------------------------------------------------------------
step()  { echo "==> $1" >&2; }
info()  { echo "    $1" >&2; }
ok()    { echo "    $1" >&2; }
warn()  { echo "    WARNING: $1" >&2; }
err()   { echo "error: $1" >&2; exit 1; }

remove_if_exists() {
    [[ -z "$1" ]] && return
    if [[ -e "$1" ]]; then
        rm -rf "$1" && info "removed $1" || warn "could not remove $1"
    fi
}

# ---- 0. Preflight: Go must be available --------------------------------------
step "Checking Go toolchain..."
if ! command -v go &>/dev/null; then
    err "Go not found in PATH. Install from https://go.dev/dl/ then re-run."
fi
GO_VERSION="$(go version 2>&1)"
info "$GO_VERSION"

# ---- 1. Stop running dfmt processes ----------------------------------------
if [[ "$KEEP_CLAUDE" == false ]]; then
    step "Stopping running Claude Code processes..."
    CLAUDE_PIDS=$(pgrep -x "Claude" 2>/dev/null || pgrep -f "claude" 2>/dev/null || true)
    if [[ -n "$CLAUDE_PIDS" ]]; then
        info "stopping Claude process(es): $CLAUDE_PIDS"
        kill $CLAUDE_PIDS 2>/dev/null || true
        sleep 1
    else
        info "no Claude Code processes running"
    fi
fi

step "Stopping running dfmt processes..."
DFMT_PIDS=$(pgrep -x "dfmt" 2>/dev/null || true)
if [[ -n "$DFMT_PIDS" ]]; then
    info "stopping dfmt process(es): $DFMT_PIDS"
    kill $DFMT_PIDS 2>/dev/null || true
    sleep 1
fi

# ---- 1.5. NUKE prior dfmt state ---------------------------------------------
if [[ "$NO_CLEAN" == false ]]; then
    step "Wiping prior dfmt state..."

    remove_if_exists "$PROJECT_DFMT"
    remove_if_exists "$DIST_DIR"
    remove_if_exists "$TARGET_PATH"
    remove_if_exists "$TARGET_DIR/daemons.json"
    remove_if_exists "$TARGET_DIR/config.yaml"
    remove_if_exists "$TARGET_DIR/journal.jsonl"
    remove_if_exists "$TARGET_DIR/index.gob"
    remove_if_exists "$TARGET_DIR/index.cursor"
    remove_if_exists "$TARGET_DIR/port"
    remove_if_exists "$TARGET_DIR/lock"
    remove_if_exists "$TARGET_DIR/daemon.pid"
    remove_if_exists "$MANIFEST_DIR"

    # git hooks
    for hook in post-commit post-checkout pre-push; do
        hook_path="$REPO_ROOT/.git/hooks/$hook"
        if [[ -f "$hook_path" ]] && grep -q 'dfmt' "$hook_path" 2>/dev/null; then
            rm -f "$hook_path" && info "removed git hook: $hook"
        fi
    done

    # Clean stale Claude Code project entries from ~/.claude.json
    if [[ -f "$CLAUDE_JSON" ]]; then
        if command -v python3 &>/dev/null; then
            python3 "$REPO_ROOT/scripts/cleanup_user_claude_json.go" "$CLAUDE_JSON" 2>/dev/null || true
        fi
    fi

    ok "wipe complete"
else
    info "skipping wipe (-NoClean)"
fi

# ---- 2. Gate tests ----------------------------------------------------------
if [[ "$SKIP_TESTS" == false ]]; then
    step "Running transport + sandbox + config + cli tests (gate)..."
    cd "$REPO_ROOT"
    go test ./internal/transport/... ./internal/sandbox/... ./internal/config/... ./internal/cli/...
    ok "tests pass"
else
    info "skipping tests (-SkipTests)"
fi

# ---- 3. Build to install target ---------------------------------------------
step "Building dfmt to $TARGET_PATH..."
cd "$REPO_ROOT"
mkdir -p "$TARGET_DIR"

GIT_REV="$(git rev-parse --short HEAD 2>/dev/null || echo "dev")"
LDFLAGS="-X github.com/ersinkoc/dfmt/internal/version.Current=v0.2.7-dev"

CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$TARGET_PATH" ./cmd/dfmt

[[ -f "$TARGET_PATH" ]] || err "build failed"
ok "built $TARGET_PATH"

# ---- 4. Verify installed binary runs ----------------------------------------
step "Verifying installed binary..."
"$TARGET_PATH" --version || err "installed dfmt failed to run"

# ---- 5. PATH: ensure $TARGET_DIR is on PATH ----------------------------------
step "Updating shell profile..."
TARGET_DIR_ESCAPED=$(printf '%s' "$TARGET_DIR" | sed 's/[][\.*^$()+?{|\\]/\\&/g')

profile_files=("$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.profile")
PATH_EXPORT_LINE="export PATH=\"\$PATH:$TARGET_DIR\""

path_updated=false
for pf in "${profile_files[@]}"; do
    if [[ -f "$pf" ]]; then
        if ! grep -q "$TARGET_DIR" "$pf" 2>/dev/null; then
            echo "" >> "$pf"
            echo "# Added by dfmt dev.sh" >> "$pf"
            echo "$PATH_EXPORT_LINE" >> "$pf"
            path_updated=true
        fi
    fi
done

# Apply to current session
if [[ ":$PATH:" != *":$TARGET_DIR:"* ]]; then
    export PATH="$PATH:$TARGET_DIR"
fi

ok "PATH updated"

# ---- 6. Configure detected agents -------------------------------------------
if [[ "$SKIP_SETUP" == false ]]; then
    step "Registering dfmt MCP server with detected agents..."
    "$TARGET_PATH" setup --force || err "dfmt setup failed"
    ok "agent setup complete"

    step "Verifying setup wrote expected files..."
    "$TARGET_PATH" setup --verify || warn "some agent config files are missing"

    # ---- MCP smoke test ----------------------------------------------------
    if [[ "$SKIP_MCP_SMOKE" == false ]]; then
        step "Running MCP smoke test (initialize + tools/call)..."
        SMOKE_REQ='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"dev.sh-smoke","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dfmt_stats","arguments":{}}}'

        SMOKE_WD=$(mktemp -d)
        SMOKE_OUT=$(echo "$SMOKE_REQ" | "$TARGET_PATH" mcp 2>&1) || true
        rm -rf "$SMOKE_WD"

        # Parse JSON responses
        INIT_RESP=$(echo "$SMOKE_OUT" | grep '^{' | head -1)
        CALL_RESP=$(echo "$SMOKE_OUT" | grep '^{' | tail -1)

        if [[ -z "$INIT_RESP" ]]; then
            err "MCP smoke: no JSON response for initialize. Raw: $SMOKE_OUT"
        fi

        # Check initialize has capabilities.tools
        if ! echo "$INIT_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print('ok' if d.get('result',{}).get('capabilities',{}).get('tools') else 'fail')" 2>/dev/null | grep -q "ok"; then
            err "MCP initialize response is MISSING capabilities.tools"
        fi
        ok "MCP handshake OK"

        if [[ -z "$CALL_RESP" ]]; then
            err "MCP smoke: no response for tools/call"
        fi

        # Check tools/call result.content is an array
        CONTENT_CHECK=$(echo "$CALL_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); c=d.get('result',{}).get('content',[]); print('ok' if isinstance(c,list) and len(c)>0 else 'fail')" 2>/dev/null || echo "fail")
        if [[ "$CONTENT_CHECK" != "ok" ]]; then
            err "MCP tools/call result.content missing or not an array"
        fi
        ok "MCP tools/call envelope OK"
    else
        info "skipping MCP smoke test (-SkipMCPSmoke)"
    fi
else
    info "skipping setup (-SkipSetup)"
fi

# ---- 7. Initialize the current project ------------------------------------
if [[ "$SKIP_INIT" == false ]]; then
    step "Initializing project at $REPO_ROOT..."
    cd "$REPO_ROOT"
    "$TARGET_PATH" init || warn "dfmt init returned non-zero"
    ok "project initialized"
else
    info "skipping init (-SkipInit)"
fi

# ---- 8. Optional: install git hooks ----------------------------------------
if [[ "$INSTALL_HOOKS" == true ]]; then
    step "Installing git hooks..."
    cd "$REPO_ROOT"
    "$TARGET_PATH" install-hooks || warn "install-hooks failed -- non-fatal"
fi

# ---- 9. Status check --------------------------------------------------------
step "Status:"
"$TARGET_PATH" status || true

# ---- 10. Doctor --------------------------------------------------------------
if [[ "$SKIP_DOCTOR" == false ]]; then
    step "Running doctor..."
    if "$TARGET_PATH" doctor; then
        ok "all health checks passed"
    else
        warn "doctor reported issues"
    fi
else
    info "skipping doctor (-SkipDoctor)"
fi

echo ""
echo "==> Install complete."
echo "    dfmt binary    : $TARGET_PATH"
echo "    MCP command    : resolved via exec.LookPath (absolute, PATH-independent)"
echo ""
echo "    Restart your AI agent to load the dfmt MCP server."
echo "    Health check any time: dfmt doctor"