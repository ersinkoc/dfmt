package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// MaxSandboxReadBytes caps the total bytes sandbox.Read will load into memory
// regardless of the caller's requested limit. Prevents OOM on huge files.
const MaxSandboxReadBytes = 4 * 1024 * 1024 // 4 MiB

// Read implements the Sandbox interface.
func (s *SandboxImpl) Read(ctx context.Context, req ReadReq) (ReadResp, error) {
	// Clean the path to prevent directory traversal
	cleanPath := filepath.Clean(req.Path)

	// Reject null bytes — Go's os.Open rejects them on Windows but on Unix they
	// are valid filename characters, so explicit rejection is defense-in-depth.
	if strings.IndexByte(cleanPath, 0) >= 0 {
		return ReadResp{}, errors.New("path contains null byte")
	}

	// Resolve to an absolute path and require it to sit inside the working
	// directory. Both relative and absolute inputs go through the same check,
	// so /etc/passwd or C:\Windows\... paths cannot slip past a missing rule.
	if s.wd != "" {
		absWd, err := filepath.Abs(s.wd)
		if err != nil {
			return ReadResp{}, fmt.Errorf("resolve working dir: %w", err)
		}
		var absPath string
		if filepath.IsAbs(cleanPath) {
			absPath = cleanPath
		} else {
			absPath = filepath.Join(absWd, cleanPath)
		}
		absPath = filepath.Clean(absPath)
		rel, err := filepath.Rel(absWd, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ReadResp{}, fmt.Errorf("path outside working directory: %s", pathHint(req.Path))
		}
		// Resolve symlinks and re-check containment. A file that sits inside the
		// wd lexically but whose target escapes (e.g. symlink pointing at
		// /etc/passwd) must be refused. EvalSymlinks fails if the file doesn't
		// exist yet; we only validate when it resolves.
		if resolved, rerr := filepath.EvalSymlinks(absPath); rerr == nil {
			resolvedWd, werr := filepath.EvalSymlinks(absWd)
			if werr != nil {
				resolvedWd = absWd
			}
			relResolved, err := filepath.Rel(resolvedWd, resolved)
			if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
				return ReadResp{}, fmt.Errorf("path outside working directory after symlink resolution: %s", pathHint(req.Path))
			}
		}
		cleanPath = absPath
	} else if filepath.IsAbs(cleanPath) {
		// No working directory configured: refuse absolute paths rather than
		// silently trusting whatever policy rules exist.
		return ReadResp{}, errors.New("absolute paths not allowed without working directory")
	}

	// Policy check with the clean path
	if err := s.PolicyCheck("read", cleanPath); err != nil {
		return ReadResp{}, err
	}

	// V-09: open with O_NOFOLLOW (Unix) / FILE_FLAG_OPEN_REPARSE_POINT
	// (Windows) so an attacker who swaps the leaf for a symlink between
	// the lexical EvalSymlinks check above and the open here cannot
	// redirect the read. The check above resolved the path through any
	// pre-existing symlinks and validated containment of the target; the
	// no-follow open then refuses any *new* symlink at the leaf,
	// regardless of where it points. Edit and Write already had this
	// guarantee via safefs.WriteFileAtomic; Read was the gap.
	//
	// Operators with benign within-root symlink leaves who want them
	// followed can resolve the symlink target themselves and pass that
	// path — that's the documented Read contract for the read tier (the
	// looser EnsureResolvedUnder posture, not the strict CheckNoSymlinks
	// posture used by writes).
	f, err := safefs.OpenReadNoFollow(cleanPath)
	if err != nil {
		return ReadResp{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ReadResp{}, err
	}
	if fi.IsDir() {
		return ReadResp{}, fmt.Errorf("cannot read directory: %s", pathHint(req.Path))
	}
	totalSize := fi.Size()

	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return ReadResp{}, err
		}
	}
	readBudget := int64(MaxSandboxReadBytes)
	if req.Limit > 0 && req.Limit < readBudget {
		readBudget = req.Limit
	}
	data, err := io.ReadAll(io.LimitReader(f, readBudget))
	if err != nil {
		return ReadResp{}, err
	}

	content := string(data)
	// Trim a trailing partial UTF-8 rune when readBudget actually clipped the
	// file — without this a multi-byte character cut at the boundary reaches
	// the consumer as invalid UTF-8 (encoding/json emits U+FFFD on marshal).
	if int64(len(data)) >= readBudget && totalSize-req.Offset > readBudget {
		content = trimPartialRune(content)
	}

	// Apply unified return-policy filter; see ApplyReturnPolicy for rules.
	// RawContent preserves the full bytes for the content store.
	filtered := ApplyReturnPolicy(content, req.Intent, req.Return)

	return ReadResp{
		Content:    filtered.Body,
		RawContent: content,
		Matches:    filtered.Matches,
		Summary:    filtered.Summary,
		Size:       totalSize,
		ReadBytes:  int64(len(content)),
	}, nil
}

// MaxFetchBodyBytes caps the size of an HTTP response body that Fetch will read.
