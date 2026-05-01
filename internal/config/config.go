package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const durabilityBatched = "batched"

const maxConfigBytes = 64 << 10

// DefaultMCPProtocolVersion is the default MCP protocol version.
const DefaultMCPProtocolVersion = "2024-11-05"

// Config represents the DFMT configuration.
//
// Knob status is documented in ADR-0015 ("Config knob consolidation").
// Every field here is parsed and validated, but not every field is read at
// runtime — fields without a production caller are flagged "Reserved (v0.4)"
// in the comment alongside their declaration. Operators editing reserved
// fields will see them accepted by Validate() and silently ignored at
// runtime; this is documented behavior, not a bug.
type Config struct {
	Version int `yaml:"version"`

	// Capture: only FS is wired today.
	//
	// `capture.mcp`, `capture.git`, `capture.shell`, `capture.fs.watch`
	// were REMOVED 2026-05-02 per ADR-0015 — each is gated elsewhere
	// (MCP capture follows the transport up/down; git capture by
	// `dfmt install-hooks`; shell capture by `dfmt shell-init` env
	// integration; FSWatcher reads everything under root with ignore
	// filtering, the watch list was never consumed). KnownFields(true)
	// surfaces a clear "field not found" error if an old config still
	// sets them.
	Capture struct {
		FS struct {
			Enabled    bool     `yaml:"enabled"`     // wired: daemon.go FSWatcher init
			Ignore     []string `yaml:"ignore"`      // wired: daemon.go FSWatcher init
			DebounceMS int      `yaml:"debounce_ms"` // wired: daemon.go FSWatcher init
		} `yaml:"fs"`
	} `yaml:"capture"`

	// All four Storage fields are wired (daemon.go ~line 105).
	Storage struct {
		Durability      string `yaml:"durability"` // "durable" | "batched"
		MaxBatchMS      int    `yaml:"max_batch_ms"`
		JournalMaxBytes int64  `yaml:"journal_max_bytes"`
		CompressRotated bool   `yaml:"compress_rotated"`
	} `yaml:"storage"`

	// Retrieval — DefaultBudget and DefaultFormat are wired
	// (handlers.Recall calls SetRecallDefaults; per-call values still
	// win, the operator override fills in for omitted fields).
	// `retrieval.throttle.*` (4 sub-knobs) was REMOVED 2026-05-02 per
	// ADR-0015: no throttle implementation existed in any version, the
	// fields were silently ignored at runtime. Strict YAML parsing
	// (KnownFields(true)) means an old config that still sets them
	// will fail to load with a clear "field not found" error — better
	// diagnostic than the previous silent drop.
	Retrieval struct {
		DefaultBudget int    `yaml:"default_budget"`
		DefaultFormat string `yaml:"default_format"`
	} `yaml:"retrieval"`

	// Index.BM25K1 / Index.BM25B — wired (core.NewIndexWithParams +
	// Index.SetParams). Daemon plumbs config values at startup; the
	// search path reads them via NewBM25OkapiWithParams which falls
	// back to package defaults on zero/out-of-range input.
	// Index.HeadingBoost — stored on the Index for forward compat but
	// no scoring path consumes it today. Reserved (v0.4) pending a
	// heading-event-type ADR.
	// `index.rebuild_interval` and `index.stopwords_path` were REMOVED
	// 2026-05-02 per ADR-0015: neither had a caller in any version.
	// Strict YAML parsing surfaces a clear error if an old config sets
	// them (vs. the previous silent ignore).
	Index struct {
		BM25K1       float64 `yaml:"bm25_k1"`
		BM25B        float64 `yaml:"bm25_b"`
		HeadingBoost float64 `yaml:"heading_boost"`
	} `yaml:"index"`

	Transport struct {
		// `transport.mcp.enabled` was REMOVED 2026-05-02 per ADR-0015:
		// MCP is served on stdio when `dfmt mcp` is the CLI entry point.
		// The daemon does not own the MCP transport's life-cycle — the
		// parent process model does — so a daemon-side toggle is
		// fictional. KnownFields(true) surfaces a clear error for old
		// configs that still set it.
		// HTTP.Enabled and HTTP.Bind are wired (daemon.go:185, 201, 215).
		HTTP struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
		} `yaml:"http"`
		// Socket.Enabled — wired (daemon.go default branch). On Unix
		// without TCP opt-in, setting this to false makes the daemon
		// refuse to start unless transport.http.enabled is also true —
		// the operator-chosen replacement transport. On Windows the
		// field is a no-op (no Unix-socket support); any value parses
		// cleanly. ADR-0015 v0.4.
		Socket struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"socket"`
	} `yaml:"transport"`

	// Lifecycle.IdleTimeout — wired (daemon idle monitor).
	// Lifecycle.ShutdownTimeout — wired (Daemon.ShutdownGrace; idle-monitor
	// stop + dispatch.go SIGTERM handler both bracket d.Stop() with a
	// context.WithTimeout sized from this knob). Default 10s; the fallback
	// path returns the same value if the YAML field is empty or unparseable
	// (Validate already rejects the latter at startup).
	Lifecycle struct {
		IdleTimeout     string `yaml:"idle_timeout"`
		ShutdownTimeout string `yaml:"shutdown_timeout"`
	} `yaml:"lifecycle"`

	// Privacy — All Reserved (v0.4). The defaults (telemetry off, no
	// remote sync, deny non-local HTTP) match the daemon's hard-coded
	// behavior; flipping these in YAML changes nothing yet. Telemetry
	// and remote sync are explicit non-features for v1.0.
	Privacy struct {
		Telemetry         bool   `yaml:"telemetry"`
		RemoteSync        string `yaml:"remote_sync"`
		AllowNonlocalHTTP bool   `yaml:"allow_nonlocal_http"`
	} `yaml:"privacy"`

	// Logging.Level — wired (logging.ApplyConfig). Precedence per
	// ADR-0015 forward declaration: DFMT_LOG env > yaml > package
	// default (LevelWarn). Validate allowlists debug|info|warn|warning|
	// error|off|none|silent.
	// Logging.Format — Reserved (v0.4). Currently the only emitter is
	// the byte-identical-to-pre-migration text shape; a JSON / slog
	// alternative would need a separate ADR (text-format stability is
	// a script-parser contract). The YAML field stays so operator
	// configs that already set it parse cleanly.
	Logging struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"logging"`

	// Exec controls how the sandbox runs subprocesses. PathPrepend is the
	// fix for the recurring "exit 127, command not found" symptom that
	// shows up when the daemon was auto-started from a shell whose PATH
	// did not include the language toolchains the agent needs (Go, Node,
	// Python). Each listed directory is prepended (in order) to the
	// sandbox's PATH for every exec call. Project-scope configures it for
	// the team; user-scope (~/.local/share/dfmt/config.yaml) configures
	// it for a single machine.
	Exec struct {
		PathPrepend []string `yaml:"path_prepend"`
	} `yaml:"exec"`
}

