package capture

// Package capture provides event capture sources for the DFMT daemon:
//   - Filesystem watcher (inotify on Linux, ReadDirectoryChangesW on Windows)
//   - Git hooks (post-commit, post-checkout, pre-push)
//   - Shell integration (bash, zsh, fish prompt hooks)
//
// Each capture source observes real-world events and translates them into
// DFMT events that are passed to the daemon's journal and index.
