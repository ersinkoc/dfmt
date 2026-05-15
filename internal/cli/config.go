package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/ersinkoc/dfmt/internal/config"
)

func runConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: dfmt config [get <key> | set <key> <value>]\n")
		return 1
	}
	// Help anywhere in the args list prints usage instead of treating
	// "--help" as a config key — pre-fix, `dfmt config get --help` errored
	// with "unknown config key '--help'".
	if helpRequested(args) {
		fmt.Println("usage: dfmt config [get <key> | set <key> <value>]")
		return 0
	}
	proj, _ := getProject()
	cfg, err := config.Load(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch args[0] {
	case "get":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "usage: dfmt config get <key>\n")
			return 1
		}
		val, ok := getConfigField(cfg, args[1])
		if !ok {
			fmt.Fprintf(os.Stderr, "error: unknown config key %q\n", args[1])
			return 1
		}
		fmt.Println(val)

	case "set":
		if len(args) != 3 {
			fmt.Fprintf(os.Stderr, "usage: dfmt config set <key> <value>\n")
			return 1
		}
		if proj == "" {
			fmt.Fprintf(os.Stderr, "error: no project path; run from a project directory or set --project\n")
			return 1
		}
		if err := setConfigField(cfg, args[1], args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := cfg.Save(proj); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("ok: %s = %s\n", args[1], args[2])

	default:
		fmt.Fprintf(os.Stderr, "usage: dfmt config [get <key> | set <key> <value>]\n")
		return 1
	}
	return 0
}

// getConfigField returns the YAML value of a dot-delimited key path.
func getConfigField(cfg *config.Config, key string) (string, bool) {
	switch key {
	case "version":
		return fmt.Sprintf("%d", cfg.Version), true
	case "capture.fs.enabled":
		return fmt.Sprintf("%v", cfg.Capture.FS.Enabled), true
	case "capture.fs.ignore":
		b, _ := json.Marshal(cfg.Capture.FS.Ignore)
		return string(b), true
	case "capture.fs.debounce_ms":
		return fmt.Sprintf("%d", cfg.Capture.FS.DebounceMS), true
	case "storage.durability":
		return cfg.Storage.Durability, true
	case "storage.max_batch_ms":
		return fmt.Sprintf("%d", cfg.Storage.MaxBatchMS), true
	case "storage.journal_max_bytes":
		return fmt.Sprintf("%d", cfg.Storage.JournalMaxBytes), true
	case "storage.compress_rotated":
		return fmt.Sprintf("%v", cfg.Storage.CompressRotated), true
	case "retrieval.default_budget":
		return fmt.Sprintf("%d", cfg.Retrieval.DefaultBudget), true
	case "retrieval.default_format":
		return cfg.Retrieval.DefaultFormat, true
	case "index.bm25_k1":
		return fmt.Sprintf("%g", cfg.Index.BM25K1), true
	case "index.bm25_b":
		return fmt.Sprintf("%g", cfg.Index.BM25B), true
	case "index.heading_boost":
		return fmt.Sprintf("%g", cfg.Index.HeadingBoost), true
	case "transport.http.enabled":
		return fmt.Sprintf("%v", cfg.Transport.HTTP.Enabled), true
	case "transport.http.bind":
		return cfg.Transport.HTTP.Bind, true
	case "transport.socket.enabled":
		return fmt.Sprintf("%v", cfg.Transport.Socket.Enabled), true
	case "lifecycle.idle_timeout":
		return cfg.Lifecycle.IdleTimeout, true
	case "lifecycle.shutdown_timeout":
		return cfg.Lifecycle.ShutdownTimeout, true
	case "privacy.telemetry":
		return fmt.Sprintf("%v", cfg.Privacy.Telemetry), true
	case "privacy.remote_sync":
		return cfg.Privacy.RemoteSync, true
	case "privacy.allow_nonlocal_http":
		return fmt.Sprintf("%v", cfg.Privacy.AllowNonlocalHTTP), true
	case "logging.level":
		return cfg.Logging.Level, true
	case "logging.format":
		return cfg.Logging.Format, true
	case "exec.path_prepend":
		return fmt.Sprintf("%v", cfg.Exec.PathPrepend), true
	default:
		return "", false
	}
}

// configBoolTrue is the literal a config-set value must equal to
// flip a boolean field on. Extracted to a constant per goconst's
// repeat-literal threshold; the value is "true" because that's
// what the CLI documentation tells operators to type.
const configBoolTrue = "true"

// setConfigField parses and sets a dot-delimited key path.
func setConfigField(cfg *config.Config, key, value string) error {
	switch key {
	case "version":
		return fmt.Errorf("version is read-only")
	case "capture.fs.enabled":
		cfg.Capture.FS.Enabled = value == configBoolTrue
	case "capture.fs.debounce_ms":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Capture.FS.DebounceMS = v
	case "capture.fs.ignore":
		if err := json.Unmarshal([]byte(value), &cfg.Capture.FS.Ignore); err != nil {
			return fmt.Errorf("invalid JSON array %q: %w", value, err)
		}
	case "storage.durability":
		cfg.Storage.Durability = value
	case "storage.max_batch_ms":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Storage.MaxBatchMS = v
	case "storage.journal_max_bytes":
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int64 %q: %w", value, err)
		}
		cfg.Storage.JournalMaxBytes = v
	case "storage.compress_rotated":
		cfg.Storage.CompressRotated = value == configBoolTrue
	case "retrieval.default_budget":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", value, err)
		}
		cfg.Retrieval.DefaultBudget = v
	case "retrieval.default_format":
		cfg.Retrieval.DefaultFormat = value
	case "index.bm25_k1":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.BM25K1 = v
	case "index.bm25_b":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.BM25B = v
	case "index.heading_boost":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float64 %q: %w", value, err)
		}
		cfg.Index.HeadingBoost = v
	case "transport.http.enabled":
		cfg.Transport.HTTP.Enabled = value == configBoolTrue
	case "transport.http.bind":
		cfg.Transport.HTTP.Bind = value
	case "transport.socket.enabled":
		cfg.Transport.Socket.Enabled = value == configBoolTrue
	case "lifecycle.idle_timeout":
		cfg.Lifecycle.IdleTimeout = value
	case "lifecycle.shutdown_timeout":
		cfg.Lifecycle.ShutdownTimeout = value
	case "privacy.telemetry":
		cfg.Privacy.Telemetry = value == configBoolTrue
	case "privacy.remote_sync":
		cfg.Privacy.RemoteSync = value
	case "privacy.allow_nonlocal_http":
		cfg.Privacy.AllowNonlocalHTTP = value == configBoolTrue
	case "logging.level":
		cfg.Logging.Level = value
	case "logging.format":
		cfg.Logging.Format = value
	case "exec.path_prepend":
		return fmt.Errorf("exec.path_prepend is a list; use config get to inspect and manual edit to change")
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}
