package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// TestLoadedProjectsViaAPI_NonPositivePort: any non-positive port short-
// circuits with nil. The function's caller is dfmt list, which uses the
// return value to decide between "global daemon owns N projects" rows
// and the single legacy fallback row — feeding it a sentinel zero must
// not raise an error or trigger a real HTTP dial.
func TestLoadedProjectsViaAPI_NonPositivePort(t *testing.T) {
	for _, port := range []int{0, -1} {
		if got := loadedProjectsViaAPI(port); got != nil {
			t.Errorf("loadedProjectsViaAPI(%d) = %v, want nil", port, got)
		}
	}
}

// TestLoadedProjectsViaAPI_DialFailureReturnsNil: nothing listening on
// the chosen port is the post-daemon-stop steady state. The function
// must silently return nil so dfmt list can fall back to the legacy
// row instead of erroring.
func TestLoadedProjectsViaAPI_DialFailureReturnsNil(t *testing.T) {
	// 1 is a reserved port that nothing should ever bind. Even if a
	// rogue test fixture were listening there, the response wouldn't
	// be valid JSON and the function still returns nil per the
	// malformed-body branch.
	if got := loadedProjectsViaAPI(1); got != nil {
		t.Errorf("loadedProjectsViaAPI(1) on a closed port = %v, want nil", got)
	}
}

// TestLoadedProjectsViaAPI_MalformedBodyReturnsNil: a daemon returning
// non-JSON (e.g. an HTML error page from a misconfigured reverse proxy
// in front of /api/all-daemons) must not propagate the parse failure
// as a panic or fallthrough. Returning nil mirrors the dial-failure
// behavior so the caller's branch logic stays uniform.
func TestLoadedProjectsViaAPI_MalformedBodyReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	if got := loadedProjectsViaAPI(port); got != nil {
		t.Errorf("loadedProjectsViaAPI on HTML body = %v, want nil", got)
	}
}

// TestLoadedProjectsViaAPI_HappyPath: a well-formed response yields one
// entry per row whose project_path is a non-empty string. The order
// follows the source list; the function does not sort or dedupe.
func TestLoadedProjectsViaAPI_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"project_path": "/a/b"},
			{"project_path": "/c/d"},
			{"project_path": "/e/f"}
		]`))
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	got := loadedProjectsViaAPI(port)
	want := []string{"/a/b", "/c/d", "/e/f"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestLoadedProjectsViaAPI_SkipsMissingAndEmpty: rows without
// project_path, with the wrong type, or with an empty string must be
// silently dropped — the daemon's API contract guarantees the field is
// optional and the caller treats absence as "this row doesn't name a
// project I care about".
func TestLoadedProjectsViaAPI_SkipsMissingAndEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"project_path": "/keep"},
			{},
			{"project_path": ""},
			{"project_path": 42},
			{"project_path": "/keep-2"}
		]`))
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	got := loadedProjectsViaAPI(port)
	want := []string{"/keep", "/keep-2"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSignalStopProcess_PIDZeroIsNoop: PID 0 is reserved on every
// supported OS, and signalStopProcess is best-effort by design. Calling
// it with 0 must return without panicking — pre-extraction this was
// exercised only indirectly via the migration path.
func TestSignalStopProcess_PIDZeroIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("signalStopProcess(0) panicked: %v", r)
		}
	}()
	signalStopProcess(0, false)
	signalStopProcess(0, true)
}

// TestSignalStopProcess_NonexistentPIDIsNoop: PIDs above the usual
// kernel range refer to no process. The Unix os.FindProcess branch
// returns a non-nil Process whose Signal call errors silently; the
// Windows taskkill branch exits non-zero and is swallowed by exec.Run.
// Either way the call must not crash the caller.
func TestSignalStopProcess_NonexistentPIDIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("signalStopProcess(huge pid) panicked: %v", r)
		}
	}()
	signalStopProcess(99999999, false)
}

// portFromTestServer extracts the numeric port from a httptest.Server's
// URL. Helper so the loadedProjectsViaAPI tests don't repeat the
// url-parse + Atoi dance.
func portFromTestServer(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port %q: %v", u.Port(), err)
	}
	if port <= 0 {
		t.Fatalf("test server bound non-positive port: %s", srv.URL)
	}
	return port
}
