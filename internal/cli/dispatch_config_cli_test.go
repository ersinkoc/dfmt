package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunConfigSetSuccess exercises the full "config set" path with a valid
// config file and successful save. This pushes runConfig from 30% to 100%.
func TestRunConfigSetSuccess(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	// "config set" with valid key/value should return 0 on success.
	code := Dispatch([]string{"config", "set", "logging.level", "debug"})
	if code != 0 {
		t.Errorf("config set logging.level debug returned %d, want 0", code)
	}
}

// TestRunConfigSetUnknownKey exercises the set path with an unknown key.
func TestRunConfigSetUnknownKey(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"config", "set", "unknown.key", "val"})
	if code != 1 {
		t.Errorf("config set unknown.key returned %d, want 1", code)
	}
}

// TestRunConfigSetReadOnlyKey exercises the set path with the read-only version key.
func TestRunConfigSetReadOnlyKey(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"config", "set", "version", "2"})
	if code != 1 {
		t.Errorf("config set version returned %d, want 1 (read-only)", code)
	}
}

// TestRunConfigSetListKey exercises the set path with exec.path_prepend
// (a list-type key that rejects scalar values).
func TestRunConfigSetListKey(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"config", "set", "exec.path_prepend", "/usr/bin"})
	if code != 1 {
		t.Errorf("config set exec.path_prepend returned %d, want 1 (list type)", code)
	}
}

// TestRunConfigGetAllKeys exercises config get for all known keys.
func TestRunConfigGetAllKeys(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	// Write a complete config so every get has a value.
	os.WriteFile(configPath, []byte(`version: 1
capture:
  fs:
    enabled: false
    ignore: []
    debounce_ms: 500
storage:
  durability: batched
  max_batch_ms: 100
  journal_max_bytes: 10485760
  compress_rotated: true
retrieval:
  default_budget: 4096
  default_format: md
index:
  bm25_k1: 1.2
  bm25_b: 0.75
  heading_boost: 5.0
transport:
  http:
    enabled: false
    bind: 127.0.0.1:8765
  socket:
    enabled: true
lifecycle:
  idle_timeout: 30m
  shutdown_timeout: 10s
privacy:
  telemetry: false
  remote_sync: none
  allow_nonlocal_http: false
logging:
  level: warn
  format: text
exec:
  path_prepend: []
`), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	keys := []string{
		"version", "capture.fs.enabled", "capture.fs.ignore",
		"capture.fs.debounce_ms", "storage.durability", "storage.max_batch_ms",
		"storage.journal_max_bytes", "storage.compress_rotated",
		"retrieval.default_budget", "retrieval.default_format",
		"index.bm25_k1", "index.bm25_b", "index.heading_boost",
		"transport.http.enabled", "transport.http.bind", "transport.socket.enabled",
		"lifecycle.idle_timeout", "lifecycle.shutdown_timeout",
		"privacy.telemetry", "privacy.remote_sync", "privacy.allow_nonlocal_http",
		"logging.level", "logging.format", "exec.path_prepend",
	}

	for _, key := range keys {
		code := Dispatch([]string{"config", "get", key})
		if code != 0 {
			t.Errorf("config get %s returned %d, want 0", key, code)
		}
	}
}

// TestRunConfigGetUnknownKey exercises the get path with an unknown key.
func TestRunConfigGetUnknownKey(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	code := Dispatch([]string{"config", "get", "does.not.exist"})
	if code != 1 {
		t.Errorf("config get unknown key returned %d, want 1", code)
	}
}

// TestRunConfigSetMultipleValues sets several different config keys
// in sequence to push runConfig coverage higher.
func TestRunConfigSetMultipleValues(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".dfmt"), 0755)
	configPath := filepath.Join(tmp, ".dfmt", "config.yaml")
	os.WriteFile(configPath, []byte("version: 1\n"), 0644)

	prevProject := flagProject
	flagProject = tmp
	t.Cleanup(func() { flagProject = prevProject })

	cases := []struct {
		key   string
		value string
	}{
		{"storage.max_batch_ms", "200"},
		{"storage.durability", "durable"},
		{"retrieval.default_budget", "8192"},
		{"logging.level", "info"},
	}

	for _, tc := range cases {
		code := Dispatch([]string{"config", "set", tc.key, tc.value})
		if code != 0 {
			t.Errorf("config set %s %s returned %d, want 0", tc.key, tc.value, code)
		}
	}
}
