#!/bin/sh
# DFMT git post-commit hook
# Records git commit events to the DFMT journal

# Run the hook this one took the place of (moved aside by
# `dfmt install-hooks`), with the same arguments and stdin. Its failure is
# this hook's failure, so a pre-push check the user relied on still blocks.
if [ -x "$0.pre-dfmt" ]; then
    "$0.pre-dfmt" "$@" || exit $?
fi

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
