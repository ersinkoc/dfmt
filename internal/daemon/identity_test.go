package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/version"
)

func TestWriteAndReadIdentityRoundTrip(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())

	if err := writeIdentityFile(); err != nil {
		t.Fatalf("writeIdentityFile: %v", err)
	}

	id, ok := ReadIdentity()
	if !ok {
		t.Fatal("ReadIdentity: ok = false immediately after a successful write")
	}
	if id.Version != version.Current {
		t.Errorf("Version = %q, want %q", id.Version, version.Current)
	}
	if id.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", id.PID, os.Getpid())
	}
	if id.StartedAt == "" {
		t.Error("StartedAt is empty")
	}
}

// A missing identity file is the pre-upgrade state — a daemon that predates
// identity publishing. It must read as "not ok" so callers treat it as
// stale; reporting ok with a zero Version would make an old daemon look
// current and defeat the entire mechanism.
func TestReadIdentityMissingFileIsNotOK(t *testing.T) {
	t.Setenv("DFMT_GLOBAL_DIR", t.TempDir())

	if _, ok := ReadIdentity(); ok {
		t.Error("ReadIdentity: ok = true for a missing file, want false")
	}
}

func TestReadIdentityRejectsMalformedAndEmptyVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", "this is not json"},
		{"truncated", `{"version":"v1.0.0"`},
		{"empty version", `{"pid":123}`},
		{"blank version", `{"version":"","pid":123}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DFMT_GLOBAL_DIR", dir)
			if err := os.WriteFile(filepath.Join(dir, project.GlobalIdentityFileName),
				[]byte(tc.body), 0o600); err != nil {
				t.Fatalf("seed identity file: %v", err)
			}
			if _, ok := ReadIdentity(); ok {
				t.Errorf("ReadIdentity: ok = true for %s, want false", tc.name)
			}
		})
	}
}

func TestReadIdentityReportsRecordedVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)

	want := Identity{Version: "v0.0.1-old", PID: 4242, Exe: "/old/dfmt"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, project.GlobalIdentityFileName), data, 0o600); err != nil {
		t.Fatalf("seed identity file: %v", err)
	}

	got, ok := ReadIdentity()
	if !ok {
		t.Fatal("ReadIdentity: ok = false for a well-formed file")
	}
	if got.Version != want.Version || got.PID != want.PID || got.Exe != want.Exe {
		t.Errorf("ReadIdentity() = %+v, want %+v", got, want)
	}
	// The whole point: an old version must NOT compare equal to ours.
	if SameVersion(got.Version, version.Current) {
		t.Errorf("SameVersion(%q, %q) = true, want false", got.Version, version.Current)
	}
}

func TestSameVersion(t *testing.T) {
	tests := []struct {
		name           string
		daemon, client string
		want           bool
	}{
		{"identical", "v0.6.9", "v0.6.9", true},
		{"differing patch", "v0.6.8", "v0.6.9", false},
		{"daemon predates identity", "", "v0.6.9", false},
		{"surrounding whitespace ignored", " v0.6.9\n", "v0.6.9", true},
		// An unstamped client (`go build` with no -ldflags) matches anything:
		// a contributor's dev build must not restart the daemon serving every
		// other project on the host over a skew they can already see.
		{"unstamped client matches anything", "v0.6.9", "", true},
		{"unstamped client vs unstamped daemon", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameVersion(tc.daemon, tc.client); got != tc.want {
				t.Errorf("SameVersion(%q, %q) = %v, want %v", tc.daemon, tc.client, got, tc.want)
			}
		})
	}
}

// IsCurrent must catch a rebuild at an unchanged version tag — the everyday
// case when developing DFMT itself, and one a version-only comparison
// misses entirely.
func TestIsCurrentDetectsRebuiltBinaryAtSameVersion(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dfmt-fake.exe")
	if err := os.WriteFile(exe, []byte("build one"), 0o600); err != nil {
		t.Fatalf("seed exe: %v", err)
	}

	id := Identity{
		Version:        version.Current,
		Exe:            exe,
		ExeFingerprint: buildFingerprint(exe),
	}
	if !id.IsCurrent(version.Current) {
		t.Fatal("IsCurrent = false for an untouched binary at a matching version")
	}

	// Rebuild: same path, same version, different bytes. Set an explicitly
	// later mtime so the check does not depend on filesystem timestamp
	// granularity (Windows FAT/NTFS can round to 1-2s).
	if err := os.WriteFile(exe, []byte("build two - different length"), 0o600); err != nil {
		t.Fatalf("rewrite exe: %v", err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(exe, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if id.IsCurrent(version.Current) {
		t.Error("IsCurrent = true after the binary was rebuilt at the same version; " +
			"a same-tag rebuild must count as stale or the daemon keeps serving replaced code")
	}
}

// A daemon whose binary has been moved or deleted is not evidence of stale
// code. Treating an unstatable exe as stale would restart the daemon on
// every single command.
func TestIsCurrentToleratesMissingExe(t *testing.T) {
	id := Identity{
		Version:        version.Current,
		Exe:            filepath.Join(t.TempDir(), "gone.exe"),
		ExeFingerprint: "123:456",
	}
	if !id.IsCurrent(version.Current) {
		t.Error("IsCurrent = false when the recorded exe no longer exists, want true")
	}
}

// A daemon that recorded no fingerprint (older identity format) must fall
// back to the version comparison rather than being judged stale forever.
func TestIsCurrentWithoutFingerprintFallsBackToVersion(t *testing.T) {
	if !(Identity{Version: version.Current}).IsCurrent(version.Current) {
		t.Error("IsCurrent = false for matching version with no fingerprint, want true")
	}
	if (Identity{Version: "v0.1.0"}).IsCurrent(version.Current) {
		t.Error("IsCurrent = true for a differing version, want false")
	}
}

func TestBuildFingerprintChangesWithContentAndIsEmptyWhenAbsent(t *testing.T) {
	if got := buildFingerprint(""); got != "" {
		t.Errorf("buildFingerprint(\"\") = %q, want \"\"", got)
	}
	if got := buildFingerprint(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Errorf("buildFingerprint(missing) = %q, want \"\"", got)
	}

	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("aa"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := buildFingerprint(p)
	if first == "" {
		t.Fatal("buildFingerprint returned empty for an existing file")
	}
	if err := os.WriteFile(p, []byte("bbbb"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if second := buildFingerprint(p); second == first {
		t.Errorf("buildFingerprint unchanged (%q) after the file grew", second)
	}
}

// Stop must remove the identity file. A stale identity next to a dead
// listener would let the next client believe a current daemon is up.
func TestIdentityFileRemovedPathIsTheGlobalOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DFMT_GLOBAL_DIR", dir)

	if err := writeIdentityFile(); err != nil {
		t.Fatalf("writeIdentityFile: %v", err)
	}
	want := filepath.Join(dir, project.GlobalIdentityFileName)
	if got := project.GlobalIdentityPath(); got != want {
		t.Fatalf("GlobalIdentityPath() = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("identity file not at the documented path: %v", err)
	}

	// Mirror Stop's cleanup and confirm the reader then reports stale.
	if err := os.Remove(want); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := ReadIdentity(); ok {
		t.Error("ReadIdentity: ok = true after removal, want false")
	}
}
