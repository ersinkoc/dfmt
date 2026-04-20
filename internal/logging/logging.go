package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Logger is the global logger instance.
var Logger *slog.Logger

// Config configures the logger.
type Config struct {
	Level  string // "debug", "info", "warn", "error"
	Format string // "text", "json"
	Output string // path to log file, or "stdout" or "stderr"
}

// Init initializes the global logger.
func Init(cfg Config) error {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}

	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	if cfg.Output != "" && cfg.Output != "stdout" && cfg.Output != "stderr" {
		// Ensure directory exists
		dir := filepath.Dir(cfg.Output)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}

		switch cfg.Format {
		case "json":
			handler = slog.NewJSONHandler(f, opts)
		default:
			handler = slog.NewTextHandler(f, opts)
		}
	}

	Logger = slog.New(handler)
	slog.SetDefault(Logger)
	return nil
}

// InitDefault initializes with default config (text, info level, stdout).
func InitDefault() {
	Init(Config{Level: "info", Format: "text", Output: "stdout"})
}

// With returns a logger with additional context.
func With(args ...any) *slog.Logger {
	if Logger == nil {
		InitDefault()
	}
	return Logger.With(args...)
}

// FromContext returns a logger from context, or the default logger.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(keyLogger{}).(*slog.Logger); ok {
		return logger
	}
	if Logger == nil {
		InitDefault()
	}
	return Logger
}

// NewContext returns a context with a logger attached.
func NewContext(parent context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(parent, keyLogger{}, logger)
}

type keyLogger struct{}

// MultiWriter is a writer that writes to multiple destinations.
type MultiWriter struct {
	writers []io.Writer
	mu      sync.Mutex
}

// NewMultiWriter creates a multi-writer.
func NewMultiWriter(writers ...io.Writer) *MultiWriter {
	return &MultiWriter{writers: writers}
}

// Write writes to all destinations.
func (m *MultiWriter) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.writers {
		w.Write(p)
	}
	return len(p), nil
}

// Close closes all destinations that implement io.Closer.
func (m *MultiWriter) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.writers {
		if closer, ok := w.(io.Closer); ok {
			closer.Close()
		}
	}
	return nil
}
