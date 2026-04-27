// Package logging is a minimal level-filtered logger used by every
// component that previously wrote `fmt.Fprintf(os.Stderr, "warning:
// ...")` directly. Centralising the writes lets the operator silence
// chatter via DFMT_LOG=error in CI and surfaces a single sink that
// tests can capture without monkey-patching os.Stderr.
//
// Design constraints:
//
//   - Output format is byte-identical to the pre-migration shape:
//     `<level>: <formatted-message>\n` to stderr. Scripts that parse
//     "warning: ..." lines must keep working unchanged. That rules out
//     log/slog's default text handler (it adds time= and structured
//     attrs that break parsers).
//
//   - Level threshold is set once at process start from the DFMT_LOG
//     env var and never mutated. Concurrent use is safe — the only
//     mutable state is the io.Writer behind a mutex, exposed only for
//     tests via SetOutput.
//
//   - The package depends on stdlib only. Adding a real logging
//     framework was out of scope; stdlib-first is the project policy.
//
// Usage from callers:
//
//	logging.Warnf("patch ~/.claude.json: %v", err)
//
// instead of:
//
//	fmt.Fprintf(os.Stderr, "warning: patch ~/.claude.json: %v\n", err)
//
// The function adds the "warning:" prefix and the trailing newline.
package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Level orders log severity. The zero value (LevelDebug) is the most
// verbose; LevelError is the quietest. Comparison uses < / >= so a
// threshold of LevelWarn admits LevelWarn and LevelError.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	// LevelOff suppresses everything. Set DFMT_LOG=off to silence the
	// daemon's stderr in CI dashboards where warning chatter is noise.
	LevelOff
)

var (
	mu     sync.Mutex
	out    io.Writer = os.Stderr
	minLvl Level     = LevelWarn // matches pre-migration default (everything was a warning)
)

func init() {
	switch strings.ToLower(os.Getenv("DFMT_LOG")) {
	case "debug":
		minLvl = LevelDebug
	case "info":
		minLvl = LevelInfo
	case "warn", "warning":
		minLvl = LevelWarn
	case "error":
		minLvl = LevelError
	case "off", "none", "silent":
		minLvl = LevelOff
	}
}

// SetOutput swaps the destination writer. Used by tests to capture log
// lines into a bytes.Buffer; callers in prod code should not touch
// this. The previous writer is returned so tests can restore it on
// cleanup.
func SetOutput(w io.Writer) (prev io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	prev = out
	out = w
	return prev
}

// SetLevel overrides the threshold. Same caveat as SetOutput: tests
// only. Returns the previous level so cleanup can restore.
func SetLevel(l Level) (prev Level) {
	mu.Lock()
	defer mu.Unlock()
	prev = minLvl
	minLvl = l
	return prev
}

func write(lvl Level, prefix, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if lvl < minLvl {
		return
	}
	// Single Fprintf + the prefix + "\n" preserves the legacy format
	// exactly. Avoid Fprintln(prefix, format, args...) — that would
	// space-separate prefix and the formatted message.
	fmt.Fprintf(out, prefix+": "+format+"\n", args...)
}

// Debugf writes a `debug:` prefixed message at LevelDebug. Suppressed
// unless DFMT_LOG=debug.
func Debugf(format string, args ...any) { write(LevelDebug, "debug", format, args...) }

// Infof writes an `info:` prefixed message at LevelInfo. Suppressed
// unless DFMT_LOG=info or below.
func Infof(format string, args ...any) { write(LevelInfo, "info", format, args...) }

// Warnf writes a `warning:` prefixed message at LevelWarn. Default
// threshold; visible without any env var override.
func Warnf(format string, args ...any) { write(LevelWarn, "warning", format, args...) }

// Errorf writes an `error:` prefixed message at LevelError. Always
// visible unless DFMT_LOG=off.
func Errorf(format string, args ...any) { write(LevelError, "error", format, args...) }
