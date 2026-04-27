package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ersinkoc/dfmt/internal/capture"
	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/content"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/safefs"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// Server is the interface for network servers (Unix socket or TCP).
type Server interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Daemon is the main DFMT daemon process.
type Daemon struct {
	projectPath string
	config      *config.Config
	index       *core.Index
	journal     core.Journal
	server      Server
	handlers    *transport.Handlers
	redactor    *redact.Redactor
	fswatcher   *capture.FSWatcher
	lock        *LockFile // exclusive singleton lock; nil only between New and Start

	indexPath       string             // .dfmt/index.gob — needed for async persist after rebuild
	needsRebuild    bool               // set in New(); Start() spawns rebuild goroutine if true
	rebuildCtx      context.Context    // cancel target for the rebuild goroutine
	rebuildStop     context.CancelFunc // stops the rebuild on Stop()
	rebuildComplete atomic.Bool        // true once async rebuild finished without cancellation

	running        atomic.Bool  // Use atomic for race-free access
	lastActivityNs atomic.Int64 // UnixNano of last inbound RPC; drives idle monitor
	idleCh         chan struct{} // closed on idle timeout or stop
	shutdownCh     chan struct{}
	stopOnce       sync.Once
	wg             sync.WaitGroup // tracks background goroutines (e.g. fswatch consumer)
}

// Touch records inbound activity so the idle monitor resets. Wired into
// Handlers via SetActivityFn so every RPC bumps the timer — the previous
// AfterFunc-only monitor never reset, making "idle timeout" effectively a
// hard uptime cap.
func (d *Daemon) Touch() {
	d.lastActivityNs.Store(time.Now().UnixNano())
}

