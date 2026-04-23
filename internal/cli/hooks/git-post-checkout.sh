#!/bin/sh
# DFMT git post-checkout hook
# Records git checkout events to the DFMT journal

# Get the checkout target
if [ -n "$1" ]; then
    REF="$1"
else
    exit 0
fi

# Determine if it's a branch or file checkout
IS_BRANCH=false
if git show-ref --verify --quiet "refs/heads/$REF" 2>/dev/null; then
    IS_BRANCH=true
fi

# Call dfmt capture if available
if command -v dfmt >/dev/null 2>&1; then
    dfmt capture git checkout "$REF" "$IS_BRANCH" 2>/dev/null &
fi

exit 0
