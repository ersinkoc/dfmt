package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/safefs"
)

var (
	ErrJournalFull     = errors.New("journal has reached max bytes")
	ErrJournalNotFound = errors.New("journal not found")
	// ErrEventTooLarge is returned when a single event exceeds maxEventBytes.
	ErrEventTooLarge = errors.New("event exceeds max serialized size")
)

// maxEventBytes caps the serialized size of a single journal event. The limit
// must stay <= the Scanner.Buffer upper bound used in Stream/scanLastID so we
// never write an event that cannot be read back.
const maxEventBytes = 1 << 20 // 1 MiB

// scannerBufferMax is the upper bound bufio.Scanner will grow its buffer to
// when reading the journal. Any longer line is silently skipped by Scanner,
// which is why we refuse to write events larger than this.
const scannerBufferMax = 1 << 20 // 1 MiB

// journalWarnf emits a warning the operator should see (corrupt-line skips,
// partial-write recovery). Tests override the implementation via
// swapJournalWarnf so the package var is never assigned concurrently — the
// previous mutable-var test seam was a -race landmine when t.Parallel tests
// ran journal streams in parallel with a pending var swap. Production never
// changes the implementation.
//
// See V-9 in security-report/: previously `streamFile` and `scanLastID`
// silently dropped any line that failed `json.Unmarshal`, leaving no
// operator-visible trail for journal corruption.
type journalWarnFunc func(format string, args ...any)

var journalWarnfPtr atomic.Pointer[journalWarnFunc]

func init() {
	defaultWarn := journalWarnFunc(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
	})
	journalWarnfPtr.Store(&defaultWarn)
}

func journalWarnf(format string, args ...any) {
	if f := journalWarnfPtr.Load(); f != nil {
		(*f)(format, args...)
	}
}

// swapJournalWarnf replaces the warning sink for testing. The returned
// restore func MUST be deferred (or wired into t.Cleanup) so the previous
// implementation is reinstated even if the test panics.
func swapJournalWarnf(f journalWarnFunc) (restore func()) {
	prev := journalWarnfPtr.Load()
	journalWarnfPtr.Store(&f)
	return func() { journalWarnfPtr.Store(prev) }
}

// snippetForWarn produces a bounded, copy-safe snippet of a journal line for
// warnings. bufio.Scanner reuses its underlying buffer, so the bytes returned
// by Bytes() are only valid until the next Scan(); we copy + truncate here.
func snippetForWarn(line []byte) string {
	const max = 80
	if len(line) <= max {
		return string(line)
	}
	out := make([]byte, max+3)
	copy(out, line[:max])
	out[max], out[max+1], out[max+2] = '.', '.', '.'
	return string(out)
}

// JournalOptions configures the journal.
type JournalOptions struct {
	Path     string
	MaxBytes int64
	Durable  bool
	BatchMS  int
	Compress bool
}

// Journal appends events to an append-only JSONL file.
type Journal interface {
	Append(ctx context.Context, e Event) error
	Stream(ctx context.Context, from string) (<-chan Event, error)
	Checkpoint(ctx context.Context) (string, error)
	Rotate(ctx context.Context) error
	// Size returns the active journal's on-disk byte count. Excludes
	// rotated archive files. Used by the Prometheus /metrics surface
	// (dfmt_journal_bytes); a -1 reading at the gauge layer means
	// the implementation returned a non-nil error here. ADR-0017.
	Size() (int64, error)
	Close() error
}

// journalImpl implements Journal.
type journalImpl struct {
	path         string
	file         *os.File
	mu           sync.Mutex
	durable      bool
	batchMS      int
	syncInterval time.Duration
	lastSync     time.Time
	maxBytes     int64
	compress     bool
	hiCursor     string
	syncStop     chan struct{}
	syncDone     chan struct{}
	closed       bool
}

