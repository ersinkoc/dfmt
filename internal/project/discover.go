package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Discover finds the project root for the given path.
// It walks up looking for .dfmt/ or .git/ directories.
// Honors DFMT_PROJECT env var.
func Discover(path string) (string, error) {
	// Honor DFMT_PROJECT env var
	if envPath := os.Getenv("DFMT_PROJECT"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return filepath.Abs(envPath)
		}
	}

	// Walk up from path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	for {
		// Check for .dfmt directory
		dfmtPath := filepath.Join(absPath, ".dfmt")
		if _, err := os.Stat(dfmtPath); err == nil {
			return absPath, nil
		}

		// Check for .git directory
		gitPath := filepath.Join(absPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return absPath, nil
		}

		// Move to parent
		parent := filepath.Dir(absPath)
		if parent == absPath {
			// Reached root
			break
		}
		absPath = parent
	}

	return "", ErrNoProjectFound
}

var ErrNoProjectFound = &NoProjectError{}

// NoProjectError indicates no project root was found.
type NoProjectError struct{}

func (e *NoProjectError) Error() string {
	return "no DFMT project found (no .dfmt or .git directory in parent tree)"
}

// ID computes the project ID (8 hex chars of SHA-256 of the path).
func ID(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	return hex.EncodeToString(h[:4])
}

// SocketPath returns the socket path for a project.
func SocketPath(projectPath string) string {
	return filepath.Join(projectPath, ".dfmt", "daemon.sock")
}
