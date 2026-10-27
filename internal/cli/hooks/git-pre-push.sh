#!/bin/sh
# DFMT git pre-push hook
# Records git push events to the DFMT journal

# Run the hook this one took the place of (moved aside by
# `dfmt install-hooks`), with the same arguments and stdin. Its failure is
# this hook's failure, so a pre-push check the user relied on still blocks.
if [ -x "$0.pre-dfmt" ]; then
    "$0.pre-dfmt" "$@" || exit $?
fi

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
