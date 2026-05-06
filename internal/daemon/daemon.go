package daemon

import (
	"context"
	"errors"
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
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/redact"
	"github.com/ersinkoc/dfmt/internal/safefs"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/transport"
)

const (
	// ephemeralLoopback is the default bind for the daemon's HTTP listener:
	// IPv4 loopback with an ephemeral port (CLI clients read the actual port
	// from .dfmt/port). Held as a constant so the test cross-checks share
	// the literal.
	ephemeralLoopback = "127.0.0.1:0"
	goosWindows       = "windows"
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

	running        atomic.Bool   // Use atomic for race-free access
	lastActivityNs atomic.Int64  // UnixNano of last inbound RPC; drives idle monitor
	idleCh         chan struct{} // closed on idle timeout or stop
	shutdownCh     chan struct{}
	stopOnce       sync.Once
	wg             sync.WaitGroup // tracks background goroutines (e.g. fswatch consumer)

	// extraProjects holds lazily-loaded resource bundles for projects
	// other than the default one (passed to New). Phase 2 multi-project
	// support: a single global daemon serves N projects in-process,
	// each with its own journal/index/content/redact/sandbox state.
	// projectsMu guards both the map and the per-entry init coordination
	// inside Resources(). The default project keeps its handles on the
	// daemon directly (journal, index, redactor, fswatcher) so the
	// existing Start/Stop/idle/register/fswatch lifecycles are unchanged.
	projectsMu    sync.RWMutex
	extraProjects map[string]*ProjectResources

	// globalMode is set by NewGlobal: the daemon listens at host-scoped
	// paths (~/.dfmt/{daemon.sock|port,daemon.pid,lock}) instead of
	// per-project (<proj>/.dfmt/...) and serves every call through the
	// per-project resource cache (no eager default project). Start/Stop
	// branch on this to choose lock + PID locations; the bind itself is
	// already correct because globalMode swaps the server's port-file /
	// socket-path during construction.
	globalMode bool
}

// Touch records inbound activity so the idle monitor resets. Wired into
// Handlers via SetActivityFn so every RPC bumps the timer — the previous
// AfterFunc-only monitor never reset, making "idle timeout" effectively a
// hard uptime cap.
func (d *Daemon) Touch() {
	d.lastActivityNs.Store(time.Now().UnixNano())
}

// defaultShutdownGrace is the fallback if config.Lifecycle.ShutdownTimeout
// is unset or unparseable. Matches the previous hard-coded behavior so an
// operator who never set the YAML field sees no behavior change.
const defaultShutdownGrace = 10 * time.Second

// ShutdownGrace returns the timeout granted to the daemon's Stop sequence
// before background goroutines and the HTTP server are forcibly torn down.
// Reads config.Lifecycle.ShutdownTimeout (ADR-0015 v0.4 wire-up); falls
// back to defaultShutdownGrace if the field is empty or fails to parse —
// the parse error path is surfaced at startup via config.Validate(), so
// reaching this fallback usually means the daemon was constructed with
// a hand-rolled (test) Config that bypassed Validate.
func (d *Daemon) ShutdownGrace() time.Duration {
	if d == nil || d.config == nil {
		return defaultShutdownGrace
	}
	raw := d.config.Lifecycle.ShutdownTimeout
	if raw == "" {
		return defaultShutdownGrace
	}
	g, err := time.ParseDuration(raw)
	if err != nil || g <= 0 {
		return defaultShutdownGrace
	}
	return g
}

