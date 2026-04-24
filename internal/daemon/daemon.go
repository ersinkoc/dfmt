package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ersinkoc/dfmt/internal/capture"
	"github.com/ersinkoc/dfmt/internal/client"
	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/project"
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
	fswatcher   *capture.FSWatcher

	running    atomic.Bool // Use atomic for race-free access
	idleTimer  *time.Timer
	idleCh     chan struct{} // closed on idle timeout or stop
	shutdownCh chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup // tracks background goroutines (e.g. fswatch consumer)
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

	// Ensure .dfmt directory exists
	dfmtDir := filepath.Join(projectPath, ".dfmt")
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
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

	// Create or load index
	indexPath := filepath.Join(dfmtDir, "index.gob")
	cursorPath := filepath.Join(dfmtDir, "index.cursor")

	index, _, needsRebuild, err := core.LoadIndexWithCursor(indexPath, cursorPath)
	if err != nil || needsRebuild {
		index = core.NewIndex()
	}

	// Create sandbox
	sb := sandbox.NewSandbox(projectPath)

	// Create handlers
	handlers := transport.NewHandlers(index, journal, sb)
	handlers.SetProject(projectPath)

	// Create server based on platform - use HTTPServer for HTTP support (dashboard, API)
	var server Server
	if runtime.GOOS == "windows" {
		// On Windows, use TCP with HTTPServer for full HTTP support.
		// Bind to the IPv4 loopback explicitly so we don't race between
		// ::1 and 127.0.0.1 — the client also dials 127.0.0.1 to avoid
		// slow IPv6-first fallbacks through "localhost" resolution.
		httpServer := transport.NewHTTPServer("127.0.0.1:0", handlers)
		portFile := filepath.Join(dfmtDir, "port")
		httpServer.SetPortFile(portFile)
		server = httpServer
	} else {
		// On Unix, use Unix socket with HTTPServer for full HTTP support
		socketPath := project.SocketPath(projectPath)
		ln, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("create socket listener: %w", err)
		}
		os.Chmod(socketPath, 0700)
		server = transport.NewHTTPServerWithListener(ln, handlers, socketPath)
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
		projectPath: projectPath,
		config:      cfg,
		index:       index,
		journal:     journal,
		server:      server,
		handlers:    handlers,
		fswatcher:   fswatcher,
		shutdownCh:  make(chan struct{}),
	}

	return d, nil
}

// Start starts the daemon with panic recovery. The whole start sequence is
// wrapped so a panic in any phase (server, fswatcher, registry) is reported
// as an error rather than crashing the daemon process — and any partially-
// opened resources (TCP listener, fswatcher) are torn down before returning.
//
// The running flag is flipped with CompareAndSwap so two concurrent Start
// calls do not both proceed to bind the listener.
func (d *Daemon) Start(ctx context.Context) error {
	if !d.running.CompareAndSwap(false, true) {
		return fmt.Errorf("daemon already running")
	}

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
		pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
		pidData := fmt.Sprintf("%d\n", os.Getpid())
		if err := os.WriteFile(pidPath, []byte(pidData), 0o644); err != nil {
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

		// Start idle monitor
		d.startIdleMonitor(ctx)

		// Register in global registry
		d.register()
	}()

	if startErr != nil {
		// Partial-start cleanup: tear down anything we already brought up so
		// the next Start can rebind the port/socket and no fswatch goroutine
		// is leaked.
		if fswatcherStarted && d.fswatcher != nil {
			_ = d.fswatcher.Stop(ctx)
		}
		if serverStarted {
			_ = d.server.Stop(ctx)
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

		// Stop idle timer and wake its callback so it exits without trying to
		// re-enter Stop.
		if d.idleTimer != nil {
			d.idleTimer.Stop()
		}
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

		// (4) Wait for consumeFSWatch to finish draining before touching the
		// journal. This is the fix for the Stop-ordering race where
		// consumeFSWatch could call journal.Append on a closed journal.
		d.wg.Wait()

		// (5) Persist the index while the journal is still open for Checkpoint.
		indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
		hiID, err := d.journal.Checkpoint(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: checkpoint failed: %v\n", err)
		}
		if perr := core.PersistIndex(d.index, indexPath, hiID); perr != nil {
			fmt.Fprintf(os.Stderr, "warning: persist index: %v\n", perr)
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
		fmt.Println("DFMT daemon stopped")
	})
	return retErr
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
			if err := d.journal.Append(ctx, e); err != nil {
				fmt.Fprintf(os.Stderr, "fswatch journal append: %v\n", err)
				continue
			}
			d.index.Add(e)
		}
	}
}

func (d *Daemon) startIdleMonitor(ctx context.Context) {
	idleTimeout, err := time.ParseDuration(d.config.Lifecycle.IdleTimeout)
	if err != nil {
		idleTimeout = 30 * time.Minute
	}

	d.idleCh = make(chan struct{}, 1)
	d.idleTimer = time.AfterFunc(idleTimeout, func() {
		select {
		case <-d.idleCh:
			return // Stop() was called first
		default:
		}
		// Check if still running before attempting shutdown
		if d.running.Load() {
			fmt.Println("Daemon idle timeout, shutting down...")
			d.Stop(ctx)
		}
	})
}

func (d *Daemon) register() {
	entry := client.NewDaemonEntry(d.projectPath, os.Getpid())
	client.GetRegistry().Register(entry)
}

func (d *Daemon) unregister() {
	client.GetRegistry().Unregister(d.projectPath)
}
