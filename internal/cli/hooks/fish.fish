# DFMT fish shell integration
# Add to ~/.config/fish/config.fish

# Prompt hook to record directory changes
function dfmt_prompt --on-variable PWD
    if command -v dfmt >/dev/null 2>&1
        dfmt capture env.cwd "$PWD" 2>/dev/null
    end
end

# Alias dfmt if not in PATH
if not command -v dfmt >/dev/null 2>&1
    if test -f "$HOME/.local/bin/dfmt"
        set -gx PATH "$HOME/.local/bin" $PATH
    end
end
