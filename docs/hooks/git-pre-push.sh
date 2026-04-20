#!/bin/sh
# DFMT git pre-push hook
# Records git push events to the DFMT journal

# Get remote and branch
REMOTE="$1"
BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null)

if [ -z "$REMOTE" ] || [ -z "$BRANCH" ]; then
    exit 0
fi

# Call dfmt capture if available
if command -v dfmt >/dev/null 2>&1; then
    dfmt capture git push "$REMOTE" "$BRANCH" 2>/dev/null &
fi

exit 0
