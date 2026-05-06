package client

import (
	"testing"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// TestClientBackendSatisfiesInterface keeps the static
// `var _ transport.Backend = (*ClientBackend)(nil)` assertion in
// backend.go honest — if a refactor renames the interface or drops a
// method, this file fails to compile too. Method behavior is
// integration-test territory; this file is structure-only.
func TestClientBackendSatisfiesInterface(t *testing.T) {
	// A typed assignment is enough — staticcheck sees "concrete value
	// always non-nil" and rejects the nil-comparison form.
	var _ transport.Backend = NewBackend(nil)
	_ = t
}
