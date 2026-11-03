package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ersinkoc/dfmt/internal/instances"
)

func TestReportCleanupEmpty(t *testing.T) {
	var buf bytes.Buffer
	rc := reportCleanup(&buf, instances.Plan{Keep: "/k/dfmt", Healthy: map[string]int{"daemon": 1, "mcp": 3}}, true)
	out := buf.String()
	if rc != 0 || !strings.Contains(out, "Nothing to clean") ||
		!strings.Contains(out, "1 daemon") || !strings.Contains(out, "3 mcp") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
}

// A plan is only printed unless --yes: cleanup ends processes and deletes
// files, so the default must be safe.
func TestReportCleanupDryRunDoesNothing(t *testing.T) {
	var buf bytes.Buffer
	plan := instances.Plan{
		Keep:   "/k/dfmt",
		Remove: []instances.Removal{{Path: "/definitely/not/here/dfmt", Reason: "stray"}},
	}
	rc := reportCleanup(&buf, plan, false)
	out := buf.String()
	if rc != 0 || !strings.Contains(out, "remove") || !strings.Contains(out, "Dry run") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if strings.Contains(out, " ok ") {
		t.Fatalf("dry run reported an action as done: %q", out)
	}
}

func TestReportCleanupApplyRemovesAndCounts(t *testing.T) {
	var buf bytes.Buffer
	stray := filepath.Join(t.TempDir(), "dfmt")
	if err := os.WriteFile(stray, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := instances.Plan{
		Keep: "/k/dfmt",
		Remove: []instances.Removal{
			{Path: stray, Reason: "stray"},
			{Path: "/definitely/not/here/dfmt", Reason: "gone already"}, // absent counts as done
		},
	}
	rc := reportCleanup(&buf, plan, true)
	out := buf.String()
	if rc != 0 || strings.Contains(out, "FAILED") {
		t.Fatalf("rc=%d out=%q", rc, out)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray copy still present (err=%v)", err)
	}
}

// Without --yes the command only observes the machine (process table, PATH);
// it must exit 0 and never act. Under `go test` the binary is cli.test, which
// is not named dfmt, so no real process can be nominated.
func TestRunCleanupDryRun(t *testing.T) {
	if rc := runCleanup(nil); rc != 0 {
		t.Fatalf("dry run rc = %d, want 0", rc)
	}
}

func TestRunCleanupFlags(t *testing.T) {
	if rc := runCleanup([]string{"-h"}); rc != 0 {
		t.Errorf("-h rc = %d, want 0", rc)
	}
	if rc := runCleanup([]string{"--nope"}); rc != 2 {
		t.Errorf("unknown flag rc = %d, want 2", rc)
	}
}

func TestPlanCleanupKeepsGivenBinary(t *testing.T) {
	plan, err := planCleanup("/some/where/dfmt")
	if err != nil {
		t.Skipf("process listing unavailable: %v", err)
	}
	if plan.Keep != "/some/where/dfmt" {
		t.Fatalf("Keep = %q", plan.Keep)
	}
}
