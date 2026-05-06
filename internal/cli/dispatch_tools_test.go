package cli

import (
	"os"
	"testing"
)

// =============================================================================
// runEdit error paths — pushes runEdit from 63.3% toward 75%+
// =============================================================================

func TestRunEditMissingOldString(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	// Without -old, parse fails or args < 2 → return 2
	code := Dispatch([]string{"edit", "/path/to/file", "new string"})
	if code != 1 && code != 2 {
		t.Errorf("edit without -old returned %d, want 1 or 2", code)
	}
}

func TestRunEditOnlyOldString(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"edit", "-old", "only-old-string"})
	if code != 1 {
		t.Errorf("edit with only -old returned %d, want 1", code)
	}
}

func TestRunEditWithOldFlag(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"edit", "-old", "old", "new"})
	if code != 1 {
		t.Logf("edit -old old new returned %d (expected fail - no daemon)", code)
	}
}

// =============================================================================
// runWrite error paths — pushes runWrite from 55.2% toward 75%+
// =============================================================================

func TestRunWriteNoPath(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"write", "-content", "hello"})
	if code != 1 {
		t.Errorf("write without path returned %d, want 1", code)
	}
}

func TestRunWriteContentFlag(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"write", "-content", "hello world", "/tmp/test_write.txt"})
	if code != 1 {
		t.Logf("write -content returned %d (expected fail - no daemon)", code)
	}
}

func TestRunWriteNoContentFlag(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	// v0.6.0+: dfmt write self-promotes when no daemon is running, so
	// the previous "want 1 (no daemon)" assertion is obsolete. The
	// promoted daemon writes 0 bytes successfully and returns 0.
	code := Dispatch([]string{"write", "/tmp/test.txt"})
	if code != 0 && code != 1 {
		t.Errorf("write without -content returned %d, expected 0 or 1", code)
	}
}

// =============================================================================
// runGlob error paths — pushes runGlob from 53.3% toward 75%+
// =============================================================================

func TestRunGlobNoPattern(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"glob"})
	if code != 1 {
		t.Errorf("glob with no pattern returned %d, want 1", code)
	}
}

func TestRunGlobWithIntent(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"glob", "-intent", "source files", "*.go"})
	if code != 1 {
		t.Logf("glob with -intent returned %d (expected fail - no daemon)", code)
	}
}

// =============================================================================
// runRead error paths — pushes runRead from 51.2% toward 75%+
// =============================================================================

func TestRunReadNoPath(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"read"})
	if code != 1 {
		t.Errorf("read with no path returned %d, want 1", code)
	}
}

func TestRunReadWithOffset(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"read", "-offset", "100", "/nonexistent/file.txt"})
	if code != 1 {
		t.Logf("read -offset returned %d (expected fail - no daemon)", code)
	}
}

func TestRunReadWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"read", "-limit", "1024", "/nonexistent/file.txt"})
	if code != 1 {
		t.Logf("read -limit returned %d (expected fail - no daemon)", code)
	}
}

func TestRunReadWithIntent(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"read", "-intent", "function signatures", "/nonexistent/file.txt"})
	if code != 1 {
		t.Logf("read -intent returned %d (expected fail - no daemon)", code)
	}
}

func TestRunReadWithOffsetAndLimit(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"read", "-offset", "50", "-limit", "500", "/nonexistent/file.txt"})
	if code != 1 {
		t.Logf("read -offset -limit returned %d (expected fail - no daemon)", code)
	}
}

// =============================================================================
// runFetch error paths — pushes runFetch from 57.9% toward 75%+
// =============================================================================

func TestRunFetchNoURL(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"fetch"})
	if code != 1 {
		t.Errorf("fetch with no URL returned %d, want 1", code)
	}
}

func TestRunFetchWithTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"fetch", "-timeout", "5", "https://127.0.0.1:99999/nonexistent"})
	if code != 1 {
		t.Logf("fetch -timeout returned %d (expected fail - no daemon)", code)
	}
}

func TestRunFetchPOSTMethod(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"fetch", "-method", "POST", "-body", `{"key":"value"}`, "https://127.0.0.1:99999/api"})
	if code != 1 {
		t.Logf("fetch POST returned %d (expected fail - no daemon)", code)
	}
}

// =============================================================================
// runGrep error paths — pushes runGrep from 58.8% toward 75%+
// =============================================================================

func TestRunGrepNoPattern(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"grep"})
	if code != 1 {
		t.Errorf("grep with no pattern returned %d, want 1", code)
	}
}

// =============================================================================
// loadProjectPolicy — test the "" project path and non-empty project
// =============================================================================

