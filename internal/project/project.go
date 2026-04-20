package project

import (
	"os"
	"path/filepath"
)

const (
	// SocketName is the Unix socket name.
	SocketName = "daemon.sock"
	// PIDFileName is the daemon PID file name.
	PIDFileName = "daemon.pid"
	// ConfigFileName is the config file name.
	ConfigFileName = "config.yaml"
	// JournalFileName is the journal file name.
	JournalFileName = "journal.jsonl"
	// IndexFileName is the index file name.
	IndexFileName = "index.gob"
	// CursorFileName is the cursor file name.
	CursorFileName = "index.cursor"
)

// DaemonDir returns the .dfmt directory path for a project.
func DaemonDir(projectPath string) string {
	return filepath.Join(projectPath, ".dfmt")
}

// EnsureDir ensures a directory exists, creating it if necessary.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}
