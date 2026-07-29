package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/safefs"
	"github.com/ersinkoc/dfmt/internal/version"
)

// Identity is the contents of ~/.dfmt/daemon.json — the record of which
// build is currently serving every project on this host.
//
// # Why this file exists
//
// The daemon is a singleton that outlives the command that started it, and
// liveness is decided by a bare TCP/socket dial (client.fastDialOK). A dial
// succeeds against ANY build, so nothing in the system could distinguish
// "the daemon is up" from "a daemon compiled months ago is up". Rebuilding
// and reinstalling dfmt therefore had no effect: the old process kept the
// singleton lock and kept answering, and because its replies were
// well-formed, the divergence showed up as silently wrong results rather
// than as an error. That is how a May-15 binary ended up serving a v0.6.9
// checkout and returning empty stdout for every exec.
//
// Publishing the build identity next to the PID file turns that invisible
// state into a checkable one: clients compare Version against their own
// version.Current and restart the daemon on mismatch.
//
// Exe and StartedAt are diagnostics, not control inputs. `dfmt doctor`
// reports them so an operator can see WHICH binary is serving when several
// copies exist on disk — the ambiguity that produced the stale daemon in the
// first place.
type Identity struct {
	// Version is the serving daemon's version.Current at Start.
	Version string `json:"version"`
	// PID is the daemon process ID (mirrors daemon.pid; duplicated here so
	// a single read answers "who is serving, and is it current").
	PID int `json:"pid"`
	// Exe is the absolute path of the binary that is running, resolved via
	// os.Executable().
	Exe string `json:"exe,omitempty"`
	// ExeFingerprint identifies the exact bytes of Exe at the moment the
	// daemon started ("<modtime-unixnano>:<size>").
	//
	// Version alone is not enough for anyone developing DFMT itself: a
	// contributor rebuilds many times at the same tag, so every rebuild
	// would compare equal and the daemon would keep serving the code they
	// just replaced — the same stale-daemon failure, scoped to one version.
	// Re-statting Exe and comparing against this value catches "the binary
	// was overwritten while the daemon kept running", which is what a
	// rebuild actually is.
	ExeFingerprint string `json:"exe_fingerprint,omitempty"`
	// StartedAt is the RFC3339 UTC timestamp of daemon start.
	StartedAt string `json:"started_at,omitempty"`
}

// buildFingerprint returns a cheap identifier for the current contents of
// the file at path. Modification time plus size is enough: the question is
// only "was this binary replaced", not "is it authentic", and hashing a
// 13 MB executable on a liveness check that runs before every command would
// cost far more than it tells us.
//
// Returns "" when path is empty or unstatable, which callers must treat as
// "unknown, assume current" — a missing binary is not evidence of staleness,
// and guessing otherwise would restart the daemon on every command.
func buildFingerprint(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
}

// IsCurrent reports whether the daemon described by this Identity is running
// the same code as a client at clientVersion.
//
// Two independent staleness signals, because they catch different failures:
//
//   - version skew: an installed release was replaced by another release;
//   - fingerprint skew: the daemon's own binary was overwritten on disk
//     while it kept running (a rebuild, or an installer writing over it).
//
// The fingerprint check deliberately ignores the unstamped-dev-build
// exemption in SameVersion: "the file I am running from changed" is a valid
// staleness signal no matter how the client was stamped, and it is the only
// signal that exists during same-version development.
func (i Identity) IsCurrent(clientVersion string) bool {
	if !SameVersion(i.Version, clientVersion) {
		return false
	}
	if i.ExeFingerprint == "" || i.Exe == "" {
		return true // nothing recorded to compare against
	}
	current := buildFingerprint(i.Exe)
	if current == "" {
		return true // exe moved or deleted; not evidence of stale code
	}
	return current == i.ExeFingerprint
}

// writeIdentityFile publishes the current process's build identity to
// ~/.dfmt/daemon.json. Called by Daemon.Start immediately after the PID
// file, and guarded by the same safefs symlink check — an attacker who can
// pre-plant daemon.json as a symlink should not get a daemon-authored write
// to the target.
//
// A failure here is non-fatal to the daemon but not silent: the caller logs
// it. The consequence of a missing identity file is that clients treat the
// daemon as stale and restart it once, which is the safe direction.
func writeIdentityFile() error {
	exe, err := os.Executable()
	if err != nil {
		exe = "" // diagnostic only; absence must not block the write
	}
	id := Identity{
		Version:        version.Current,
		PID:            os.Getpid(),
		Exe:            exe,
		ExeFingerprint: buildFingerprint(exe),
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	data = append(data, '\n')
	return safefs.WriteFile(project.GlobalDir(), project.GlobalIdentityPath(), data, 0o600)
}

// ReadIdentity loads ~/.dfmt/daemon.json. It returns ok=false when the file
// is missing, unreadable, or malformed — all of which callers must treat
// identically to "a daemon that predates identity publishing", i.e. stale.
//
// Exported because internal/cli performs the version comparison; keeping the
// struct and both accessors in one place stops the daemon-side writer and
// the client-side reader from drifting on field names.
func ReadIdentity() (Identity, bool) {
	data, err := os.ReadFile(project.GlobalIdentityPath())
	if err != nil {
		return Identity{}, false
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Identity{}, false
	}
	if id.Version == "" {
		return Identity{}, false
	}
	return id, true
}

// SameVersion reports whether a daemon reporting `daemonVersion` is
// interchangeable with a client built as `clientVersion`.
//
// Comparison is exact after trimming surrounding space, with one
// deliberate exception: an empty client version (a `go build` with no
// -ldflags, common in a contributor's working tree) matches anything. Making
// an unstamped dev build restart every daemon it touched would turn a
// routine `go run ./cmd/dfmt` into a service interruption for every other
// project on the host, and the developer building from source is precisely
// the person who does not need to be protected from a version skew they can
// see. Release binaries always carry a stamped version.
func SameVersion(daemonVersion, clientVersion string) bool {
	d := strings.TrimSpace(daemonVersion)
	c := strings.TrimSpace(clientVersion)
	if c == "" {
		return true
	}
	return d == c
}