// New creates a new daemon instance.
func New(projectPath string, cfg *config.Config) (*Daemon, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}

	// Apply YAML logging level as soon as config is in hand so subsequent
	// logging.Warnf calls in this function honor the operator setting.
	// The DFMT_LOG env var still wins (init() recorded that case);
	// ApplyConfig is a no-op when env was set. ADR-0015.
	logging.ApplyConfig(cfg.Logging.Level)

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
		logging.Warnf("load index: %v", err)
		needsRebuild = true
	}
	// BM25 / heading params from operator config (ADR-0015 v0.4 wire-up).
	// Validate has already gated ranges; fields default to zero if the
	// operator never touched them, in which case NewBM25OkapiWithParams's
	// fallback restores the package defaults.
	indexParams := core.IndexParams{
		K1:           cfg.Index.BM25K1,
		B:            cfg.Index.BM25B,
		HeadingBoost: cfg.Index.HeadingBoost,
	}
	if index == nil || needsRebuild {
		// Tokenizer-version bump or corrupt index: start fresh so we don't
		// mix differently-tokenized postings, and let Start() fill it.
		index = core.NewIndexWithParams(indexParams)
	} else {
		// Loaded an existing index — apply operator params so a config
		// change since the last persist takes effect immediately.
		index.SetParams(indexParams)
	}

	// Create sandbox; cfg.Exec.PathPrepend is the project's escape hatch
	// when the daemon's inherited PATH does not see the user's toolchains
	// (Go, Node, Python). dirs are prepended for every exec call.
	//
	// V-11: surface configuration smells (missing dirs, world-writable
	// entries) so the operator notices a planted-binary risk at start
	// instead of after compromise. We do not refuse the daemon — the
	// PathPrepend semantics are operator-trusted by design.
	if err := sandbox.ValidatePathPrepend(cfg.Exec.PathPrepend); err != nil {
		return nil, fmt.Errorf("invalid path_prepend: %w", err)
	}
	// ADR-0014: load .dfmt/permissions.yaml on top of DefaultPolicy. A
	// missing file is normal (most projects don't override). A parse error
	// surfaces as a warning; the daemon still starts so a typo in the
	// override doesn't lock the operator out of the running system.
	// `dfmt doctor` echoes the same state for verification.
	polRes, polErr := sandbox.LoadPolicyMerged(projectPath)
	if polErr != nil {
		logging.Warnf("permissions: %v", polErr)
	}
	for _, w := range polRes.Warnings {
		logging.Warnf("permissions: %s", w)
	}
	sb := sandbox.NewSandboxWithPolicy(projectPath, polRes.Policy).
		WithPathPrepend(cfg.Exec.PathPrepend)

	// Create handlers
	handlers := transport.NewHandlers(index, journal, sb)
	handlers.SetProject(projectPath)

	// Wire operator-configured Recall fallbacks (ADR-0015 v0.4 punch
	// list). Per-call Budget/Format still wins; these only fill in
	// when the caller omits a value. Zero / empty means "use package
	// default", same as before this commit.
	handlers.SetRecallDefaults(
		cfg.Retrieval.DefaultBudget,
		cfg.Retrieval.DefaultFormat,
	)

	// Wire ephemeral content store so sandbox output can be stashed for recall.
	// Failure here is non-fatal — handlers gracefully degrade to excerpt-only.
	contentDir := filepath.Join(dfmtDir, "content")
	if store, cerr := content.NewStore(content.StoreOptions{Path: contentDir}); cerr == nil {
		handlers.SetContentStore(store)
	} else {
		logging.Warnf("create content store: %v", cerr)
	}

	// Create server based on platform - use HTTPServer for HTTP support (dashboard, API)
	var server Server
	var httpServer *transport.HTTPServer
	// cfg is non-nil here — the New() entry replaces nil with &config.Config{}.
	tcpOptIn := cfg.Transport.HTTP.Enabled && cfg.Transport.HTTP.Bind != ""
	switch {
	case runtime.GOOS == goosWindows:
		// On Windows, use TCP with HTTPServer for full HTTP support.
		// Bind to the IPv4 loopback explicitly so we don't race between
		// ::1 and 127.0.0.1 — the client also dials 127.0.0.1 to avoid
		// slow IPv6-first fallbacks through "localhost" resolution.
		//
		// Default is 127.0.0.1:0 (ephemeral; CLI clients read the actual
		// port from .dfmt/port). When the operator opts into a fixed bind
		// via transport.http.enabled=true + transport.http.bind=…:8765,
		// honor it so the dashboard URL is stable. Port file is still
		// written either way — CLI clients always read it so they don't
		// need to know whether the bind is fixed or ephemeral.
		bind := ephemeralLoopback
		if tcpOptIn {
			bind = cfg.Transport.HTTP.Bind
		}
		httpServer = transport.NewHTTPServer(bind, handlers)
		portFile := filepath.Join(dfmtDir, "port")
		httpServer.SetPortFile(portFile)
		httpServer.SetProjectPath(projectPath)
	case tcpOptIn:
		// Unix opt-in TCP loopback. Required for the dashboard, which is
		// HTTP over a routable address — a browser cannot dial a Unix
		// socket. Loopback validation lives in transport (NewHTTPServer's
		// listener phase), so a public bind would fail there. We refuse
		// to run both a socket AND a TCP listener: the CLI client picks
		// its dial target via the presence of .dfmt/port (TCP) vs the
		// socket file, and exposing both would make that choice ambiguous.
		httpServer = transport.NewHTTPServer(cfg.Transport.HTTP.Bind, handlers)
		portFile := filepath.Join(dfmtDir, "port")
		httpServer.SetPortFile(portFile)
		httpServer.SetProjectPath(projectPath)
	default:
		// Unix path with no TCP opt-in: use the Unix socket — unless the
		// operator explicitly disabled it (ADR-0015 wire-up of
		// transport.socket.enabled). Disabling the socket without
		// enabling TCP leaves the daemon with no listener; refuse to
		// start with a hint pointing at the two viable configurations.
		if !cfg.Transport.Socket.Enabled {
			return nil, fmt.Errorf("transport: no listener configured — transport.socket.enabled is false and transport.http.enabled is also false. " +
				"Set one of them to true (HTTP requires transport.http.bind to also be non-empty)")
		}
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
			logging.Warnf("chmod socket: %v", cerr)
		}
		httpServer = transport.NewHTTPServerWithListener(ln, handlers, socketPath)
		httpServer.SetProjectPath(projectPath)
	}
	// Single assignment site — previously the Unix branch fell through with
	// server still nil, which would panic at d.server.Start(ctx). Tests
	// run on Windows so the nil-server bug went unnoticed in CI.
	server = httpServer

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

	// ADR-0014: load .dfmt/redact.yaml on top of the default pattern set.
	// One redactor instance feeds both Daemon (for direct redactions) and
	// Handlers (for journal-write hygiene); SetRedactor below overrides
	// the default that NewHandlers initialized.
	redactor, redactRes, redactErr := redact.LoadProjectRedactor(projectPath)
	if redactErr != nil {
		logging.Warnf("redact: %v", redactErr)
	}
	for _, w := range redactRes.Warnings {
		logging.Warnf("redact: %s", w)
	}
	handlers.SetRedactor(redactor)

	d := &Daemon{
		projectPath:  projectPath,
		config:       cfg,
		index:        index,
		indexPath:    indexPath,
		needsRebuild: needsRebuild,
		journal:      journal,
		server:       server,
		handlers:     handlers,
		redactor:     redactor,
		fswatcher:    fswatcher,
		shutdownCh:   make(chan struct{}),
	}
	d.lastActivityNs.Store(time.Now().UnixNano())
	handlers.SetActivityFn(d.Touch)

	return d, nil
}

