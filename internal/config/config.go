package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const durabilityBatched = "batched"

// Config represents the DFMT configuration.
type Config struct {
	Version int `yaml:"version"`

	Capture struct {
		MCP struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"mcp"`
		FS struct {
			Enabled    bool     `yaml:"enabled"`
			Watch      []string `yaml:"watch"`
			Ignore     []string `yaml:"ignore"`
			DebounceMS int      `yaml:"debounce_ms"`
		} `yaml:"fs"`
		Git struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"git"`
		Shell struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"shell"`
	} `yaml:"capture"`

	Storage struct {
		Durability      string `yaml:"durability"` // "durable" | "batched"
		MaxBatchMS      int    `yaml:"max_batch_ms"`
		JournalMaxBytes int64  `yaml:"journal_max_bytes"`
		CompressRotated bool   `yaml:"compress_rotated"`
	} `yaml:"storage"`

	Retrieval struct {
		DefaultBudget int    `yaml:"default_budget"`
		DefaultFormat string `yaml:"default_format"`
		Throttle      struct {
			FirstTierCalls    int `yaml:"first_tier_calls"`
			SecondTierCalls   int `yaml:"second_tier_calls"`
			ResultsFirstTier  int `yaml:"results_first_tier"`
			ResultsSecondTier int `yaml:"results_second_tier"`
		} `yaml:"throttle"`
	} `yaml:"retrieval"`

	Index struct {
		RebuildInterval string  `yaml:"rebuild_interval"`
		BM25K1          float64 `yaml:"bm25_k1"`
		BM25B           float64 `yaml:"bm25_b"`
		HeadingBoost    float64 `yaml:"heading_boost"`
		StopwordsPath   string  `yaml:"stopwords_path"`
	} `yaml:"index"`

	Transport struct {
		MCP struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"mcp"`
		HTTP struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
		} `yaml:"http"`
		Socket struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"socket"`
	} `yaml:"transport"`

	Lifecycle struct {
		IdleTimeout     string `yaml:"idle_timeout"`
		ShutdownTimeout string `yaml:"shutdown_timeout"`
	} `yaml:"lifecycle"`

	Privacy struct {
		Telemetry         bool   `yaml:"telemetry"`
		RemoteSync        string `yaml:"remote_sync"`
		AllowNonlocalHTTP bool   `yaml:"allow_nonlocal_http"`
	} `yaml:"privacy"`

	Logging struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"logging"`
}

// Default returns a fully populated Config with all defaults.
func Default() *Config {
	c := &Config{
		Version: 1,
	}

	// Capture defaults
	c.Capture.MCP.Enabled = true
	// FSWatcher wire-up is opt-in: set capture.fs.enabled=true in the
	// project config to start indexing file-system activity.
	c.Capture.FS.Enabled = false
	c.Capture.FS.Watch = []string{"**"}
	c.Capture.FS.Ignore = []string{".git/**", "node_modules/**", "__pycache__/**"}
	c.Capture.FS.DebounceMS = 500
	c.Capture.Git.Enabled = true
	c.Capture.Shell.Enabled = true

	// Storage defaults
	c.Storage.Durability = durabilityBatched
	c.Storage.MaxBatchMS = 100
	c.Storage.JournalMaxBytes = 10 * 1024 * 1024 // 10MB
	c.Storage.CompressRotated = true

	// Retrieval defaults
	c.Retrieval.DefaultBudget = 4096
	c.Retrieval.DefaultFormat = "md"
	c.Retrieval.Throttle.FirstTierCalls = 10
	c.Retrieval.Throttle.SecondTierCalls = 5
	c.Retrieval.Throttle.ResultsFirstTier = 20
	c.Retrieval.Throttle.ResultsSecondTier = 10

	// Index defaults
	c.Index.RebuildInterval = "1h"
	c.Index.BM25K1 = 1.2
	c.Index.BM25B = 0.75
	c.Index.HeadingBoost = 5.0

	// Transport defaults
	c.Transport.MCP.Enabled = true
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
	c.Logging.Level = "info"
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
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip missing files
		}
		return err
	}

	return yaml.Unmarshal(data, cfg)
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
		return fmt.Errorf("storage.max_batch_ms must be non-negative")
	}

	if c.Storage.JournalMaxBytes < 0 {
		return fmt.Errorf("storage.journal_max_bytes must be non-negative")
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

	return nil
}
