package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// V-14 (F-Setup-4): trust-flag prior-value tracking.
//
// PatchClaudeCodeUserJSON flips three keys on each project entry to true:
//
//   - hasTrustDialogAccepted
//   - hasClaudeMdExternalIncludesApproved
//   - hasClaudeMdExternalIncludesWarningShown
//
// Pre-fix UnpatchClaudeCodeUserJSON deliberately left the flags in place
// because DFMT could not tell whether the user had accepted them
// independently of our setup. The audit flagged this as a silent
// permission-elevation that survives uninstall (the user keeps the
// elevated `@external` include permission they may not have agreed to).
//
// Fix: at first patch, capture the prior value of each flag (or its
// absence) into a sidecar file, then restore on unpatch. Capture is
// idempotent — a re-run that finds an existing capture for a project
// leaves it alone, so back-to-back patches don't overwrite the original
// state with the now-flipped state.

// claudeTrustFlags is the closed set of keys Patch flips. Restore walks
// exactly this list so a future user-set key (added by Claude itself)
// is never misclassified as "owned by dfmt".
var claudeTrustFlags = []string{
	"hasTrustDialogAccepted",
	"hasClaudeMdExternalIncludesApproved",
	"hasClaudeMdExternalIncludesWarningShown",
}

// claudeTrustPriors captures the pre-patch value of each trust flag per
// project. Inner-map presence semantics:
//
//   - key present with value true/false: flag had that value before patch
//   - key absent from inner map: flag was not present in the entry at all
//
// Distinguishing "absent" from "false" matters because Restore must
// delete-the-key for "absent" (to leave the entry exactly as it was)
// versus set-to-false for "false".
type claudeTrustPriors struct {
	Version  int                       `json:"version"`
	Projects map[string]map[string]bool `json:"projects"`
}

// claudeTrustPriorPath returns the sidecar path. Same parent dir as the
// setup manifest so a hostile actor with write access to one already has
// write access to the other — no new attack surface is introduced.
func claudeTrustPriorPath() string {
	mp := ManifestPath()
	if mp == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(mp), "claude-trust-prior.json")
}

func loadClaudeTrustPriors() (*claudeTrustPriors, error) {
	path := claudeTrustPriorPath()
	if path == "" {
		return &claudeTrustPriors{Version: 1, Projects: map[string]map[string]bool{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &claudeTrustPriors{Version: 1, Projects: map[string]map[string]bool{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var p claudeTrustPriors
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Projects == nil {
		p.Projects = map[string]map[string]bool{}
	}
	return &p, nil
}

func saveClaudeTrustPriors(p *claudeTrustPriors) error {
	path := claudeTrustPriorPath()
	if path == "" {
		return errors.New("claude trust prior path unresolved")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return safefs.WriteFileAtomic(dir, path, out, 0o600)
}

// captureClaudeTrustPriors records the current state of every flag in
// claudeTrustFlags for projectKey, but only if no capture exists for
// that project yet. The first patch wins — a re-patch sees the already-
// flipped flags and would mis-record "true" as the prior state. Returns
// the in-memory priors object for the caller to save.
func captureClaudeTrustPriors(p *claudeTrustPriors, projectKey string, entry map[string]any) {
	if _, already := p.Projects[projectKey]; already {
		return
	}
	captured := map[string]bool{}
	for _, flag := range claudeTrustFlags {
		v, ok := entry[flag]
		if !ok {
			continue // omitted from inner map → restore deletes the key
		}
		if b, isBool := v.(bool); isBool {
			captured[flag] = b
		}
		// Non-bool values (shouldn't happen — Claude always writes bools)
		// are skipped; restore will treat them as "absent" and delete.
	}
	p.Projects[projectKey] = captured
}

// restoreClaudeTrustPriors applies the saved prior state to entry,
// deleting flags that were originally absent and resetting flags to
// their original true/false values. Returns true if any restore was
// applied (so the caller knows to write the file back).
func restoreClaudeTrustPriors(p *claudeTrustPriors, projectKey string, entry map[string]any) bool {
	priors, ok := p.Projects[projectKey]
	if !ok {
		return false
	}
	for _, flag := range claudeTrustFlags {
		if v, present := priors[flag]; present {
			entry[flag] = v
		} else {
			delete(entry, flag)
		}
	}
	delete(p.Projects, projectKey)
	return true
}

// removeClaudeTrustPriorFile deletes the sidecar after a successful
// uninstall. Missing file is a no-op.
func removeClaudeTrustPriorFile() error {
	path := claudeTrustPriorPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