// New creates a new daemon instance.
func New(projectPath string, cfg *config.Config) (*Daemon, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	// Discover project if not found
	if projectPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		p, err := project.Discover(cwd)
		if err != nil {
			return nil, err
		}
		projectPath = p
	}

	// Ensure .dfmt directory exists. 0700 so nobody else on the host can
	// read the indexed events, raw tool output, or redact patterns.
	dfmtDir := filepath.Join(projectPath, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0o700); err != nil {
		return nil, fmt.Errorf("create .dfmt: %w", err)
	}

	// Create journal
	journalPath := filepath.Join(dfmtDir, "journal.jsonl")
	journalOpts := core.JournalOptions{
		Path:     journalPath,
		MaxBytes: cfg.Storage.JournalMaxBytes,
		Durable:  cfg.Storage.Durability == "durable",
		BatchMS:  cfg.Storage.MaxBatchMS,
		Compress: cfg.Storage.CompressRotated,
	}

	journal, err := core.OpenJournal(journalPath, journalOpts)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	// Create or load index. When LoadIndexWithCursor signals needsRebuild
	// (missing/corrupt cursor, tokenizer version bump, or unreadable index),
	// stream the journal into a fresh index so historical events stay
	// searchable. Without this, any rebuild signal silently empties search
	// and recall until new events arrive.
	indexPath := filepath.Join(dfmtDir, "index.gob")
	cursorPath := filepath.Join(dfmtDir, "index.cursor")

	// Load whatever's on disk; rebuild (when required) is deferred to Start()
	// so the listener can come up immediately. The previous synchronous
	// rebuild here blocked daemon.New() for seconds on large journals — long
	// enough that the auto-start retry budget (~3.9s) ran out and the agent's
	// first MCP call failed with "daemon not responding" while the daemon
	// was actually still in the middle of rebuilding.
	index, _, needsRebuild, err := core.LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: load index: %v\n", err)
		needsRebuild = true
	}
	if index == nil || needsRebuild {
		// Tokenizer-version bump or corrupt index: start fresh so we don't
		// mix differently-tokenized postings, and let Start() fill it.
		index = core.NewIndex()
	}

	// Create sandbox
	sb := sandbox.NewSandbox(projectPath)

	// Create handlers
	handlers := transport.NewHandlers(index, journal, sb)
	handlers.SetProject(projectPath)

	// Wire ephemeral content store so sandbox output can be stashed for recall.
	// Failure here is non-fatal — handlers gracefully degrade to excerpt-only.
	contentDir := filepath.Join(dfmtDir, "content")
	if store, cerr := content.NewStore(content.StoreOptions{Path: contentDir}); cerr == nil {
		handlers.SetContentStore(store)
	} else {
		fmt.Fprintf(os.Stderr, "warning: create content store: %v\n", cerr)
	}

	// Create server based on platform - use HTTPServer for HTTP support (dashboard, API)
	var server Server
	var httpServer *transport.HTTPServer
	if runtime.GOOS == "windows" {
		// On Windows, use TCP with HTTPServer for full HTTP support.
		// Bind to the IPv4 loopback explicitly so we don't race between
		// ::1 and 127.0.0.1 — the client also dials 127.0.0.1 to avoid
		// slow IPv6-first fallbacks through "localhost" resolution.
		httpServer = transport.NewHTTPServer("127.0.0.1:0", handlers)
		portFile := filepath.Join(dfmtDir, "port")
		httpServer.SetPortFile(portFile)
		httpServer.SetProjectPath(projectPath)
		server = httpServer
	} else {
		// On Unix, use Unix socket with HTTPServer for full HTTP support.
		// transport.ListenUnixSocket applies a 0o077 umask for the duration
		// of bind(2) so the socket file is never world-readable in the
		// window before chmod (closes F-05). Surface chmod errors to the
		// operator — silently allowing 0666 perms on the socket would let
		// any local user dial the daemon.
		socketPath := project.SocketPath(projectPath)
		ln, err := transport.ListenUnixSocket(socketPath)
		if err != nil {
			return nil, fmt.Errorf("create socket listener: %w", err)
		}
		if cerr := os.Chmod(socketPath, 0o700); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: chmod socket: %v\n", cerr)
		}
		httpServer = transport.NewHTTPServerWithListener(ln, handlers, socketPath)
		httpServer.SetProjectPath(projectPath)
	}

	// Optionally construct the filesystem watcher. Start() wires its event channel into the journal.
	var fswatcher *capture.FSWatcher
	if cfg.Capture.FS.Enabled {
		w, werr := capture.NewFSWatcher(projectPath, cfg.Capture.FS.Ignore, cfg.Capture.FS.DebounceMS)
		if werr != nil {
			return nil, fmt.Errorf("create fswatcher: %w", werr)
		}
		w.SetProject(projectPath)
		fswatcher = w
	}

	d := &Daemon{
		projectPath:  projectPath,
		config:       cfg,
		index:        index,
		indexPath:    indexPath,
		needsRebuild: needsRebuild,
		journal:      journal,
		server:       server,
		handlers:     handlers,
		redactor:     redact.NewRedactor(),
		fswatcher:    fswatcher,
		shutdownCh:   make(chan struct{}),
	}
	d.lastActivityNs.Store(time.Now().UnixNano())
	handlers.SetActivityFn(d.Touch)

	return d, nil
}

