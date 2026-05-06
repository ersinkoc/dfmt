package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/ersinkoc/dfmt/internal/cli"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/safefs"
	"github.com/ersinkoc/dfmt/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("dfmt %s\n", version.Current)
		return
	}

	// Handle global flags before dispatch. We strip them out of args[] so
	// subcommand flagsets don't see them. Global-flag state is propagated via
	// exported setter functions rather than a package-level flag.Parse (which
	// is never called in this binary — relying on it is how the --json flag
	// has been broken for all subcommands historically).
	args := os.Args[1:]
	cleaned := args[:0]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--project":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "dfmt: --project requires a value")
				os.Exit(1)
			}
			val := args[i+1]
			if len(val) > 0 && val[0] == '-' {
				fmt.Fprintf(os.Stderr, "dfmt: invalid --project value: %q\n", val)
				os.Exit(1)
			}
			_ = os.Setenv("DFMT_PROJECT", val)
			cli.SetGlobalProject(val)
			i++
		case "--json":
			cli.SetGlobalJSON(true)
		default:
			cleaned = append(cleaned, arg)
		}
	}

	// Phase 2: with one global daemon serving every project the blast
	// radius of an unhandled panic widened — every CLI / agent talking
	// to dfmt would lose its journal at once. The recover handler
	// captures the stack trace + a wall-clock timestamp to
	// ~/.dfmt/last-crash.log so `dfmt doctor` can surface the most
	// recent crash to the operator. The defer is installed AFTER
	// arg-parsing on purpose: arg-parsing errors above already exit
	// with their own message, and pairing them with a recover would
	// trip gocritic's exitAfterDefer.
	code := dispatchWithRecover(cleaned)
	os.Exit(code)
}

// dispatchWithRecover wraps cli.Dispatch with the panic-to-crash-log
// recovery. Split out so the defer scope is exactly the dispatch
// call — earlier os.Exit paths in main() are intentionally outside
// this wrapper.
func dispatchWithRecover(cleaned []string) (code int) {
	defer recoverAndLogCrash()
	return cli.Dispatch(cleaned)
}

// recoverAndLogCrash is the global daemon's last line of defense
// against an unhandled panic. The body runs only when a panic is in
// flight (recover() returns non-nil); on a clean exit the deferred
// call is a no-op.
//
// The crash log holds: ISO-8601 timestamp, dfmt version, panic value,
// and the full goroutine stack trace. Older content is overwritten —
// this is "last crash", not a journal — so operators wanting history
// should archive the file before restarting.
//
// Failures inside the recover (e.g. ~/.dfmt/ unwritable) are
// swallowed via the inner defer/recover; the process still exits 1
// rather than panicking out of the panic handler. Without that
// guard a write failure here would cause double-panic and a
// confusing exit-code-2 stack dump.
func recoverAndLogCrash() {
	r := recover()
	if r == nil {
		return
	}
	defer func() {
		_ = recover()
		// Re-fail visibly even if log write blew up. We avoid os.Exit
		// inside a defer chain that's already unwinding from panic to
		// keep behavior identical to the pre-Phase-2 native panic exit
		// shape: stderr trace + non-zero exit.
		fmt.Fprintf(os.Stderr, "dfmt: panic: %v\n", r)
		os.Exit(1)
	}()

	_ = writeCrashLog(r, debug.Stack())
}

// writeCrashLog renders the crash log body and atomically writes it
// to ~/.dfmt/last-crash.log. Split out from recoverAndLogCrash so
// tests can exercise the formatting + write path without invoking
// the os.Exit-bearing recover wrapper. Returns the underlying write
// error (or nil) so recover-in-recover patterns above can decide
// whether to log a secondary failure.
func writeCrashLog(panicValue any, stack []byte) error {
	body := fmt.Sprintf(
		"timestamp: %s\nversion: %s\npanic: %v\n\n%s\n",
		time.Now().UTC().Format(time.RFC3339Nano),
		version.Current,
		panicValue,
		stack,
	)
	dir := project.GlobalDir()
	return safefs.WriteFileAtomic(dir, project.GlobalCrashPath(), []byte(body), 0o600)
}
