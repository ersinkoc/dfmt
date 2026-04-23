//go:build unix

package daemon

import (
	"os"
	"testing"
)

func TestLockFlock_Blocking(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	// Create a temp file for locking
	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file.Close()

	// Test blocking lock
	err = lockFlock(file, false)
	if err != nil {
		t.Errorf("lockFlock blocking failed: %v", err)
	}

	// Unlock
	err = unlockFlock(file)
	if err != nil {
		t.Errorf("unlockFlock failed: %v", err)
	}
}

func TestLockFlock_NonBlocking(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	// Create a temp file for locking
	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file1, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file1.Close()

	file2, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file2.Close()

	// Lock first file
	err = lockFlock(file1, false)
	if err != nil {
		t.Fatalf("first lockFlock failed: %v", err)
	}

	// Try non-blocking lock on second file - should fail
	err = lockFlock(file2, true)
	if err == nil {
		t.Error("expected error for second non-blocking lock, got nil")
		unlockFlock(file2)
	}

	// Unlock first file
	err = unlockFlock(file1)
	if err != nil {
		t.Errorf("unlockFlock failed: %v", err)
	}
}

func TestLockFlock_NonBlockNowAvailable(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file1, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file1.Close()

	file2, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file2.Close()

	// Lock first file
	err = lockFlock(file1, false)
	if err != nil {
		t.Fatalf("first lockFlock failed: %v", err)
	}

	err = unlockFlock(file1)
	if err != nil {
		t.Fatalf("unlockFlock(file1) failed: %v", err)
	}

	// Try non-blocking - should succeed after unlock
	err = lockFlock(file2, true)
	if err != nil {
		t.Errorf("expected non-blocking lock to succeed after unlock: %v", err)
	}

	err = unlockFlock(file2)
	if err != nil {
		t.Errorf("unlockFlock failed: %v", err)
	}

	err = unlockFlock(file1)
	if err != nil {
		t.Errorf("unlockFlock failed: %v", err)
	}
}

func TestUnlockFlock_WithoutLock(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file.Close()

	// Unlock without locking - should still work (unlock is idempotent-ish)
	err = unlockFlock(file)
	if err != nil {
		t.Errorf("unlockFlock without prior lock failed: %v", err)
	}
}

func TestLockFlock_DoubleLock(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file.Close()

	// First lock
	err = lockFlock(file, false)
	if err != nil {
		t.Fatalf("first lockFlock failed: %v", err)
	}

	// Second lock (blocking) - should succeed (flock is reentrant on same fd)
	err = lockFlock(file, false)
	if err != nil {
		t.Errorf("second lockFlock should succeed (reentrant): %v", err)
	}

	err = unlockFlock(file)
	if err != nil {
		t.Errorf("unlockFlock failed: %v", err)
	}
}

func TestLockFlock_FileDescriptor(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("skipping Unix-specific test on Windows")
	}

	f, err := os.CreateTemp("", "flock_test")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	file, err := os.OpenFile(f.Name(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open temp file: %v", err)
	}
	defer file.Close()

	// Verify we can get a valid file descriptor
	fd := file.Fd()
	if fd == 0 {
		t.Error("file descriptor should not be 0 (stdin)")
	}

	err = lockFlock(file, false)
	if err != nil {
		t.Errorf("lockFlock with valid fd failed: %v", err)
	}

	err = unlockFlock(file)
	if err != nil {
		t.Errorf("unlockFlock with valid fd failed: %v", err)
	}
}
