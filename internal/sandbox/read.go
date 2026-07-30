package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ersinkoc/dfmt/internal/safefs"
)

// maxReadLineBytes caps a single line the scanner will return. Minified
// bundles and one-line JSON dumps are the reason it is generous; beyond it
// the scan fails rather than silently truncating, which is the same contract
// the journal reader uses.
const maxReadLineBytes = 1 << 20

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
		return ReadResp{}, ErrPathContainsNullByte
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

	// Line-oriented windowing.
	//
	// Offset and Limit used to be BYTES, which is a unit no agent thinks in:
	// to look at a function it knew started around line 400, it had to guess
	// a byte offset, and nothing in the response said which lines came back.
	// The practical result was a fall back to the native Read tool for any
	// real code work — measured on this very review. Offset is now a 1-based
	// starting line (0 and 1 both mean "from the top") and Limit a line
	// count, matching what every editor, stack trace, and code-review tool
	// already agrees on.
	//
	// Deliberately NOT done: prefixing each line with its number. Numbered
	// content is a trap for the next step in the loop — an agent that copies
	// an anchor out of it builds an old_string that cannot match the file,
	// and dfmt_edit would refuse (or worse, with the old first-match
	// behaviour, silently hit the wrong site). The line numbers travel in
	// the response metadata and in Matches[].Line instead.
	startLine := int(req.Offset)
	if startLine < 1 {
		startLine = 1
	}
	limit := int(req.Limit)

	content, window, err := readLineWindow(f, totalSize, startLine, limit)
	if err != nil {
		return ReadResp{}, err
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
		StartLine:  window.start,
		EndLine:    window.end,
		TotalLines: window.total,
	}, nil
}

// lineWindow describes which part of a file a read returned. total is 0 when
// the scan stopped before the end of the file, because at that point the
// count is genuinely unknown and reporting a partial count as the total
// would be worse than saying nothing.
type lineWindow struct {
	start int
	end   int
	total int
}

// readLineWindow returns the requested line range as text plus the window it
// covers. It streams rather than slurping: skipping to line 4000 of a large
// log costs I/O but not memory, which is what preserves access to files
// bigger than MaxSandboxReadBytes.
//
// The trailing newline of the last line is preserved only if the source had
// one, so a full read round-trips byte-for-byte — anything else would break
// dfmt_edit anchors taken from the end of a file.
func readLineWindow(f *os.File, totalSize int64, startLine, limit int) (string, lineWindow, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxReadLineBytes)

	var b strings.Builder
	w := lineWindow{start: startLine}
	lineNo := 0
	collected := 0
	reachedEOF := true

	for sc.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if limit > 0 && collected >= limit {
			reachedEOF = false
			break
		}
		line := sc.Text()
		if b.Len()+len(line)+1 > MaxSandboxReadBytes {
			reachedEOF = false
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		collected++
		w.end = lineNo
	}
	if err := sc.Err(); err != nil {
		return "", w, err
	}
	if reachedEOF {
		w.total = lineNo
	}
	if collected == 0 {
		// Offset past the end of the file: an empty window, not an error.
		// The caller asked a coherent question and the answer is "nothing
		// there"; total (when known) tells it how far the file actually goes.
		w.start, w.end = 0, 0
		return "", w, nil
	}

	content := b.String()
	// Drop the newline we appended to the final line when the file itself
	// did not end with one and we read to its end.
	if reachedEOF && totalSize > 0 && !fileEndsWithNewline(f, totalSize) {
		content = strings.TrimSuffix(content, "\n")
	}
	return content, w, nil
}

// fileEndsWithNewline reports whether the last byte of the file is a newline.
// A read error is reported as "yes" so the caller leaves the content as-is;
// guessing the other way would silently strip a real newline.
func fileEndsWithNewline(f *os.File, size int64) bool {
	var buf [1]byte
	if _, err := f.ReadAt(buf[:], size-1); err != nil {
		return true
	}
	return buf[0] == '\n'
}

// MaxFetchBodyBytes caps the size of an HTTP response body that Fetch will read.
