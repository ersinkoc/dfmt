package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Config.Save and DefaultConfigYAML were both uncovered. Save is the write
// half of the config round-trip — `dfmt init` and every config-mutating
// command land here — so a regression would corrupt or refuse to persist a
// project's settings with nothing catching it until a user hit it.

func TestConfigSaveRoundTrips(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	cfg.Lifecycle.IdleTimeout = "17m"

	if err := cfg.Save(proj); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(proj, ".dfmt", "config.yaml")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	// 0600: the config can carry redaction patterns and permission rules;
	// other local users have no business reading it. Windows has no Unix
	// mode bits — Go reports 0666 for any writable file there — so the
	// assertion would be meaningless rather than merely lenient.
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("config mode = %o, want no group/other bits", perm)
		}
	}

	reloaded, err := Load(proj)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Lifecycle.IdleTimeout != "17m" {
		t.Errorf("IdleTimeout = %q after round-trip, want %q",
			reloaded.Lifecycle.IdleTimeout, "17m")
	}
}

// Save validates before writing, so an invalid config never reaches disk in
// a state the next Load would reject — that would leave the project unable
// to start a daemon at all.
func TestConfigSaveRejectsInvalidConfig(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".dfmt"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	cfg.Lifecycle.IdleTimeout = "not-a-duration"

	if err := cfg.Save(proj); err == nil {
		t.Fatal("Save accepted an unparseable duration")
	}
	if _, err := os.Stat(filepath.Join(proj, ".dfmt", "config.yaml")); err == nil {
		t.Error("invalid config was written to disk anyway")
	}
}

// The embedded default YAML is what `dfmt init` writes into a fresh
// project. It must parse as the config it claims to be, or every new
// project starts broken.
func TestDefaultConfigYAMLIsValid(t *testing.T) {
	body := DefaultConfigYAML()
	if strings.TrimSpace(body) == "" {
		t.Fatal("DefaultConfigYAML is empty")
	}

	proj := t.TempDir()
	dir := filepath.Join(proj, ".dfmt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(proj)
	if err != nil {
		t.Fatalf("the shipped default config does not load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the shipped default config does not validate: %v", err)
	}
}