// Start starts the daemon with panic recovery. The whole start sequence is
// wrapped so a panic in any phase (server, fswatcher, registry) is reported
// as an error rather than crashing the daemon process — and any partially-
// opened resources (TCP listener, fswatcher) are torn down before returning.
//
// The running flag is flipped with CompareAndSwap so two concurrent Start
// calls do not both proceed to bind the listener.
//
// Before binding anything, we acquire the project-level singleton lock. On
// Windows the previous behavior was that two daemons could happily coexist
// (each bound 127.0.0.1:0 to a different ephemeral port and the port file
// got overwritten by whichever wrote last) — exactly the "bir sürü daemon"
// failure mode. AcquireLock used to be dead code; calling it here makes it
// real.
func (d *Daemon) Start(ctx context.Context) error {
	if !d.running.CompareAndSwap(false, true) {
		return fmt.Errorf("daemon already running")
	}

	lock, lerr := AcquireLock(d.projectPath)
	if lerr != nil {
		// New() opened the journal; lock contention means this Daemon will
		// never reach Stop's normal cleanup path (Stop short-circuits when
		// running=false). Close the journal explicitly so Windows doesn't
		// hold the file open and refuse the test's TempDir RemoveAll. Also
		// covers the production case where a CLI invocation fails Start
		// against an already-running daemon and must not leak handles.
		if d.journal != nil {
			_ = d.journal.Close()
		}
		d.running.Store(false)
		return fmt.Errorf("acquire singleton lock: %w", lerr)
	}
	d.lock = lock

	serverStarted := false
	fswatcherStarted := false

	var startErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				startErr = fmt.Errorf("panic during start: %v", r)
			}
		}()

		if err := d.server.Start(ctx); err != nil {
			startErr = fmt.Errorf("start server: %w", err)
			return
		}
		serverStarted = true

		// Write PID file; a write failure is non-fatal (status/list commands
		// degrade gracefully) but must surface to the operator via stderr.
		// safefs.WriteFile refuses if .dfmt/ or daemon.pid is a symlink — closes
		// F-08 (attacker plants daemon.pid -> /etc/cron.d/x before daemon
		// start, host PID gets injected into the symlink target).
		absProject, perr := filepath.Abs(d.projectPath)
		if perr != nil {
			absProject = d.projectPath
		}
		pidPath := filepath.Join(absProject, ".dfmt", "daemon.pid")
		pidData := fmt.Sprintf("%d\n", os.Getpid())
		if err := safefs.WriteFile(absProject, pidPath, []byte(pidData), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: write pid file: %v\n", err)
		}

		// Start optional filesystem watcher and pipe its events into the journal.
		if d.fswatcher != nil {
			if err := d.fswatcher.Start(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "warning: fswatcher start: %v\n", err)
			} else {
				fswatcherStarted = true
				d.wg.Add(1)
				go d.consumeFSWatch(ctx)
			}
		}

		// Spawn the deferred index rebuild AFTER the listener is up, so the
		// agent's first MCP call doesn't time out behind a multi-second
		// journal replay. Search/recall during rebuild operate on a partial
		// index (Index has internal RWMutex) — degraded but responsive,
		// strictly better than "daemon not responding".
		if d.needsRebuild {
			d.rebuildCtx, d.rebuildStop = context.WithCancel(context.Background())
			d.wg.Add(1)
			go d.rebuildIndexAsync()
		}

		// Start idle monitor
		d.startIdleMonitor(ctx)

		// Register in global registry
		d.register()
	}()

	if startErr != nil {
		// Partial-start cleanup: tear down anything we already brought up so
		// the next Start can rebind the port/socket and no fswatch / rebuild
		// goroutine is leaked.
		if fswatcherStarted && d.fswatcher != nil {
			_ = d.fswatcher.Stop(ctx)
		}
		// Cancel the rebuild goroutine if it was already spawned (panic in
		// startIdleMonitor or register would land here with rebuildStop set).
		if d.rebuildStop != nil {
			d.rebuildStop()
		}
		// Wait for any goroutine we Add()ed to drain before returning, so a
		// failed Start doesn't leak background workers.
		d.wg.Wait()
		if serverStarted {
			_ = d.server.Stop(ctx)
		}
		if d.lock != nil {
			_ = d.lock.Release()
			d.lock = nil
		}
		d.running.Store(false)
		return startErr
	}

	fmt.Printf("DFMT daemon started for %s\n", d.projectPath)
	return nil
}