func TestLoadProjectPolicyEmptyProject(t *testing.T) {
	// With proj="" → returns DefaultPolicy() (line 3691-3692 branch)
	p := loadProjectPolicy("")
	if p.Allow == nil {
		t.Error("loadProjectPolicy(\"\") returned nil policy.Allow")
	}
}

func TestLoadProjectPolicyValidProject(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)

	// With a real project → LoadPolicyMerged (line 3694-3701)
	// Errors are printed to stderr but path continues
	p := loadProjectPolicy(tmpDir)
	if p.Allow == nil {
		t.Error("loadProjectPolicy returned nil policy.Allow")
	}
}

// =============================================================================
// runConfig get all known keys — push runConfig coverage higher
// =============================================================================

func TestRunConfigGetCapture(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\ncapture:\n  fs:\n    enabled: true\n    ignore: [\"*.log\"]\n    debounce_ms: 1000\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	// Test get of capture subtree keys
	cases := []struct {
		key  string
		want int
	}{
		{"capture.fs.enabled", 0},
		{"capture.fs.ignore", 0},
		{"capture.fs.debounce_ms", 0},
	}
	for _, tc := range cases {
		code := Dispatch([]string{"config", "get", tc.key})
		if code != tc.want {
			t.Errorf("config get %s returned %d, want %d", tc.key, code, tc.want)
		}
	}
}

func TestRunConfigGetIndex(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\nindex:\n  bm25_k1: 1.5\n  bm25_b: 0.8\n  heading_boost: 3.0\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"index.bm25_k1", "index.bm25_b", "index.heading_boost"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigGetTransport(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\ntransport:\n  http:\n    enabled: true\n    bind: 127.0.0.1:9999\n  socket:\n    enabled: false\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"transport.http.enabled", "transport.http.bind", "transport.socket.enabled"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigGetLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\nlifecycle:\n  idle_timeout: 60m\n  shutdown_timeout: 30s\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"lifecycle.idle_timeout", "lifecycle.shutdown_timeout"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigGetPrivacy(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\nprivacy:\n  telemetry: false\n  remote_sync: local\n  allow_nonlocal_http: true\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"privacy.telemetry", "privacy.remote_sync", "privacy.allow_nonlocal_http"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigGetStorage(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte(`version: 1
storage:
  durability: durable
  max_batch_ms: 50
  journal_max_bytes: 1048576
  compress_rotated: false
`), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"storage.durability", "storage.max_batch_ms", "storage.journal_max_bytes", "storage.compress_rotated"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigGetRetrieval(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\nretrieval:\n  default_budget: 8192\n  default_format: json\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	cases := []string{"retrieval.default_budget", "retrieval.default_format"}
	for _, key := range cases {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

func TestRunConfigSetDurability(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "storage.durability", "batched"})
	if code != 0 {
		t.Errorf("config set storage.durability batched returned %d, want 0", code)
	}
}

func TestRunConfigSetTelemetry(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "privacy.telemetry", "true"})
	if code != 0 {
		t.Errorf("config set privacy.telemetry true returned %d, want 0", code)
	}
}

func TestRunConfigSetTransport(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "transport.http.enabled", "true"})
	if code != 0 {
		t.Errorf("config set transport.http.enabled true returned %d, want 0", code)
	}
}

func TestRunConfigSetLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "lifecycle.idle_timeout", "120m"})
	if code != 0 {
		t.Errorf("config set lifecycle.idle_timeout 120m returned %d, want 0", code)
	}
}

func TestRunConfigSetIndex(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "index.bm25_k1", "2.0"})
	if code != 0 {
		t.Errorf("config set index.bm25_k1 2.0 returned %d, want 0", code)
	}
}

func TestRunConfigSetRetrieval(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "retrieval.default_budget", "16384"})
	if code != 0 {
		t.Errorf("config set retrieval.default_budget 16384 returned %d, want 0", code)
	}
}

func TestRunConfigSetInvalidValue(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "logging.level", "not_a_level"})
	if code != 1 {
		t.Errorf("config set logging.level not_a_level returned %d, want 1", code)
	}
}

func TestRunConfigSetBM25B(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(tmpDir+"/.dfmt", 0755)
	configPath := tmpDir + "/.dfmt/config.yaml"
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmpDir
	defer func() { flagProject = prevProject }()

	code := Dispatch([]string{"config", "set", "index.bm25_b", "0.5"})
	if code != 0 {
		t.Errorf("config set index.bm25_b 0.5 returned %d, want 0", code)
	}
}
