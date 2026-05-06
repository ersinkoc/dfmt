package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// MergeMCPServerEntry reads the JSON file at mcpPath (treating ENOENT as an
// empty object), splices `mcpServers.dfmt` into the existing config while
// preserving every other top-level key and every other entry inside
// `mcpServers`, then writes the result atomically via safefs.WriteFileAtomic.
//
// Closes V-04: the prior writers replaced the file outright with
// `{"mcpServers":{"dfmt":{...}}}`, silently destroying any other MCP servers
// (playwright, context7, github, …) the user had configured for that agent.
// Only the new ~/.claude.json writer (PatchClaudeCodeUserJSON) was merge-aware
// before this fix.
//
// V-20: the write goes through safefs so symlink-planting on the parent path
// or at the file itself cannot redirect the write outside the intended
// directory.
//
// On the first patch a `<path>.dfmt.bak` backup is captured (skipped if one
// already exists, so a re-run can't clobber the pristine copy).
//
// target controls the form of the embedded `command` string the same way
// claude.go does: TargetOSWindows produces a Windows-style command path,
// TargetOSUnix produces a Unix-style path even on Windows hosts.
func MergeMCPServerEntry(mcpPath string, target TargetOS) error {
	cfg, raw, existed, err := readJSONObject(mcpPath)
	if err != nil {
		return err
	}

	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["dfmt"] = dfmtMCPServerEntryForTarget(target)
	cfg["mcpServers"] = servers

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}
	out = append(out, '\n')

	if existed {
		if err := writeBackupOnce(mcpPath, raw); err != nil {
			return err
		}
	}

	dir := filepath.Dir(mcpPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := safefs.WriteFileAtomic(dir, mcpPath, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", mcpPath, err)
	}
	return nil
}

// UnmergeMCPServerEntry removes only the `dfmt` entry from `mcpServers` while
// leaving every other key and server intact. If the resulting `mcpServers`
// map is empty it is removed (so the file doesn't grow `"mcpServers": {}`
// stubs across reinstall cycles); if the resulting top-level object is empty
// the file is deleted entirely. Missing file is a no-op.
func UnmergeMCPServerEntry(mcpPath string) error {
	cfg, _, existed, err := readJSONObject(mcpPath)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}

	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}
	if _, present := servers["dfmt"]; !present {
		return nil
	}
	delete(servers, "dfmt")
	if len(servers) == 0 {
		delete(cfg, "mcpServers")
	} else {
		cfg["mcpServers"] = servers
	}

	if len(cfg) == 0 {
		if err := os.Remove(mcpPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", mcpPath, err)
		}
		return nil
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(mcpPath)
	if err := safefs.WriteFileAtomic(dir, mcpPath, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", mcpPath, err)
	}
	return nil
}

// readJSONObject reads path as a JSON object. Missing file returns an empty
// object with existed=false. Empty (whitespace-only) file returns an empty
// object with existed=true so the caller can still write a backup.
func readJSONObject(path string) (cfg map[string]any, raw []byte, existed bool, err error) {
	cfg = map[string]any{}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return cfg, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	existed = true
	raw = data
	if strings.TrimSpace(string(data)) == "" {
		return cfg, raw, existed, nil
	}
	if uerr := json.Unmarshal(data, &cfg); uerr != nil {
		return nil, nil, false, fmt.Errorf("parse %s: %w", path, uerr)
	}
	return cfg, raw, existed, nil
}

// writeBackupOnce captures the pristine pre-DFMT bytes at <path>.dfmt.bak the
// first time a config is patched. A second call with the backup already in
// place is a no-op (so a re-run preserves the original capture). Routed
// through safefs to keep the F-07 symlink-plant variant closed.
func writeBackupOnce(path string, raw []byte) error {
	backupPath := path + ".dfmt.bak"
	if _, statErr := os.Stat(backupPath); statErr == nil {
		return nil
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat backup %s: %w", backupPath, statErr)
	}
	if err := safefs.WriteFile(filepath.Dir(path), backupPath, raw, 0o600); err != nil {
		return fmt.Errorf("write backup %s: %w", backupPath, err)
	}
	return nil
}
