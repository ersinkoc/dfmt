package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestCloseExtraProjectsPersistsIndex proves that closeExtraProjects
// writes each cached project's index to disk before closing its
// journal. Without this, every restart of a global daemon serving N
// projects would force a full journal replay per project on the next
// run — the user-visible symptom is a cold-recall pause that scales
// with journal size.
//
// The skip-on-NeedsRebuild branch is exercised by the second
// sub-assertion: a fresh daemon load that finds no on-disk index
// (NeedsRebuild=true) MUST NOT persist a near-empty index, because
// doing so would write cursor=HEAD and cause the next start to skip
// the rebuild that historic events depend on.
func TestCloseExtraProjectsPersistsIndex(t *testing.T) {
	defaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(defaultRoot, ".dfmt"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := New(defaultRoot, newTestConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	extraRoot := t.TempDir()
	r, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}

	// Force the persist-eligible branch: simulate a healthy load where
	// the cursor was already up-to-date so writing index.gob is safe.
	r.NeedsRebuild = false

	// Append something so the index has content worth persisting.
	ctx := context.Background()
	e := core.Event{
		ID:      "01PERSIST",
		Type:    core.EvtNote,
		Project: extraRoot,
		Tags:    []string{"persist-check"},
	}
	e.Sig = e.ComputeSig()
	if err := r.Journal.Append(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	r.Index.Add(e)

	// Close the default project's journal so closeExtraProjects can run
	// without contention with daemon Stop sequencing.
	if d.journal != nil {
		_ = d.journal.Close()
	}
	d.closeExtraProjects()

	indexPath := filepath.Join(extraRoot, ".dfmt", "index.gob")
	st, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("index file missing after closeExtraProjects: %v", err)
	}
	if st.Size() == 0 {
		t.Errorf("index file is empty after closeExtraProjects (size=%d)", st.Size())
	}

	// Now exercise the skip branch: a second extra project where
	// NeedsRebuild stays true must NOT have an index file written.
	d2, err := New(t.TempDir(), newTestConfig())
	if err != nil {
		t.Fatalf("New d2: %v", err)
	}
	skipRoot := t.TempDir()
	rSkip, err := d2.Resources(skipRoot)
	if err != nil {
		t.Fatalf("Resources(skip): %v", err)
	}
	rSkip.NeedsRebuild = true
	if d2.journal != nil {
		_ = d2.journal.Close()
	}
	d2.closeExtraProjects()
	if _, err := os.Stat(filepath.Join(skipRoot, ".dfmt", "index.gob")); err == nil {
		t.Errorf("index.gob written for NeedsRebuild=true project — would cause next start to skip rebuild and lose historic events")
	}
}

// TestResourcesLoadsProjectOwnConfig proves that an extra project loads
// its own .dfmt/config.yaml rather than inheriting the daemon's
// startup cfg. Before this fix a global daemon serving both project A
// and project B silently used A's retention/budget/path_prepend for
// every B call, regardless of what B's YAML said.
func TestResourcesLoadsProjectOwnConfig(t *testing.T) {
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

	// Project B has its own config.yaml with a non-default budget.
	extraRoot := t.TempDir()
	dfmtDir := filepath.Join(extraRoot, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dfmtDir, "config.yaml")
	cfgYAML := "retrieval:\n  default_budget: 12345\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}
	defer func() { _ = r.Journal.Close() }()

	if r.Config == nil {
		t.Fatal("extra project resources have nil Config")
	}
	if r.Config.Retrieval.DefaultBudget != 12345 {
		t.Errorf("extra project default_budget: got %d, want 12345 (project YAML must override daemon cfg)",
			r.Config.Retrieval.DefaultBudget)
	}
}

// TestIndexTailPicksUpExternalAppends proves the per-project index
// tail goroutine catches events written to the journal by a
// *different* writer (the production case being a `dfmt mcp`
// subprocess that owns its own journal handle to the same
// .dfmt/journal.jsonl file). Before this fix the daemon's in-memory
// index drifted permanently behind any out-of-process append, and
// `dfmt_search` results stayed stale until the next daemon restart
// triggered a journal-replay rebuild.
//
// The test uses a tight tail interval and a brief sleep window to
// keep the run fast without exposing the goroutine's internals.
func TestIndexTailPicksUpExternalAppends(t *testing.T) {
	prevInterval := indexTailInterval
	indexTailInterval = 50 * time.Millisecond
	t.Cleanup(func() { indexTailInterval = prevInterval })

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
	r, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}
	defer func() {
		r.stopIndexTail()
		if r.Journal != nil {
			_ = r.Journal.Close()
		}
	}()

	// Simulate a second writer (the MCP subprocess analog) by opening
	// a NEW journal handle to the same on-disk file. Add WITHOUT
	// going through r.Index — that would defeat the test by
	// pre-populating the index the daemon owns.
	externalJournal, err := core.OpenJournal(
		filepath.Join(extraRoot, ".dfmt", "journal.jsonl"),
		core.JournalOptions{Path: filepath.Join(extraRoot, ".dfmt", "journal.jsonl")},
	)
	if err != nil {
		t.Fatalf("open external journal: %v", err)
	}
	defer func() { _ = externalJournal.Close() }()

	ctx := context.Background()
	e := core.Event{
		ID:      "01TAILTEST",
		Type:    core.EvtNote,
		Project: extraRoot,
		Tags:    []string{"tail-witness-XPLT9"},
	}
	e.Sig = e.ComputeSig()
	if err := externalJournal.Append(ctx, e); err != nil {
		t.Fatalf("external append: %v", err)
	}

	// Wait up to ~1 s for the tail to converge. Bounded so a
	// regression that breaks the tail does not hang CI forever.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		hits := r.Index.SearchTrigram("tail-witness-XPLT9", 5)
		if len(hits) > 0 {
			return // success — tail observed the external append
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("index tail never observed externally-appended event; daemon's index drifted from on-disk journal")
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

// TestFSWatcherPerExtraProject verifies the v0.5.0 contract: when an
// extra project's config opts into Capture.FS.Enabled, a per-project
// FSWatcher attaches to that project and a filesystem mutation in
// that root lands in *that* project's journal — not the default's.
//
// This closes the multi-project FS-capture gap that v0.4.x left open:
// only the default project ever received fswatch events, so secondary
// projects relied on MCP/CLI events alone for their recall surface.
func TestFSWatcherPerExtraProject(t *testing.T) {
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
		d.closeExtraProjects()
	}()

	// Extra project with its own config.yaml enabling FS capture.
	// Without the on-disk file the extra project would inherit the
	// daemon's fallback cfg (FS disabled by default) and the watcher
	// would never start — that's the v0.4.x behavior we're replacing.
	extraRoot := t.TempDir()
	extraDfmt := filepath.Join(extraRoot, ".dfmt")
	if err := os.MkdirAll(extraDfmt, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgYAML := "" +
		"capture:\n" +
		"  fs:\n" +
		"    enabled: true\n" +
		"    ignore: [\".git/**\"]\n" +
		"    debounce_ms: 50\n"
	if err := os.WriteFile(filepath.Join(extraDfmt, "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := d.Resources(extraRoot)
	if err != nil {
		t.Fatalf("Resources(extra): %v", err)
	}
	if r.FSWatcher == nil {
		t.Fatal("extra project FSWatcher is nil despite cfg.Capture.FS.Enabled=true")
	}
	if r.fsStop == nil {
		t.Fatal("startFSWatch did not run: fsStop is nil")
	}

	// Touch a file in the extra project root. The watcher's debounce
	// window (50 ms) plus capture pipeline latency means we may need
	// a few hundred ms before the event lands in the journal.
	target := filepath.Join(extraRoot, "trigger.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		stream, serr := r.Journal.Stream(ctx, "")
		if serr != nil {
			cancel()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var found bool
		for e := range stream {
			if strings.Contains(strings.ToLower(string(e.Source)), "fs") ||
				strings.Contains(strings.ToLower(string(e.Type)), "fs") {
				found = true
				break
			}
			if path, ok := e.Data["path"].(string); ok && strings.HasSuffix(path, "trigger.txt") {
				found = true
				break
			}
		}
		cancel()
		if found {
			// Belt and suspenders: the default project's journal must
			// NOT contain this event. Otherwise the per-project routing
			// regressed.
			ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
			ds, derr := d.journal.Stream(ctx2, "")
			if derr == nil {
				for e := range ds {
					if path, ok := e.Data["path"].(string); ok && strings.HasSuffix(path, "trigger.txt") {
						cancel2()
						t.Fatalf("default project journal received extra-project fswatch event for %s", path)
					}
				}
			}
			cancel2()
			// Use core.Event to avoid an unused-import error if the
			// matcher above changes shape.
			_ = core.Event{}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("FSWatcher event for trigger.txt never landed in extra project's journal within 3 s")
}
