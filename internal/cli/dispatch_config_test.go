package cli

import (
	"encoding/json"
	"testing"

	"github.com/ersinkoc/dfmt/internal/config"
)

// getConfigFieldCases covers all branches in getConfigField.
var getConfigFieldCases = []struct {
	key     string
	wantVal string
	wantOk  bool
}{
	{"version", "1", true},
	{"capture.fs.enabled", "false", true},
	{"capture.fs.debounce_ms", "500", true},
	{"storage.durability", "batched", true},
	{"storage.max_batch_ms", "100", true},
	{"storage.journal_max_bytes", "10485760", true},
	{"storage.compress_rotated", "true", true},
	{"retrieval.default_budget", "4096", true},
	{"retrieval.default_format", "md", true},
	{"index.bm25_k1", "1.2", true},
	{"index.bm25_b", "0.75", true},
	{"index.heading_boost", "5", true},
	{"transport.http.enabled", "false", true},
	{"transport.http.bind", "127.0.0.1:8765", true},
	{"transport.socket.enabled", "true", true},
	{"lifecycle.idle_timeout", "30m", true},
	{"lifecycle.shutdown_timeout", "10s", true},
	{"privacy.telemetry", "false", true},
	{"privacy.remote_sync", "none", true},
	{"privacy.allow_nonlocal_http", "false", true},
	{"logging.level", "warn", true},
	{"logging.format", "text", true},
	{"exec.path_prepend", "[]", true},
	{"unknown.key", "", false},
}

func TestGetConfigFieldAllCases(t *testing.T) {
	cfg := config.Default()
	for _, tc := range getConfigFieldCases {
		t.Run(tc.key+"_"+tc.wantVal, func(t *testing.T) {
			gotVal, gotOk := getConfigField(cfg, tc.key)
			if gotOk != tc.wantOk {
				t.Errorf("getConfigField(cfg, %q) ok = %v, want %v", tc.key, gotOk, tc.wantOk)
				return
			}
			if gotOk && gotVal != tc.wantVal {
				t.Errorf("getConfigField(cfg, %q) = %q, want %q", tc.key, gotVal, tc.wantVal)
			}
		})
	}
}

