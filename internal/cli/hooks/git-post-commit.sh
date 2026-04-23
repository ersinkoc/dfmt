#!/bin/sh
# DFMT git post-commit hook
# Records git commit events to the DFMT journal

# Get the commit hash
COMMIT_HASH=$(git rev-parse HEAD 2>/dev/null)
if [ -z "$COMMIT_HASH" ]; then
    exit 0
fi

# Get the commit message
COMMIT_MSG=$(git log -1 --format=%s 2>/dev/null)

# Call dfmt capture if available
if command -v dfmt >/dev/null 2>&1; then
    dfmt capture git commit "$COMMIT_HASH" "$COMMIT_MSG" 2>/dev/null &
fi

exit 0