// OpenJournal opens or creates a journal at the given path.
func OpenJournal(path string, opt JournalOptions) (Journal, error) {
	// Reject Windows reserved device names (NUL, CON, …) before MkdirAll —
	// NTFS Services-for-UNIX silently maps a `NUL:` component to a real
	// `NUL` directory, which would leave a stray directory on disk
	// every time a test (or a typo'd config) hands us such a path.
	if err := safefs.CheckNoReservedNames(path); err != nil {
		return nil, fmt.Errorf("create journal dir: %w", err)
	}
	// Ensure directory exists. Use 0700 so a journal containing potentially
	// sensitive project activity isn't readable by other local users.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create journal dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	j := &journalImpl{
		path:     path,
		file:     f,
		durable:  opt.Durable,
		batchMS:  opt.BatchMS,
		maxBytes: opt.MaxBytes,
		compress: opt.Compress,
	}

	// Configure periodic sync for batched (non-durable) mode. The loop starts
	// after existing-file recovery succeeds so a failed OpenJournal does not
	// leave a goroutine behind.
	var batchInterval time.Duration
	if !opt.Durable {
		batchInterval = 30 * time.Second
		if opt.BatchMS > 0 {
			batchInterval = time.Duration(opt.BatchMS) * time.Millisecond
		}
		j.syncInterval = batchInterval
		j.lastSync = time.Now()
	}

	// Find the last ULID in the file
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		if err := j.scanLastID(); err != nil {
			f.Close()
			return nil, fmt.Errorf("scan last id: %w", err)
		}
	}

	if batchInterval > 0 {
		j.syncStop = make(chan struct{})
		j.syncDone = make(chan struct{})
		go j.periodicSync(batchInterval, j.syncStop, j.syncDone)
	}

	return j, nil
}

// Append adds an event to the journal.
func (j *journalImpl) Append(ctx context.Context, e Event) error {
	// Honor caller cancellation before doing any work. Append's hot path is
	// CPU + a small disk write, but on a contended Append (mu held by a
	// long Rotate or by another writer queued ahead) ctx.Done() is the
	// only escape — pre-fix the parameter was ignored entirely.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Sign the event before persisting. ComputeSig uses the canonical JSON
	// (id, ts, project, type, priority, source, actor, data, refs, tags) so
	// the Sig is a tractable 16-char prefix of SHA-256 over the stable form.
	// Events written before this fix have Sig == ""; Validate() handles them
	// by treating empty-Sig as "unverified but acceptable" (skip on content
	// mismatch but not on missing Sig alone).
	e.Sig = e.ComputeSig()
	// Marshal event outside the lock (CPU-bound, no shared state).
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if len(data)+1 > maxEventBytes {
		// +1 for the trailing newline. Refuse events that cannot be read back
		// via bufio.Scanner (which silently skips lines > its buffer max).
		return ErrEventTooLarge
	}
	data = append(data, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()

	// Re-check ctx after the (potentially blocking) lock acquire. A caller
	// that canceled while waiting on j.mu shouldn't get its append snuck
	// through after the cancel.
	if err := ctx.Err(); err != nil {
		return err
	}

	if j.closed {
		return errors.New("journal closed")
	}

	// Size limit check MUST be under the lock to avoid TOCTOU: two concurrent
	// Appends could both observe Size() < maxBytes and then push the journal
	// past the limit.
	if j.maxBytes > 0 {
		if fi, statErr := j.file.Stat(); statErr == nil && fi.Size() >= j.maxBytes {
			return ErrJournalFull
		}
	}

	if _, err := j.file.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if j.durable {
		if err := j.file.Sync(); err != nil {
			return fmt.Errorf("sync: %w", err)
		}
	}

	j.hiCursor = e.ID
	return nil
}

func (j *journalImpl) periodicSync(interval time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(done)

	for {
		select {
		case <-ticker.C:
			j.mu.Lock()
			if !j.closed {
				_ = j.file.Sync()
				j.lastSync = time.Now()
			}
			j.mu.Unlock()
		case <-stop:
			return
		}
	}
}

// Stream reads events from the journal starting at 'from' cursor.
// An empty 'from' starts from the beginning. Rotated segments
// (journal.jsonl.<ULID>.jsonl) are streamed before the active file in
// lexicographic ULID order so callers like RebuildIndexFromJournal and
// Recall see the full history — without this, anything written before
// the most recent Rotate() was invisible to readers.
func (j *journalImpl) Stream(ctx context.Context, from string) (<-chan Event, error) {
	// Verify the active file path is at least open-able (or absent).
	if _, err := os.Stat(j.path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat journal: %w", err)
	}
	segments, err := journalSegments(j.path)
	if err != nil {
		return nil, fmt.Errorf("enumerate rotated segments: %w", err)
	}

	ch := make(chan Event, 100)
	go func() {
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("journal stream goroutine panic recovered: %v", r)
			}
		}()
		foundFrom := from == ""
		for _, seg := range segments {
			done, err := streamFile(ctx, seg, ch, &foundFrom, from)
			if err != nil {
				// Skip an unreadable segment but continue with the rest —
				// a single corrupt rotated file shouldn't black out all
				// subsequent history.
				continue
			}
			if done {
				return
			}
		}
	}()

	return ch, nil
}

