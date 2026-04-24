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
)

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
	path       string
	file       *os.File
	mu         sync.Mutex
	durable    bool
	batchMS    int
	pending    []Event
	maxBytes   int64
	compress   bool
	hiCursor   string
	syncTicker *time.Ticker
	syncCh     <-chan time.Time
	closed     bool
}

// OpenJournal opens or creates a journal at the given path.
func OpenJournal(path string, opt JournalOptions) (Journal, error) {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create journal dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
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

	// Start periodic sync ticker for batched (non-durable) mode
	if !opt.Durable {
		j.syncTicker = time.NewTicker(30 * time.Second)
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
	// Check size limit
	if j.maxBytes > 0 {
		fi, err := j.file.Stat()
		if err == nil && fi.Size() >= j.maxBytes {
			return ErrJournalFull
		}
	}

	// Marshal event
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	data = append(data, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()

	// Check for periodic sync signal in batched mode
	if j.syncCh != nil {
		select {
		case <-j.syncCh:
			// Flush pending events before processing new event
			j.flushPendingLocked()
		default:
		}
	}

	// In durable mode, write and sync each append
	if j.durable {
		if _, err := j.file.Write(data); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		if err := j.file.Sync(); err != nil {
			return fmt.Errorf("sync: %w", err)
		}
	} else {
		// Buffer and write without sync
		j.pending = append(j.pending, e)
		if _, err := j.file.Write(data); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}

	j.hiCursor = e.ID
	return nil
}

// flushPendingLocked flushes all pending events to disk and clears the pending buffer.
// Caller must hold j.mu.
func (j *journalImpl) flushPendingLocked() {
	if len(j.pending) == 0 {
		return
	}
	for _, e := range j.pending {
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if _, err := j.file.Write(append(data, '\n')); err != nil {
			continue
		}
	}
	j.pending = nil
	// Sync to ensure all pending events are flushed to disk
	j.file.Sync()
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
		// Increase buffer for large events
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

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
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
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

	// Flush any remaining pending events
	j.flushPendingLocked()

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
func (j *journalImpl) scanLastID() error {
	// Seek to start
	j.file.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(j.file)
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
	j.hiCursor = last.ID
	j.file.Seek(0, io.SeekEnd)
	return nil
}