// setConfigFieldCases covers all branches in setConfigField.
var setConfigFieldCases = []struct {
	key      string
	value    string
	wantErr  bool
	modifyFn func(*config.Config) // apply expected change
}{
	{"capture.fs.enabled", "true", false, func(c *config.Config) { c.Capture.FS.Enabled = true }},
	{"capture.fs.enabled", "false", false, func(c *config.Config) { c.Capture.FS.Enabled = false }},
	{"capture.fs.debounce_ms", "500", false, func(c *config.Config) { c.Capture.FS.DebounceMS = 500 }},
	{"capture.fs.debounce_ms", "not-an-int", true, nil},
	{"capture.fs.ignore", `["*.log","*.tmp"]`, false, func(c *config.Config) { c.Capture.FS.Ignore = []string{"*.log", "*.tmp"} }},
	{"capture.fs.ignore", "not-json", true, nil},
	{"storage.durability", "durable", false, func(c *config.Config) { c.Storage.Durability = "durable" }},
	{"storage.max_batch_ms", "200", false, func(c *config.Config) { c.Storage.MaxBatchMS = 200 }},
	{"storage.max_batch_ms", "bad", true, nil},
	{"storage.journal_max_bytes", "2097152", false, func(c *config.Config) { c.Storage.JournalMaxBytes = 2 << 20 }},
	{"storage.journal_max_bytes", "nan", true, nil},
	{"storage.compress_rotated", "true", false, func(c *config.Config) { c.Storage.CompressRotated = true }},
	{"storage.compress_rotated", "false", false, func(c *config.Config) { c.Storage.CompressRotated = false }},
	{"retrieval.default_budget", "8192", false, func(c *config.Config) { c.Retrieval.DefaultBudget = 8192 }},
	{"retrieval.default_budget", "xyz", true, nil},
	{"retrieval.default_format", "json", false, func(c *config.Config) { c.Retrieval.DefaultFormat = "json" }},
	{"retrieval.default_format", "xml", false, func(c *config.Config) { c.Retrieval.DefaultFormat = "xml" }},
	{"retrieval.default_format", "md", false, func(c *config.Config) { c.Retrieval.DefaultFormat = "md" }},
	{"index.bm25_k1", "1.5", false, func(c *config.Config) { c.Index.BM25K1 = 1.5 }},
	{"index.bm25_k1", "oops", true, nil},
	{"index.bm25_b", "0.5", false, func(c *config.Config) { c.Index.BM25B = 0.5 }},
	{"index.bm25_b", "oops", true, nil},
	{"index.heading_boost", "2.0", false, func(c *config.Config) { c.Index.HeadingBoost = 2.0 }},
	{"index.heading_boost", "oops", true, nil},
	{"transport.http.enabled", "true", false, func(c *config.Config) { c.Transport.HTTP.Enabled = true }},
	{"transport.http.enabled", "false", false, func(c *config.Config) { c.Transport.HTTP.Enabled = false }},
	{"transport.http.bind", "0.0.0.0:9000", false, func(c *config.Config) { c.Transport.HTTP.Bind = "0.0.0.0:9000" }},
	{"transport.socket.enabled", "true", false, func(c *config.Config) { c.Transport.Socket.Enabled = true }},
	{"transport.socket.enabled", "false", false, func(c *config.Config) { c.Transport.Socket.Enabled = false }},
	{"lifecycle.idle_timeout", "10m", false, func(c *config.Config) { c.Lifecycle.IdleTimeout = "10m" }},
	{"lifecycle.shutdown_timeout", "60s", false, func(c *config.Config) { c.Lifecycle.ShutdownTimeout = "60s" }},
	{"privacy.telemetry", "true", false, func(c *config.Config) { c.Privacy.Telemetry = true }},
	{"privacy.telemetry", "false", false, func(c *config.Config) { c.Privacy.Telemetry = false }},
	{"privacy.remote_sync", "sync", false, func(c *config.Config) { c.Privacy.RemoteSync = "sync" }},
	{"privacy.allow_nonlocal_http", "true", false, func(c *config.Config) { c.Privacy.AllowNonlocalHTTP = true }},
	{"privacy.allow_nonlocal_http", "false", false, func(c *config.Config) { c.Privacy.AllowNonlocalHTTP = false }},
	{"logging.level", "debug", false, func(c *config.Config) { c.Logging.Level = "debug" }},
	{"logging.level", "info", false, func(c *config.Config) { c.Logging.Level = "info" }},
	{"logging.level", "error", false, func(c *config.Config) { c.Logging.Level = "error" }},
	{"logging.format", "text", false, func(c *config.Config) { c.Logging.Format = "text" }},
	{"exec.path_prepend", "anything", true, nil},
	{"unknown.key", "val", true, nil},
	{"version", "2", true, nil},
}

func TestSetConfigFieldAllCases(t *testing.T) {
	for _, tc := range setConfigFieldCases {
		t.Run(tc.key+":"+tc.value, func(t *testing.T) {
			cfg := &config.Config{}
			err := setConfigField(cfg, tc.key, tc.value)
			if (err != nil) != tc.wantErr {
				t.Errorf("setConfigField(cfg, %q, %q) error = %v, wantErr %v", tc.key, tc.value, err, tc.wantErr)
				return
			}
			if tc.modifyFn != nil && err == nil {
				want := &config.Config{}
				tc.modifyFn(want)
				if cfg.Index.BM25K1 != want.Index.BM25K1 || cfg.Index.BM25B != want.Index.BM25B ||
					cfg.Storage.Durability != want.Storage.Durability ||
					cfg.Storage.MaxBatchMS != want.Storage.MaxBatchMS ||
					cfg.Storage.JournalMaxBytes != want.Storage.JournalMaxBytes ||
					cfg.Storage.CompressRotated != want.Storage.CompressRotated ||
					cfg.Capture.FS.Enabled != want.Capture.FS.Enabled ||
					cfg.Capture.FS.DebounceMS != want.Capture.FS.DebounceMS ||
					cfg.Capture.FS.Ignore == nil && want.Capture.FS.Ignore != nil ||
					cfg.Capture.FS.Ignore != nil && want.Capture.FS.Ignore == nil {
					t.Errorf("setConfigField(cfg, %q, %q) mismatch", tc.key, tc.value)
				}
			}
		})
	}
}

