package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/project"
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
	config     *config.Config
	index      *core.Index
	journal    core.Journal
	server     Server
	handlers   *transport.Handlers

	mu         sync.Mutex
	running    bool
	idleTimer  *time.Timer
	shutdownCh chan struct{}
}

// New creates a new daemon instance.
func New(projectPath string, cfg *config.Config) (*Daemon, error) {
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
		Path:       journalPath,
		MaxBytes:   cfg.Storage.JournalMaxBytes,
		Durable:    cfg.Storage.Durability == "durable",
		BatchMS:    cfg.Storage.MaxBatchMS,
		Compress:   cfg.Storage.CompressRotated,
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

	// Create handlers
	handlers := transport.NewHandlers(index, journal)

	// Create server based on platform - use HTTPServer for HTTP support (dashboard, API)
	var server Server
	if runtime.GOOS == "windows" {
		// On Windows, use TCP with HTTPServer for full HTTP support
		httpServer := transport.NewHTTPServer("localhost:0", handlers)
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

	d := &Daemon{
		projectPath: projectPath,
		config:     cfg,
		index:      index,
		journal:    journal,
		server:     server,
		handlers:   handlers,
		shutdownCh: make(chan struct{}),
	}

	return d, nil
}

// Start starts the daemon.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon already running")
	}
	d.running = true
	d.mu.Unlock()

	// Start server
	if err := d.server.Start(ctx); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	// Write PID file
	pidPath := filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
	pidData := fmt.Sprintf("%d\n", os.Getpid())
	os.WriteFile(pidPath, []byte(pidData), 0644)

	// Start idle monitor
	d.startIdleMonitor(ctx)

	// Register in global registry
	d.register()

	fmt.Printf("DFMT daemon started for %s\n", d.projectPath)
	return nil
}

// Stop stops the daemon gracefully.
func (d *Daemon) Stop(ctx context.Context) error {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = false
	d.mu.Unlock()

	// Close shutdown channel only once
	select {
	case <-d.shutdownCh:
		// Already closed
	default:
		close(d.shutdownCh)
	}

	// Stop idle timer if running
	if d.idleTimer != nil {
		d.idleTimer.Stop()
	}

	// Persist index
	indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
	hiID, _ := d.journal.Checkpoint(ctx)
	core.PersistIndex(d.index, indexPath, hiID)

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

func (d *Daemon) startIdleMonitor(ctx context.Context) {
	idleTimeout, err := time.ParseDuration(d.config.Lifecycle.IdleTimeout)
	if err != nil {
		idleTimeout = 30 * time.Minute
	}

	d.idleTimer = time.AfterFunc(idleTimeout, func() {
		select {
		case <-d.shutdownCh:
			return
		default:
			fmt.Println("Daemon idle timeout, shutting down...")
			d.Stop(ctx)
		}
	})
}

func (d *Daemon) register() {
	// Register project in global registry
	// This would use the registry to track running daemons
}

func (d *Daemon) unregister() {
	// Remove from global registry
}