// Default returns a fully populated Config with all defaults.
func Default() *Config {
	c := &Config{
		Version: 1,
	}

	// Capture defaults (only FS is wired; see struct comment).
	// FSWatcher wire-up is opt-in: set capture.fs.enabled=true in the
	// project config to start indexing file-system activity.
	c.Capture.FS.Enabled = false
	c.Capture.FS.Ignore = []string{".git/**", "node_modules/**", "__pycache__/**"}
	c.Capture.FS.DebounceMS = 500

	// Storage defaults
	c.Storage.Durability = durabilityBatched
	c.Storage.MaxBatchMS = 100
	c.Storage.JournalMaxBytes = 10 * 1024 * 1024 // 10MB
	c.Storage.CompressRotated = true

	// Retrieval defaults
	c.Retrieval.DefaultBudget = 4096
	c.Retrieval.DefaultFormat = "md"

	// Index defaults
	c.Index.BM25K1 = 1.2
	c.Index.BM25B = 0.75
	c.Index.HeadingBoost = 5.0

	// Transport defaults
	c.Transport.HTTP.Enabled = false
	c.Transport.HTTP.Bind = "127.0.0.1:8765"
	c.Transport.Socket.Enabled = true

	// Lifecycle defaults
	c.Lifecycle.IdleTimeout = "30m"
	c.Lifecycle.ShutdownTimeout = "10s"

	// Privacy defaults
	c.Privacy.Telemetry = false
	c.Privacy.RemoteSync = "none"
	c.Privacy.AllowNonlocalHTTP = false

	// Logging defaults
	// Default level matches the pre-ADR-0015-wire-up logging package
	// default (LevelWarn). Operators wanting the chattier baseline set
	// `logging.level: info` in YAML or DFMT_LOG=info in env.
	c.Logging.Level = "warn"
	c.Logging.Format = "text"

	return c
}

// Load reads global then project YAML and merges (project wins).
func Load(projectPath string) (*Config, error) {
	// Start with defaults
	cfg := Default()

	// Load global config
	globalPath := globalConfigPath()
	if globalPath != "" {
		if err := merge(cfg, globalPath); err != nil {
			return nil, fmt.Errorf("global config: %w", err)
		}
	}

	// Load project config (overrides global)
	if projectPath != "" {
		projectConfigPath := filepath.Join(projectPath, ".dfmt", "config.yaml")
		if _, err := os.Stat(projectConfigPath); err == nil {
			if err := merge(cfg, projectConfigPath); err != nil {
				return nil, fmt.Errorf("project config: %w", err)
			}
		}
	}

	// Validate the merged result so typo'd durability values, malformed
	// idle_timeout strings, etc. surface immediately instead of silently
	// degrading to defaults at runtime.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config invalid: %w", err)
	}

	return cfg, nil
}

