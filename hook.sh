#!/bin/bash
# DFMT bash integration
# Add to ~/.bashrc or ~/.bash_profile

# Prompt hook to record directory changes
PROMPT_COMMAND="dfmt_prompt_hook"

dfmt_prompt_hook() {
    if command -v dfmt >/dev/null 2>&1; then
        dfmt capture env.cwd "$PWD" 2>/dev/null
    fi
}

# Alias dfmt if not in PATH
if ! command -v dfmt >/dev/null 2>&1; then
    if [ -f "$HOME/.local/bin/dfmt" ]; then
        export PATH="$HOME/.local/bin:$PATH"
    fi
fi