// journalSegments returns the chronological list of journal files for active
// path: rotated segments first (sorted by ULID suffix), then the active file.
// Active file is included even if it doesn't exist yet (streamFile no-ops).
func journalSegments(activePath string) ([]string, error) {
	dir := filepath.Dir(activePath)
	base := filepath.Base(activePath)
	pattern := filepath.Join(dir, base+".*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	// Glob returns lexicographic order, which is the same as chronological
	// for ULIDs, but Go's docs don't guarantee that ordering for all
	// platforms — sort explicitly to be safe.
	sort.Strings(matches)
	matches = append(matches, activePath)
	return matches, nil
}

// streamFile scans one journal file and forwards events into ch. Returns
// done=true when ctx cancellation tells us to stop entirely. foundFromPtr
// is shared across files so the `from` cursor crosses segment boundaries.
func streamFile(ctx context.Context, path string, ch chan<- Event, foundFromPtr *bool, from string) (done bool, err error) {
	f, oerr := os.Open(path)
	if oerr != nil {
		if os.IsNotExist(oerr) {
			return false, nil
		}
		return false, oerr
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Buffer upper bound must match maxEventBytes; Scanner silently skips
	// lines beyond this, so it doubles as a data-integrity guardrail.
	scanner.Buffer(make([]byte, 64*1024), scannerBufferMax)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Surface the corruption — silent skips left no trail for
			// operators when a journal segment was tampered with or
			// truncated mid-line. See V-9 in security-report/.
			journalWarnf("warning: journal %s: skip malformed line: %v (snippet=%q)\n",
				path, err, snippetForWarn(line))
			continue
		}
		// Defensive: verify event integrity even on the read path.
		// Events are always server-generated (ULID, time.Now, source),
		// but Validate guards against a corrupted or tampered journal.
		if !e.Validate() {
			journalWarnf("warning: journal %s: skip event with invalid signature id=%s type=%s\n",
				path, e.ID, e.Type)
			continue
		}

		if !*foundFromPtr {
			if e.ID == from {
				*foundFromPtr = true
			}
			continue
		}

		select {
		case ch <- e:
		case <-ctx.Done():
			return true, nil
		}
	}
	return false, nil
}

