package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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

// Start starts the daemon with panic recovery.
func (d *Daemon) Start(ctx context.Context) error {
	if d.running.Load() {
		return fmt.Errorf("daemon already running")
	}
	d.running.Store(true)

	// Protect server start with panic recovery
	var startErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				startErr = fmt.Errorf("panic during start: %v", r)
				d.running.Store(false)
			}
		}()
		startErr = d.server.Start(ctx)
	}()

	if startErr != nil {
		return fmt.Errorf("start server: %w", startErr)
	}

	// Write PID file
	pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
	pidData := fmt.Sprintf("%d\n", os.Getpid())
	os.WriteFile(pidPath, []byte(pidData), 0644)

	// Start optional filesystem watcher and pipe its events into the journal.
	if d.fswatcher != nil {
		if err := d.fswatcher.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: fswatcher start: %v\n", err)
		} else {
			go d.consumeFSWatch(ctx)
		}
	}

	// Start idle monitor
	d.startIdleMonitor(ctx)

	// Register in global registry
	d.register()

	fmt.Printf("DFMT daemon started for %s\n", d.projectPath)
	return nil
}

// Stop stops the daemon gracefully.
func (d *Daemon) Stop(ctx context.Context) error {
	if !d.running.CompareAndSwap(true, false) {
		return nil // Already stopped
	}

	// Close shutdown channel only once
	select {
	case <-d.shutdownCh:
		// Already closed
	default:
		close(d.shutdownCh)
	}

	// Stop idle timer and signal callback to return early if it fires concurrently
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}
	if d.idleCh != nil {
		select {
		case <-d.idleCh:
			// Already closed
		default:
			close(d.idleCh)
		}
	}

	// Persist index
	indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
	hiID, err := d.journal.Checkpoint(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: checkpoint failed: %v\n", err)
	}
	core.PersistIndex(d.index, indexPath, hiID)

	// Stop filesystem watcher
	if d.fswatcher != nil {
		_ = d.fswatcher.Stop(ctx)
	}

	// Stop server
	if err := d.server.Stop(ctx); err != nil {
		return fmt.Errorf("stop server: %w", err)
	}

	// Close journal
	if err := d.journal.Close(); err != nil {
		return fmt.Errorf("close journal: %w", err)
	}

	// Remove PID file
	pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
	os.Remove(pidPath)

	// Unregister from global registry
	d.unregister()

	fmt.Println("DFMT daemon stopped")
	return nil
}

// consumeFSWatch drains events from the filesystem watcher into the journal and index.
// It returns when the watcher's Events channel closes or the daemon shuts down.
func (d *Daemon) consumeFSWatch(ctx context.Context) {
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