// globalConfigPath returns the path to the global config file.
func globalConfigPath() string {
	// XDG_DATA_HOME/dfmt/config.yaml or ~/.local/share/dfmt/config.yaml
	xdgDataHome := os.Getenv("XDG_DATA_HOME")
	if xdgDataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		xdgDataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdgDataHome, "dfmt", "config.yaml")
}

// merge reads a YAML file and merges its values into cfg.
func merge(cfg *Config, path string) error {
	data, err := readConfigFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip missing files
		}
		return err
	}

	return decodeConfigYAML(data, cfg)
}

func decodeConfigYAML(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("config file must contain exactly one YAML document")
	}
	return nil
}

func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("config file too large: exceeds %d bytes", maxConfigBytes)
	}
	return data, nil
}

// ParseDuration parses a duration string.
func ParseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// Validate checks if the config values are valid.
func (c *Config) Validate() error {
	if c.Storage.Durability != "durable" && c.Storage.Durability != "batched" {
		return fmt.Errorf("storage.durability must be 'durable' or 'batched', got %q", c.Storage.Durability)
	}

	if c.Storage.MaxBatchMS < 0 {
		return errors.New("storage.max_batch_ms must be non-negative")
	}

	if c.Storage.JournalMaxBytes < 0 {
		return errors.New("storage.journal_max_bytes must be non-negative")
	}

	if c.Lifecycle.IdleTimeout != "" {
		if _, err := time.ParseDuration(c.Lifecycle.IdleTimeout); err != nil {
			return fmt.Errorf("lifecycle.idle_timeout: %w", err)
		}
	}

	if c.Lifecycle.ShutdownTimeout != "" {
		if _, err := time.ParseDuration(c.Lifecycle.ShutdownTimeout); err != nil {
			return fmt.Errorf("lifecycle.shutdown_timeout: %w", err)
		}
	}

	// BM25 parameters must be within sensible ranges. Negative k1 or b
	// produce NaN BM25 scores; b outside [0,1] makes length normalization
	// nonsensical. Non-positive budget makes Recall return empty.
	if c.Index.BM25K1 < 0 {
		return fmt.Errorf("index.bm25_k1 must be non-negative, got %v", c.Index.BM25K1)
	}
	if c.Index.BM25B < 0 || c.Index.BM25B > 1 {
		return fmt.Errorf("index.bm25_b must be in [0,1], got %v", c.Index.BM25B)
	}
	if c.Index.HeadingBoost < 0 {
		return fmt.Errorf("index.heading_boost must be non-negative, got %v", c.Index.HeadingBoost)
	}
	if c.Retrieval.DefaultBudget < 0 {
		return fmt.Errorf("retrieval.default_budget must be non-negative, got %d", c.Retrieval.DefaultBudget)
	}
	// retrieval.default_format is wired into handlers.Recall as the
	// operator-override fallback. Empty means "use the package default
	// (md)"; non-empty must be one of the formats handlers.Recall knows
	// how to render so a typo doesn't reach the agent as a runtime
	// error after every Recall call.
	switch c.Retrieval.DefaultFormat {
	case "", "md", "json", "xml":
	default:
		return fmt.Errorf("retrieval.default_format must be one of md|json|xml (or empty), got %q", c.Retrieval.DefaultFormat)
	}

	// logging.level allowlist matches logging.parseLevel — keeps a
	// typo'd `logging.level: warm` from being silently ignored at
	// daemon start. Empty is the documented "use package default" value.
	switch c.Logging.Level {
	case "", "debug", "info", "warn", "warning", "error", "off", "none", "silent":
	default:
		return fmt.Errorf("logging.level must be one of debug|info|warn|warning|error|off|none|silent (or empty), got %q", c.Logging.Level)
	}
	// logging.format is still Reserved (v0.4): only "text" is a real
	// emitter today. Operators who set it to anything else are silently
	// no-op'd by the logging package; reject at config load so the
	// surprise doesn't ship.
	switch c.Logging.Format {
	case "", "text":
	default:
		return fmt.Errorf("logging.format must be \"text\" (or empty); JSON output is on the v0.4 roadmap, got %q", c.Logging.Format)
	}

	for i, p := range c.Exec.PathPrepend {
		if p == "" {
			return fmt.Errorf("exec.path_prepend[%d] is empty", i)
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("exec.path_prepend[%d] must be absolute, got %q", i, p)
		}
	}

	return nil
}