// StreamN is like Stream but stops after emitting at most n events. Pass n <= 0
// for unlimited. Useful in HTTP handlers that would otherwise buffer the whole
// journal into memory.
func (j *journalImpl) StreamN(ctx context.Context, from string, n int) (<-chan Event, error) {
	if n <= 0 {
		return j.Stream(ctx, from)
	}
	src, err := j.Stream(ctx, from)
	if err != nil {
		return nil, err
	}
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("journal drain goroutine panic recovered: %v", r)
			}
		}()
		sent := 0
		for e := range src {
			if sent >= n {
				return
			}
			select {
			case out <- e:
				sent++
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Checkpoint returns the ULID of the last appended event.
func (j *journalImpl) Checkpoint(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.hiCursor, nil
}

// Size returns the active journal file's on-disk byte count. Excludes
// any rotated archive files (those are tracked separately if/when a
// dfmt_journal_archive_bytes gauge is added). Stat is taken under the
// same mutex that guards Append so a concurrent write does not race
// with the size read.
//
// The error path covers transient os.Stat failures (file removed by an
// operator, filesystem hiccup); callers reporting Size into a Prometheus
// gauge should encode err != nil as -1 to keep the "missing" signal
// distinguishable from "empty file." See ADR-0017.
func (j *journalImpl) Size() (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return 0, errors.New("journal closed")
	}
	if j.file == nil {
		return 0, errors.New("journal file not open")
	}
	fi, err := j.file.Stat()
	if err != nil {
		return 0, fmt.Errorf("journal stat: %w", err)
	}
	return fi.Size(), nil
}

// Rotate rotates the journal file.
// Before renaming, a tombstone event is written to mark the rotation start.
// This ensures crash recovery can detect incomplete rotations.
func (j *journalImpl) Rotate(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.hiCursor == "" {
		return nil
	}

	// Write rotation tombstone to current file before renaming.
	// This marks the start of rotation; if crash occurs after rename
	// but before new file is initialized, the tombstone in the rotated
	// file indicates the rotation was intentional, not a crash mid-write.
	tombstoneID := NewULID(time.Now().Add(-time.Millisecond))
	tombstone := Event{
		ID:   string(tombstoneID),
		TS:   time.Now(),
		Type: "journal.rotate",
		Data: map[string]any{
			"rotationID": j.hiCursor,
			"ts":         time.Now().Format(time.RFC3339Nano),
		},
	}
	tombstoneBytes, err := json.Marshal(tombstone)
	if err != nil {
		return fmt.Errorf("marshal rotation tombstone: %w", err)
	}
	tombstoneBytes = append(tombstoneBytes, '\n')

	// Write tombstone to current file
	if _, err := j.file.Write(tombstoneBytes); err != nil {
		return fmt.Errorf("write rotation tombstone: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync tombstone: %w", err)
	}

	// Close current file
	if err := j.file.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	// Rename to journal.<ULID>.jsonl
	newPath := fmt.Sprintf("%s.%s.jsonl", j.path, j.hiCursor)
	if err := os.Rename(j.path, newPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// Open new file
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	j.file = f
	j.hiCursor = ""

	return nil
}

// Close closes the journal.
func (j *journalImpl) Close() error {
	var syncStop chan struct{}
	var syncDone chan struct{}

	j.mu.Lock()

	// Stop the background sync loop. Capture the channels while locked, then
	// wait after unlocking so Close cannot deadlock with a sync already waiting
	// for j.mu.
	if j.syncStop != nil {
		syncStop = j.syncStop
		syncDone = j.syncDone
		j.syncStop = nil
		j.syncDone = nil
	}

	// Mark as closed to prevent further operations
	j.closed = true
	j.mu.Unlock()

	if syncStop != nil {
		close(syncStop)
		<-syncDone
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	// Sync before close
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	if err := j.file.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

// scanLastID scans the file to find the last event ID.
// Also performs crash-recovery: if the file does not end with '\n' the last
// append was interrupted mid-write. We log a warning so subsequent appends
// don't produce a visually garbled line (Append always writes \n before
// payload when the previous append was partial; currently it just appends
// which is slightly malformed but readable line-by-line except the stitched
// line. Future: open the file without O_APPEND to truncate, on Windows
// O_APPEND disallows truncate).
func (j *journalImpl) scanLastID() error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek start: %w", err)
	}
	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64*1024), scannerBufferMax)
	var last Event
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Same trail-leaving as streamFile — see V-9 in security-report/.
			journalWarnf("warning: journal %s: skip malformed line during scan: %v (snippet=%q)\n",
				j.path, err, snippetForWarn(line))
			continue
		}
		last = e
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	j.hiCursor = last.ID

	// Partial-write detection: last byte must be '\n' if file is non-empty.
	fi, err := j.file.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if fi.Size() > 0 {
		var buf [1]byte
		if _, err := j.file.ReadAt(buf[:], fi.Size()-1); err == nil && buf[0] != '\n' {
			logging.Warnf("journal %s does not end with newline (partial write?); "+
				"next append will insert a leading newline to recover", j.path)
			// Ensure the next Append starts a new line by writing a newline
			// separator before anything else. This keeps JSONL parseable.
			if _, werr := j.file.Write([]byte{'\n'}); werr != nil {
				logging.Warnf("could not insert recovery newline: %v", werr)
			}
		}
	}

	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek end: %w", err)
	}
	return nil
}
