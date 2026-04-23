package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSWatcher_SetProject_StampsEvents(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWatcher(dir, nil, 10)
	if err != nil {
		t.Fatalf("NewFSWatcher: %v", err)
	}
	w.SetProject("proj-under-test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop(context.Background())

	// Trigger a filesystem change.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case e := <-w.Events():
		if e.Project != "proj-under-test" {
			t.Errorf("Event.Project = %q, want %q", e.Project, "proj-under-test")
		}
		if e.Sig == "" {
			t.Error("Event.Sig is empty - Project must be stamped before ComputeSig")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fswatch event")
	}
}

func TestFSWatcher_NoProject_EmptyStamp(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFSWatcher(dir, nil, 10)
	if err != nil {
		t.Fatalf("NewFSWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop(context.Background())

	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	select {
	case e := <-w.Events():
		if e.Project != "" {
			t.Errorf("Event.Project = %q, want empty", e.Project)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fswatch event")
	}
}
