package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

func (s *SandboxImpl) Edit(ctx context.Context, req EditReq) (EditResp, error) {
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return EditResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Resolve file path
	cleanPath := req.Path
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(absWd, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix
	// they are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return EditResp{}, errors.New("path contains null byte")
	}

	// Verify path is within working directory
	rel, err := filepath.Rel(absWd, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return EditResp{}, fmt.Errorf("path outside working directory: %s", pathHint(req.Path))
	}

	// Refuse if any path segment beneath wd is a symlink (closes F-04 for the
	// Edit path, including the target-missing case where the previous
	// EvalSymlinks-only gate returned without checking).
	if err := safefs.CheckNoSymlinks(absWd, cleanPath); err != nil {
		return EditResp{}, fmt.Errorf("path symlink check: %w", err)
	}

	// V-15: refuse Windows reserved device names (CON, PRN, AUX, NUL,
	// COM0-9, LPT0-9 and their `.ext` forms). The journal and transport
	// layers already gate on this; the sandbox Edit path was the gap.
	// Active on every host, not just Windows — NTFS via CIFS/WSL/SFM
	// produces the same surprise dirs on Linux.
	if err := safefs.CheckNoReservedNames(cleanPath); err != nil {
		return EditResp{}, fmt.Errorf("path reserved-name check: %w", err)
	}

	// Policy check — Edit is both a write and an edit. Run BOTH checks so
	// that:
	//   - existing `Op: "write"` deny rules continue to protect Edit calls
	//     (they have always done so via this code path);
	//   - explicit `Op: "edit"` deny rules in user policies actually fire
	//     (closes F-29: the DefaultPolicy carries `edit` mirrors of every
	//     `write` deny but Edit only invoked PolicyCheck("write"), making
	//     the `edit` rules dead in the default config).
	if err := s.PolicyCheck("write", cleanPath); err != nil {
		return EditResp{}, err
	}
	if err := s.PolicyCheck("edit", cleanPath); err != nil {
		return EditResp{}, err
	}

	// Read current content
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return EditResp{}, fmt.Errorf("read file: %w", err)
	}
	content := string(data)

	// Check if old string exists
	if !strings.Contains(content, req.OldString) {
		return EditResp{}, fmt.Errorf("old string not found in file: %s", pathHint(req.Path))
	}

	// Replace
	newContent := strings.Replace(content, req.OldString, req.NewString, 1)

	// Write back, preserving original mode where possible. We re-stat instead
	// of trusting WriteFileAtomic's perm arg (which only takes effect on
	// create) so a 0600 secrets file edited by an agent keeps its 0600 mode.
	//
	// WriteFileAtomic (tmp + rename) closes the F-R-LOW-1 TOCTOU window
	// from the security audit: the previous WriteFile path was Lstat-then-
	// open, so an attacker who could swap `cleanPath` for a symlink between
	// the CheckNoSymlinks call above and the open could still write through
	// that symlink. Rename(2) replaces the symlink as a directory entry
	// rather than following it, so the race window is closed at the cost
	// of breaking pre-existing hard links to the target — an acceptable
	// trade-off for an agent-driven editor.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(cleanPath); ferr == nil {
		mode = fi.Mode().Perm()
	}
	if err := safefs.WriteFileAtomic(absWd, cleanPath, []byte(newContent), mode); err != nil {
		return EditResp{}, fmt.Errorf("write file: %w", err)
	}

	return EditResp{
		Success: true,
		Summary: fmt.Sprintf("Replaced string in %s", req.Path),
	}, nil
}

// Write implements the Sandbox interface.
func (s *SandboxImpl) Write(ctx context.Context, req WriteReq) (WriteResp, error) {
	wd := s.wd
	if wd == "" {
		wd = "."
	}

	absWd, err := filepath.Abs(wd)
	if err != nil {
		return WriteResp{}, fmt.Errorf("resolve working directory: %w", err)
	}

	// Resolve file path
	cleanPath := req.Path
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(absWd, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix
	// they are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return WriteResp{}, errors.New("path contains null byte")
	}

	// Verify path is within working directory
	rel, err := filepath.Rel(absWd, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return WriteResp{}, fmt.Errorf("path outside working directory: %s", pathHint(req.Path))
	}

	// Refuse if any path segment beneath wd is a symlink (closes F-04). The
	// previous EvalSymlinks-only gate skipped the check entirely when the
	// target file didn't exist, which let an attacker plant
	// `wd/leak -> /etc/cron.d/x` and then have the agent write through it.
	// safefs.CheckNoSymlinks Lstat-walks each component so missing-leaf
	// cases still reject symlinked parents.
	if err := safefs.CheckNoSymlinks(absWd, cleanPath); err != nil {
		return WriteResp{}, fmt.Errorf("path symlink check: %w", err)
	}

	// V-15: refuse Windows reserved device names (CON, PRN, AUX, NUL,
	// COM0-9, LPT0-9 and their `.ext` forms). Active on every host —
	// NTFS via CIFS/WSL/SFM produces the same surprise dirs on Linux.
	if err := safefs.CheckNoReservedNames(cleanPath); err != nil {
		return WriteResp{}, fmt.Errorf("path reserved-name check: %w", err)
	}

	// Policy check - write permission
	if err := s.PolicyCheck("write", cleanPath); err != nil {
		return WriteResp{}, err
	}

	// Ensure parent directory exists. Use 0o700 so newly-created intermediate
	// directories aren't world-readable on multi-user hosts. The .dfmt
	// directory itself is 0o700 — sandbox writes follow the same hygiene.
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return WriteResp{}, fmt.Errorf("create directory: %w", err)
	}

	// Write file with owner-only permissions for new files. If the file
	// already exists, preserve its mode (an agent shouldn't widen access on
	// an existing file by overwriting it).
	//
	// WriteFileAtomic (tmp + rename) closes the F-R-LOW-1 TOCTOU window
	// from the security audit: rename(2) replaces a symlink that an
	// attacker raced into the leaf position rather than following it.
	// Trade-off: any hard links to a pre-existing target are broken on
	// overwrite — acceptable for an agent-driven file writer.
	mode := os.FileMode(0o600)
	if fi, ferr := os.Stat(cleanPath); ferr == nil {
		mode = fi.Mode().Perm()
	}
	if err := safefs.WriteFileAtomic(absWd, cleanPath, []byte(req.Content), mode); err != nil {
		return WriteResp{}, fmt.Errorf("write file: %w", err)
	}

	return WriteResp{
		Success: true,
		Summary: fmt.Sprintf("Wrote %d bytes to %s", len(req.Content), req.Path),
	}, nil
}
