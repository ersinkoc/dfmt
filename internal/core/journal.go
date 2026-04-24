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
	"sync"
	"time"
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
	syncTicker   *time.Ticker
	syncCh       <-chan time.Time
	closed       bool
}

// OpenJournal opens or creates a journal at the given path.
func OpenJournal(path string, opt JournalOptions) (Journal, error) {
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

	// Start periodic sync ticker for batched (non-durable) mode. Interval is
	// driven by JournalOptions.BatchMS when set, falling back to 30s which is
	// the historical default when callers leave the field zero.
	if !opt.Durable {
		interval := 30 * time.Second
		if opt.BatchMS > 0 {
			interval = time.Duration(opt.BatchMS) * time.Millisecond
		}
		j.syncInterval = interval
		j.lastSync = time.Now()
		j.syncTicker = time.NewTicker(interval)
		j.syncCh = j.syncTicker.C
	}

	// Find the last ULID in the file
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		if err := j.scanLastID(); err != nil {
			f.Close()
			return nil, fmt.Errorf("scan last id: %w", err)
		}
	}

	return j, nil
}

// Append adds an event to the journal.
func (j *journalImpl) Append(ctx context.Context, e Event) error {
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

	// Periodic sync in batched mode. A non-blocking select on syncCh alone
	// misses ticks under steady high-rate writes (Go's time.Ticker only
	// buffers one value; any tick fired while no one was reading is
	// silently dropped). Track lastSync so we also catch up if we drifted
	// past the interval.
	if j.syncCh != nil {
		drained := false
		select {
		case <-j.syncCh:
			drained = true
		default:
		}
		if drained || (j.syncInterval > 0 && time.Since(j.lastSync) >= j.syncInterval) {
			_ = j.file.Sync()
			j.lastSync = time.Now()
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

// Stream reads events from the journal starting at 'from' cursor.
// An empty 'from' starts from the beginning.
func (j *journalImpl) Stream(ctx context.Context, from string) (<-chan Event, error) {
	// Reopen for reading
	readFile, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			ch := make(chan Event)
			close(ch)
			return ch, nil
		}
		return nil, fmt.Errorf("open for read: %w", err)
	}

	ch := make(chan Event, 100)
	go func() {
		defer close(ch)
		defer readFile.Close()

		scanner := bufio.NewScanner(readFile)
		// Buffer upper bound must match maxEventBytes; Scanner silently skips
		// lines beyond this, so it doubles as a data-integrity guardrail.
		scanner.Buffer(make([]byte, 64*1024), scannerBufferMax)

		foundFrom := from == ""
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var e Event
			if err := json.Unmarshal(line, &e); err != nil {
				// Skip malformed lines
				continue
			}

			if !foundFrom {
				if e.ID == from {
					foundFrom = true
				}
				continue
			}

			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
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
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.hiCursor, nil
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
	j.mu.Lock()
	defer j.mu.Unlock()

	// Stop the sync ticker
	if j.syncTicker != nil {
		j.syncTicker.Stop()
		j.syncTicker = nil
		j.syncCh = nil
	}

	// Mark as closed to prevent further operations
	j.closed = true

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
			fmt.Fprintf(os.Stderr, "warning: journal %s does not end with newline (partial write?); "+
				"next append will insert a leading newline to recover\n", j.path)
			// Ensure the next Append starts a new line by writing a newline
			// separator before anything else. This keeps JSONL parseable.
			if _, werr := j.file.Write([]byte{'\n'}); werr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not insert recovery newline: %v\n", werr)
			}
		}
	}

	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek end: %w", err)
	}
	return nil
}