// NewGlobal creates a host-wide daemon that serves every DFMT-initialized
// project from a single process. Unlike New, it does not load a default
// project at construction — every inbound RPC must carry a project_id
// (Phase 2 commit 2 ensures MCP subprocesses and CLI clients always do)
// which routes through Daemon.Resources(pid) to lazily-loaded per-project
// state.
//
// Listener bind, lock, and PID locations come from internal/project's
// global helpers (~/.dfmt/{daemon.sock|port,daemon.pid,lock}). Two
// global daemons cannot coexist on the same host — the second one's
// AcquireGlobalLock fails with LockError.
//
// The constructed daemon's handlers have NO direct journal/index/redactor/
// store/sandbox handles. A ResourceFetcher is installed instead, closing
// over Resources(pid) so each tool call resolves its bundle per-call.
// errProjectIDRequired surfaces as -32603 to MCP clients when a call
// arrives without a project_id (degraded clients, mis-rigged hooks).
func NewGlobal(cfg *config.Config) (*Daemon, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	logging.ApplyConfig(cfg.Logging.Level)

	// Ensure ~/.dfmt/ exists before the listener tries to drop port/pid
	// files into it. project.GlobalDir() already does this side-effect
	// but the caller may have nil-ified it via env override; double
	// MkdirAll is harmless.
	globalDir := project.GlobalDir()
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		return nil, fmt.Errorf("create global dir: %w", err)
	}

	// Handlers without direct project state. SetResourceFetcher below
	// installs the per-call routing closure; resolveBundle reads
	// bundle.Sandbox / bundle.Journal / bundle.Index / bundle.Redactor /
	// bundle.ContentStore from the fetcher's Bundle.
	handlers := transport.NewHandlers(nil, nil, nil)
	handlers.SetRecallDefaults(
		cfg.Retrieval.DefaultBudget,
		cfg.Retrieval.DefaultFormat,
	)

	// Server creation. Same platform/cfg branching as New, but the port
	// file / socket path point at global locations. SetProjectPath stays
	// empty — the dashboard uses the registry's per-project rows, not a
	// daemon-level default.
	var server Server
	var httpServer *transport.HTTPServer
	tcpOptIn := cfg.Transport.HTTP.Enabled && cfg.Transport.HTTP.Bind != ""
	switch {
	case runtime.GOOS == goosWindows:
		bind := ephemeralLoopback
		if tcpOptIn {
			bind = cfg.Transport.HTTP.Bind
		}
		httpServer = transport.NewHTTPServer(bind, handlers)
		httpServer.SetPortFile(project.GlobalPortPath())
	case tcpOptIn:
		httpServer = transport.NewHTTPServer(cfg.Transport.HTTP.Bind, handlers)
		httpServer.SetPortFile(project.GlobalPortPath())
	default:
		if !cfg.Transport.Socket.Enabled {
			return nil, fmt.Errorf("transport: no listener configured — transport.socket.enabled is false and transport.http.enabled is also false. " +
				"Set one of them to true (HTTP requires transport.http.bind to also be non-empty)")
		}
		socketPath := project.GlobalSocketPath()
		ln, err := transport.ListenUnixSocket(socketPath)
		if err != nil {
			return nil, fmt.Errorf("create global socket listener: %w", err)
		}
		if cerr := os.Chmod(socketPath, 0o700); cerr != nil {
			logging.Warnf("chmod global socket: %v", cerr)
		}
		httpServer = transport.NewHTTPServerWithListener(ln, handlers, socketPath)
	}
	server = httpServer

	d := &Daemon{
		config:     cfg,
		server:     server,
		handlers:   handlers,
		shutdownCh: make(chan struct{}),
		globalMode: true,
	}
	d.lastActivityNs.Store(time.Now().UnixNano())
	handlers.SetActivityFn(d.Touch)

	// Install the per-call resource fetcher. Resources(pid) handles
	// cache lookup + lazy load of per-project journal/index/sandbox
	// state. errProjectIDRequired surfaces here on empty pid, which
	// resolveBundle propagates to the caller as a -32603 RPC error.
	handlers.SetResourceFetcher(func(projectID string) (transport.Bundle, error) {
		r, err := d.Resources(projectID)
		if err != nil {
			return transport.Bundle{}, err
		}
		return transport.Bundle{
			Journal:      r.Journal,
			Index:        r.Index,
			Redactor:     r.Redactor,
			ContentStore: r.ContentStore,
			Sandbox:      r.Sandbox,
			ProjectPath:  r.ProjectPath,
		}, nil
	})

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
		return errors.New("daemon already running")
	}

	// V-15: warn the operator if the daemon is starting as root. Every
	// sandbox PolicyCheck then runs at root privilege — the deny-list is
	// the only thing standing between an agent's argv and an arbitrary
	// privileged write. The default project workflow does not need root,
	// so flag this as almost certainly a misconfiguration.
	if euid := geteuid(); euid == 0 {
		logging.Warnf("daemon running as root (euid=0); sandbox runs every command at root privilege. Run as your user account instead.")
	}

	var lock *LockFile
	var lerr error
	if d.globalMode {
		lock, lerr = AcquireGlobalLock()
	} else {
		lock, lerr = AcquireLock(d.projectPath)
	}
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
		var pidBaseDir, pidPath string
		if d.globalMode {
			pidBaseDir = project.GlobalDir()
			pidPath = project.GlobalPIDPath()
		} else {
			absProject, perr := filepath.Abs(d.projectPath)
			if perr != nil {
				absProject = d.projectPath
			}
			pidBaseDir = absProject
			pidPath = filepath.Join(absProject, ".dfmt", "daemon.pid")
		}
		pidData := fmt.Sprintf("%d\n", os.Getpid())
		if err := safefs.WriteFile(pidBaseDir, pidPath, []byte(pidData), 0o600); err != nil {
			logging.Warnf("write pid file: %v", err)
		}

		// Start optional filesystem watcher and pipe its events into the journal.
		if d.fswatcher != nil {
			if err := d.fswatcher.Start(ctx); err != nil {
				logging.Warnf("fswatcher start: %v", err)
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

	// stopOnce.Do runs the body exactly once across all callers. retErr
	// captures the *first* call's outcome; any subsequent Stop() races
	// past the CompareAndSwap (returns nil there) so this Once arm is
	// effectively the unique invocation. Documented because the
	// retErr-set-inside-closure pattern can read like "every call gets a
	// fresh retErr" if you don't notice the Once guard.
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

		// (5) Persist the default-project index while its journal is
		// still open for Checkpoint. Global daemons have no default
		// project (d.journal / d.index are nil); their per-project
		// caches persist via closeExtraProjects below.
		//
		// Skip persist if an async rebuild was in flight and got canceled
		// before completing. In that case d.index holds events only up to
		// some Y, but journal.Checkpoint returns the latest event ID X > Y,
		// and writing a cursor that says "indexed up to X" would lie — next
		// start would skip rebuild and search would silently miss events
		// (Y, X]. Letting the cursor stay where it was lets the next daemon
		// detect needsRebuild and replay correctly.
		if !d.globalMode && d.journal != nil && d.index != nil {
			indexPath := filepath.Join(d.projectPath, ".dfmt", "index.gob")
			skipPersist := d.needsRebuild && !d.rebuildComplete.Load()
			if skipPersist {
				fmt.Println("Skipping index persist — async rebuild was incomplete; next start will replay journal")
			} else {
				hiID, err := d.journal.Checkpoint(ctx)
				if err != nil {
					logging.Warnf("checkpoint failed: %v", err)
				}
				if perr := core.PersistIndex(d.index, indexPath, hiID); perr != nil {
					logging.Warnf("persist index: %v", perr)
				}
			}
		}

		// (6) Stop the server.
		if err := d.server.Stop(ctx); err != nil {
			retErr = fmt.Errorf("stop server: %w", err)
			// Fall through — we still want to close the journal and unregister.
		}

		// (7) Close the default-project journal last. nil in global mode.
		if d.journal != nil {
			if err := d.journal.Close(); err != nil && retErr == nil {
				retErr = fmt.Errorf("close journal: %w", err)
			}
		}

		// (7b) Close any extra-project journals the cache holds. This
		// runs after the default project's journal close so an error in
		// the extras can't mask the primary close error. closeExtraProjects
		// logs per-entry warnings; it never returns an error.
		d.closeExtraProjects()

		// Best-effort housekeeping. Global daemons clean up the host-wide
		// PID file; legacy daemons clean up the per-project one.
		var pidPath string
		if d.globalMode {
			pidPath = project.GlobalPIDPath()
		} else {
			pidPath = filepath.Join(d.projectPath, ".dfmt", "daemon.pid")
		}
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
			logging.Warnf("index rebuild panic recovered: %v", r)
		}
	}()

	hiID, err := core.RebuildIndexFromJournalInto(d.rebuildCtx, d.journal, d.index)
	if err != nil {
		// Cancellation is expected on shutdown; only surface real errors.
		// rebuildComplete stays false so Stop knows the persisted cursor
		// would lie about which events are indexed — see Stop's persist
		// guard below.
		if d.rebuildCtx.Err() == nil {
			logging.Warnf("index rebuild: %v", err)
		}
		return
	}
	if perr := core.PersistIndex(d.index, d.indexPath, hiID); perr != nil {
		logging.Warnf("persist rebuilt index: %v", perr)
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
		defer func() {
			t.Stop()
			if r := recover(); r != nil {
				logging.Errorf("daemon idle monitor panic recovered: %v", r)
			}
		}()
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
				stopCtx, cancel := context.WithTimeout(context.Background(), d.ShutdownGrace())
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
	// Global daemon: skip the per-project registry row entirely. The
	// global daemon's discoverability comes from the well-known path
	// (~/.dfmt/{port|sock} + the global PID file), not from N rows in
	// daemons.json. Registering with d.projectPath="" would still write
	// a row but with an empty key, which the registry's dial code
	// (`portFile := filepath.Join(projectPath, ".dfmt", "port")`)
	// would try to read from `.dfmt/port` in the cwd — not what we want.
	if d.globalMode {
		return
	}
	entry := client.NewDaemonEntry(d.projectPath, os.Getpid())
	client.GetRegistry().Register(entry)
}

func (d *Daemon) unregister() {
	if d.globalMode {
		return
	}
	client.GetRegistry().Unregister(d.projectPath)
}