// Stop stops the daemon gracefully. Safe to call multiple times or from both
// the idle callback and an external trigger — only the first caller performs
// the real shutdown.
//
// Ordering matters: we must stop accepting new events before persisting or
// closing the journal, otherwise an fswatch event in flight could race with
// journal.Close() and land on a closed file.
//
//  1. flip running→false so new requests/idle callback return early;
//  2. close shutdownCh to tell consumeFSWatch to return;
//  3. stop the fswatcher so no more events are produced;
//  4. wait for consumeFSWatch to drain;
//  5. persist the index (needs journal.Checkpoint which still works);
//  6. stop the server;
//  7. close the journal.
func (d *Daemon) Stop(ctx context.Context) error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}

	var retErr error
	d.stopOnce.Do(func() {
		// (1/2) Signal shutdown to background goroutines.
		select {
		case <-d.shutdownCh:
		default:
			close(d.shutdownCh)
		}

		// Wake the idle monitor so it returns without trying to re-enter Stop.
		if d.idleCh != nil {
			select {
			case <-d.idleCh:
			default:
				close(d.idleCh)
			}
		}

		// (3) Stop fswatcher so it stops producing events.
		if d.fswatcher != nil {
			_ = d.fswatcher.Stop(ctx)
		}

		// Cancel any in-flight async rebuild. Without this, Stop would block
		// on d.wg.Wait until the rebuild finished naturally — potentially
		// many seconds on a large journal — even though the user wants to
		// shut down NOW.
		if d.rebuildStop != nil {
			d.rebuildStop()
		}

		// (4) Wait for consumeFSWatch and the async rebuild to finish before
		// touching the journal. This is the fix for the Stop-ordering race
		// where consumeFSWatch (or rebuild) could call journal.Append /
		// journal.Stream on a closed journal.
		d.wg.Wait()

		// (5) Persist the index while the journal is still open for Checkpoint.
		//
		// Skip persist if an async rebuild was in flight and got cancelled
		// before completing. In that case d.index holds events only up to
		// some Y, but journal.Checkpoint returns the latest event ID X > Y,
		// and writing a cursor that says "indexed up to X" would lie — next
		// start would skip rebuild and search would silently miss events
		// (Y, X]. Letting the cursor stay where it was lets the next daemon
		// detect needsRebuild and replay correctly.
		indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
		skipPersist := d.needsRebuild && !d.rebuildComplete.Load()
		if skipPersist {
			fmt.Println("Skipping index persist — async rebuild was incomplete; next start will replay journal")
		} else {
			hiID, err := d.journal.Checkpoint(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: checkpoint failed: %v\n", err)
			}
			if perr := core.PersistIndex(d.index, indexPath, hiID); perr != nil {
				fmt.Fprintf(os.Stderr, "warning: persist index: %v\n", perr)
			}
		}

		// (6) Stop the server.
		if err := d.server.Stop(ctx); err != nil {
			retErr = fmt.Errorf("stop server: %w", err)
			// Fall through — we still want to close the journal and unregister.
		}

		// (7) Close the journal last.
		if err := d.journal.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close journal: %w", err)
		}

		// Best-effort housekeeping.
		pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
		_ = os.Remove(pidPath)
		d.unregister()
		// Release the singleton lock LAST so a fresh daemon spawned the
		// instant Stop returns can bind cleanly. Releasing too early would
		// let a racing daemon acquire the lock before the listener is fully
		// torn down → port-already-in-use on the new daemon's bind.
		if d.lock != nil {
			_ = d.lock.Release()
			d.lock = nil
		}
		fmt.Println("DFMT daemon stopped")
	})
	return retErr
}

