package transport

import (
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

func TestHTTPServerStopNotRunning(t *testing.T) {
	// Create server but don't start it
	idx := core.NewIndex()
	handlers := NewHandlers(idx, nil)
	hs := NewHTTPServer(":0", handlers)

	// Should return nil immediately since not running
	if err := hs.Stop(); err != nil {
		t.Errorf("Stop on not-running server failed: %v", err)
	}
}
