package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/core"
)

// TestResourcesDefaultProjectReturnsDirectFields verifies that calling
// Resources("") or Resources(d.projectPath) on a daemon constructed
// with a default project returns a view that aliases the daemon's
// own journal/index/redactor — not a freshly loaded copy. This is the
// load-bearing fast path that keeps the existing single-project
// daemon's behavior unchanged.
func TestResourcesDefaultProjectReturnsDirectFields(t *testing.T) {
	tmpDir := t.TempDir()
	dfmtDir := filepath.Join(tmpDir, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	d, err := New(tmpDir, newTestConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	// Empty projectID → default fallback.
	r1, err := d.Resources("")
	if err != nil {
		t.Fatalf("Resources(\"\"): %v", err)
	}
	if r1.Journal != d.journal {
		t.Errorf("Resources(\"\").Journal: got %p, want %p (daemon's own journal)", r1.Journal, d.journal)
	}
	if r1.Index != d.index {
		t.Errorf("Resources(\"\").Index: got %p, want %p (daemon's own index)", r1.Index, d.index)
	}

	// Explicit default path → same fast path.
	r2, err := d.Resources(d.projectPath)
	if err != nil {
		t.Fatalf("Resources(d.projectPath): %v", err)
	}
	if r2.Journal != d.journal {
		t.Errorf("Resources(default path) returned a non-default journal handle")
	}

	// Cache must remain empty for the default project — direct fields,
	// no allocation.
	if len(d.extraProjects) != 0 {
		t.Errorf("extraProjects has %d entries after default-project lookups, want 0", len(d.extraProjects))
	}
}

// TestResourcesExtraProjectLoadsAndCaches verifies the lazy-load path:
// a second project root, when requested, gets its own journal/index/
// content store, lands in the cache, and returns the same instance on
// the next call.
func TestResourcesExtraProjectLoadsAndCaches(t *testing.T) {
	defaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(defaultRoot, ".dfmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := New(defaultRoot, newTestConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	extraRoot := t.TempDir()
	r1, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}
	if r1.Journal == d.journal {
		t.Error("extra project journal aliases default journal — cache must isolate")
	}
	if r1.Journal == nil || r1.Index == nil {
		t.Errorf("extra project resources missing handles: journal=%v index=%v", r1.Journal, r1.Index)
	}
	defer func() { _ = r1.Journal.Close() }()

	// Second call returns the cached instance.
	r2, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra) second call: %v", err)
	}
	if r1 != r2 {
		t.Errorf("Resources(extra) returned a fresh bundle on second call: r1=%p r2=%p", r1, r2)
	}

	// .dfmt directory should now exist for the extra project.
	if _, err := os.Stat(filepath.Join(extraRoot, ".dfmt")); err != nil {
		t.Errorf(".dfmt not created in extra project root: %v", err)
	}
}

// TestResourcesIsolatesJournalAppends proves that an event appended via
// the extra-project resources lands only in that project's journal,
// not the default project's. This is the test that closes the cross-
// project leak risk listed in the Phase 2 plan.
func TestResourcesIsolatesJournalAppends(t *testing.T) {
	defaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(defaultRoot, ".dfmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := New(defaultRoot, newTestConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if d.journal != nil {
			_ = d.journal.Close()
		}
	}()

	extraRoot := t.TempDir()
	rExtra, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}
	defer func() { _ = rExtra.Journal.Close() }()

	// Append to extra journal only.
	ctx := context.Background()
	e := core.Event{
		ID:      "01TEST",
		Type:    core.EvtNote,
		Project: extraRoot,
		Tags:    []string{"isolation-check"},
	}
	e.Sig = e.ComputeSig()
	if err := rExtra.Journal.Append(ctx, e); err != nil {
		t.Fatalf("extra journal append: %v", err)
	}

	// The default journal should be untouched. Read its file directly —
	// the in-memory journal also reflects state but reading the file is
	// the cleaner cross-instance check.
	defaultPath := filepath.Join(defaultRoot, ".dfmt", "journal.jsonl")
	defaultBody, _ := os.ReadFile(defaultPath)
	if strings.Contains(string(defaultBody), "isolation-check") {
		t.Errorf("default project journal contains tag from extra project — isolation broken")
	}

	// And the extra journal should have it.
	extraPath := filepath.Join(extraRoot, ".dfmt", "journal.jsonl")
	extraBody, _ := os.ReadFile(extraPath)
	if !strings.Contains(string(extraBody), "isolation-check") {
		t.Errorf("extra project journal missing the appended event tag")
	}
}

// TestResourcesEmptyProjectIDOnDegradedDaemon verifies the degraded-mode
// contract: a daemon constructed without a discoverable project (empty
// projectPath) returns errProjectIDRequired when asked to resolve an
// empty project_id. MCP subprocesses in degraded mode rely on this so
// memory-touching tools surface a clear error rather than panicking.
func TestResourcesEmptyProjectIDOnDegradedDaemon(t *testing.T) {
	d := &Daemon{} // no project, no journal — purely degraded.
	if _, err := d.Resources(""); err == nil {
		t.Error("Resources(\"\") on degraded daemon: got nil error, want errProjectIDRequired")
	}
}