// rebuildIndexAsync replays the journal into d.index in the background.
// Tracked in d.wg so Stop can wait for it before persisting / closing the
// journal. Cancellation via d.rebuildCtx lets Stop interrupt a slow rebuild
// without leaking a goroutine. Panic recovery here matches the synchronous
// path in New() — a corrupt event must not crash the daemon.
func (d *Daemon) rebuildIndexAsync() {
	defer d.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "warning: index rebuild panic recovered: %v\n", r)
		}
	}()

	hiID, err := core.RebuildIndexFromJournalInto(d.rebuildCtx, d.journal, d.index)
	if err != nil {
		// Cancellation is expected on shutdown; only surface real errors.
		// rebuildComplete stays false so Stop knows the persisted cursor
		// would lie about which events are indexed — see Stop's persist
		// guard below.
		if d.rebuildCtx.Err() == nil {
			fmt.Fprintf(os.Stderr, "warning: index rebuild: %v\n", err)
		}
		return
	}
	if perr := core.PersistIndex(d.index, d.indexPath, hiID); perr != nil {
		fmt.Fprintf(os.Stderr, "warning: persist rebuilt index: %v\n", perr)
	}
	d.rebuildComplete.Store(true)
}

// consumeFSWatch drains events from the filesystem watcher into the journal and index.
// It returns when the watcher's Events channel closes or the daemon shuts down.
// Signals d.wg so Stop() can wait for the drain to complete before closing the journal.
func (d *Daemon) consumeFSWatch(ctx context.Context) {
	defer d.wg.Done()
	events := d.fswatcher.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			// Re-check shutdown inside the case — Stop() may have closed
			// shutdownCh just before this event was selected. We want to
			// drop in-flight events rather than append to a journal Stop()
			// is about to close.
			select {
			case <-d.shutdownCh:
				return
			default:
			}
			// Redact fswatch event before journaling. File paths routinely
			// contain secrets (~/work/.env.production, customer-token-abc/)
			// and this event flows into the index and recall output.
			if d.redactor != nil {
				e.Data = d.redactor.RedactEvent(e.Data)
				for i, tag := range e.Tags {
					e.Tags[i] = d.redactor.Redact(tag)
				}
				e.Sig = e.ComputeSig()
			}
			if err := d.journal.Append(ctx, e); err != nil {
				fmt.Fprintf(os.Stderr, "fswatch journal append: %v\n", err)
				continue
			}
			d.index.Add(e)
		}
	}
}

// startIdleMonitor spawns a goroutine that periodically checks whether
// lastActivityNs is older than idleTimeout and fires Stop if so. Previous
// implementation used a one-shot time.AfterFunc and never reset it, so the
// configured timeout behaved as a hard uptime cap; Touch() now resets the
// activity clock on every inbound RPC and the monitor re-checks on each tick.
func (d *Daemon) startIdleMonitor(_ context.Context) {
	idleTimeout, err := time.ParseDuration(d.config.Lifecycle.IdleTimeout)
	if err != nil || idleTimeout <= 0 {
		idleTimeout = 30 * time.Minute
	}

	d.idleCh = make(chan struct{}, 1)
	// Check at idleTimeout/10 but never more often than every second or less
	// often than every minute — keeps short timeouts responsive in tests
	// without burning CPU on long production timeouts.
	tick := idleTimeout / 10
	if tick < time.Second {
		tick = time.Second
	}
	if tick > time.Minute {
		tick = time.Minute
	}

	// Deliberately NOT tracked in d.wg: the monitor may call Stop() itself,
	// and Stop calls d.wg.Wait() — adding this goroutine to wg would deadlock.
	// Stop closes idleCh instead, which the goroutine observes on its next tick.
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-d.idleCh:
				return
			case <-d.shutdownCh:
				return
			case <-t.C:
				if !d.running.Load() {
					return
				}
				last := d.lastActivityNs.Load()
				if time.Since(time.Unix(0, last)) < idleTimeout {
					continue
				}
				fmt.Println("Daemon idle timeout, shutting down...")
				stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				// Stop() closes idleCh and sets running→false, so the next
				// loop iteration (or a concurrent Stop caller) exits cleanly.
				_ = d.Stop(stopCtx)
				cancel()
				return
			}
		}
	}()
}

func (d *Daemon) register() {
	entry := client.NewDaemonEntry(d.projectPath, os.Getpid())
	client.GetRegistry().Register(entry)
}

func (d *Daemon) unregister() {
	client.GetRegistry().Unregister(d.projectPath)
}