// TestSetConfigFieldVersionReadOnly specifically exercises the version read-only error path.
func TestSetConfigFieldVersionReadOnly(t *testing.T) {
	cfg := &config.Config{Version: 1}
	err := setConfigField(cfg, "version", "999")
	if err == nil {
		t.Fatal("setConfigField version should return error")
	}
	if got := err.Error(); got != "version is read-only" {
		t.Errorf("error = %q, want %q", got, "version is read-only")
	}
}

// TestSetConfigFieldExecPathPrependListError exercises the exec.path_prepend list-type guard.
func TestSetConfigFieldExecPathPrependListError(t *testing.T) {
	cfg := &config.Config{}
	err := setConfigField(cfg, "exec.path_prepend", "/some/path")
	if err == nil {
		t.Fatal("setConfigField exec.path_prepend should return error for non-list value")
	}
	if got := err.Error(); got != "exec.path_prepend is a list; use config get to inspect and manual edit to change" {
		t.Errorf("error = %q, want %q", got, "exec.path_prepend is a list; use config get to inspect and manual edit to change")
	}
}

// TestSetConfigFieldUnknownKeyError exercises the default branch (unknown key).
func TestSetConfigFieldUnknownKeyError(t *testing.T) {
	cfg := &config.Config{}
	err := setConfigField(cfg, "does.not.exist", "val")
	if err == nil {
		t.Fatal("setConfigField unknown key should return error")
	}
	if got := err.Error(); got != `unknown config key "does.not.exist"` {
		t.Errorf("error = %q, want %q", got, `unknown config key "does.not.exist"`)
	}
}

// TestGetConfigFieldCaptureFSIgnore covers the json.Marshal round-trip for
// capture.fs.ignore which uses []string serialized as a JSON array.
func TestGetConfigFieldCaptureFSIgnore(t *testing.T) {
	cfg := config.Default()
	cfg.Capture.FS.Ignore = []string{"*.log", "*.tmp", "**/.git/**"}
	got, ok := getConfigField(cfg, "capture.fs.ignore")
	if !ok {
		t.Fatal("getConfigField returned false for capture.fs.ignore")
	}
	var gotSlice []string
	if err := json.Unmarshal([]byte(got), &gotSlice); err != nil {
		t.Fatalf("getConfigField returned non-JSON: %s", got)
	}
	if len(gotSlice) != 3 || gotSlice[0] != "*.log" {
		t.Errorf("capture.fs.ignore = %v, want [*.log *.tmp **/.git/**]", gotSlice)
	}
}

// TestSetConfigFieldCaptureFSIgnore covers the JSON array parse for capture.fs.ignore.
func TestSetConfigFieldCaptureFSIgnore(t *testing.T) {
	cfg := &config.Config{}
	err := setConfigField(cfg, "capture.fs.ignore", `["*.exe","*.dll"]`)
	if err != nil {
		t.Fatalf("setConfigField capture.fs.ignore failed: %v", err)
	}
	if len(cfg.Capture.FS.Ignore) != 2 || cfg.Capture.FS.Ignore[0] != "*.exe" {
		t.Errorf("capture.fs.ignore = %v, want [*.exe *.dll]", cfg.Capture.FS.Ignore)
	}
}
