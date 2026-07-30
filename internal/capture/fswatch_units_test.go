package capture

import (
	"testing"
)

// FSWatcher bookkeeping helpers that carried no coverage. Each is small,
// but they are the pieces Stop() and the drop-counter diagnostics depend on
// — a mutex forgotten in addWatchedPath would surface as a rare, confusing
// data race under -race rather than as a failing test.

func TestFSWatcherDroppedEventsCounter(t *testing.T) {
	w, err := NewFSWatcher(t.TempDir(), nil, 10)
	if err != nil {
		t.Fatalf("NewFSWatcher: %v", err)
	}
	if got := w.DroppedEvents(); got != 0 {
		t.Errorf("DroppedEvents() = %d on a fresh watcher, want 0", got)
	}

	w.droppedEvents.Add(3)
	if got := w.DroppedEvents(); got != 3 {
		t.Errorf("DroppedEvents() = %d after 3 drops, want 3", got)
	}
}

func TestFSWatcherAddWatchedPath(t *testing.T) {
	w, err := NewFSWatcher(t.TempDir(), nil, 10)
	if err != nil {
		t.Fatalf("NewFSWatcher: %v", err)
	}

	w.addWatchedPath("/a")
	w.addWatchedPath("/b")

	got := w.snapshotWatchedPaths()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("snapshotWatchedPaths() = %v, want [/a /b]", got)
	}

	// The snapshot must be a copy: Stop() iterates it while other goroutines
	// may still be appending, so handing back the live slice would race.
	got[0] = "mutated"
	if again := w.snapshotWatchedPaths(); again[0] != "/a" {
		t.Error("snapshotWatchedPaths returned the live slice; mutating it corrupted watcher state")
	}
}

// The panic handler exists so one misbehaving watch goroutine cannot take
// the daemon down with it. It must swallow the panic, not re-raise.
func TestFSWatcherHandleGoroutinePanicDoesNotRepanic(t *testing.T) {
	w, err := NewFSWatcher(t.TempDir(), nil, 10)
	if err != nil {
		t.Fatalf("NewFSWatcher: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleGoroutinePanic re-panicked with %v; a watch goroutine "+
				"crash would take the whole daemon down", r)
		}
	}()
	w.handleGoroutinePanic("simulated watch goroutine panic")
}
