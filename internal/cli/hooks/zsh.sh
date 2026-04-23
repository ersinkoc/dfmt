#!/bin/zsh
# DFMT zsh integration
# Add to ~/.zshrc

# Precmd hook to record directory changes
dfmt_precmd_hook() {
    if command -v dfmt >/dev/null 2>&1; then
        dfmt capture env.cwd "$PWD" 2>/dev/null
    fi
}

# Add to precmd
autoload -Uz add-zsh-hook
add-zsh-hook precmd dfmt_precmd_hook

# Alias dfmt if not in PATH
if ! command -v dfmt >/dev/null 2>&1; then
    if [ -f "$HOME/.local/bin/dfmt" ]; then
        export PATH="$HOME/.local/bin:$PATH"
    fi
fi
