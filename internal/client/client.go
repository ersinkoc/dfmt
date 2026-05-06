package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// Client is a DFMT daemon client.
type Client struct {
	socketPath string // For debugging/testing only
	network    string
	address    string
	timeout    time.Duration

	// authToken is the bearer token for HTTP endpoint authentication.
	authToken string

	// sessionID is sent on every HTTP request as the X-DFMT-Session header
	// so the daemon's wire-dedup cache (ADR-0011) keys per-CLI-session.
	// Multiple sub-calls within one CLI invocation share this ID and
	// benefit from "(unchanged; same content_id)" short-circuiting on
	// repeated reads. Two distinct CLI invocations (different processes)
	// get independent IDs unless the user exports DFMT_SESSION explicitly
	// — useful for shell loops that want a stable bucket across commands.
	sessionID string

	// projectID is the canonical project root path this client targets.
	// Stamped on every RPC's params struct (Phase 2) so the global daemon
	// can route to the correct per-project resources. Empty when the CLI
	// is invoked from outside any initialized project — in that case the
	// daemon falls back to its default project (legacy single-project
	// daemon) or returns errNoProject.
	projectID string

	// globalMode is true when this client is dialing the host-wide
	// global daemon at ~/.dfmt/{port|sock}. Set by NewClient when a
	// running global daemon is detected, or when auto-spawn fell back
	// to spawning one. Influences the auto-spawn retry loop so we
	// re-read the right port file on Windows.
	globalMode bool
}

// resolveSessionID returns the session ID to use for outbound HTTP calls.
// Order: DFMT_SESSION env var if set (lets shells share a bucket across
// commands by exporting once), otherwise a fresh ULID minted at client
// construction time. The ULID is unique across CLI invocations even when
// two of them race against the same daemon.
func resolveSessionID() string {
	if v := os.Getenv("DFMT_SESSION"); v != "" {
		return v
	}
	return string(core.NewULID(time.Now()))
}

// readPortFile parses the port file written by HTTPServer.writePortFile.
// Supports the current JSON form {"port":N,"token":"..."} and falls back to the
// legacy bare-integer format from older daemons for compatibility.
func readPortFile(path string) (int, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return 0, "", errors.New("empty port file")
	}
	if trimmed[0] == '{' {
		var pf transport.PortFile
		if err := json.Unmarshal(trimmed, &pf); err != nil {
			return 0, "", fmt.Errorf("parse port file: %w", err)
		}
		return pf.Port, pf.Token, nil
	}
	port, err := strconv.Atoi(string(trimmed))
	if err != nil {
		return 0, "", fmt.Errorf("parse port file: %w", err)
	}
	return port, "", nil
}

const (
	goosWindows    = "windows"
	netUnix        = "unix"
	addrLocalhost0 = "127.0.0.1:0"
)

// maxRPCResponseBytes caps the daemon's JSON-RPC response body so a buggy or
// runaway daemon (looping streamer, recall snapshot misconfigured to return
// MBs of journal) cannot OOM the client. The peer is the same-user daemon —
// trust boundary is loose — but defense-in-depth here is cheap and matches
// the +1-trick pattern used by config/setup loaders. 16 MiB is well above
// any legitimate response (Stats summary, Recall snapshot capped at the
// caller's Budget) and well below sizes that would actually crash the CLI.
const maxRPCResponseBytes = 16 << 20

// NewClient creates a new client for the given project.
// If the project is not initialized, it auto-initializes.
// If the daemon is not running, it automatically starts it.
//
// Phase 2 connection order:
//
//  1. Try the host-wide global daemon at ~/.dfmt/{port|daemon.sock}.
//     A running global daemon serves every project from one process,
//     so dialing it is preferred over starting a new per-project one.
//  2. Fall back to the legacy per-project port/socket if the global
//     daemon is not responsive. Older operators with already-running
//     per-project daemons keep working.
//  3. If neither is responsive, auto-spawn a global daemon
//     (`dfmt daemon --global`). The next call lands in branch 1.
func NewClient(projectPath string) (*Client, error) {
	// Auto-init project if not initialized. Delegates to setup so the
	// `dfmt init` and lazy-client init paths converge on the same merge-
	// safe writer for .claude/settings.json. The legacy inline implementation
	// here clobbered the file with a hardcoded template, dropping any user-
	// owned plugins/env/statusLine entries on first RPC for a fresh project.
	if err := setup.EnsureProjectInitialized(projectPath); err != nil {
		return nil, fmt.Errorf("auto-init project: %w", err)
	}

	// Resolve absolute project path so Client.projectID is canonical and
	// matches what the daemon stores. filepath.Abs falls back gracefully
	// on error — the projectID stays as-passed and the daemon-side
	// resolver still finds it via samePath comparison.
	resolvedProj := projectPath
	if abs, aerr := filepath.Abs(projectPath); aerr == nil {
		resolvedProj = abs
	}

	// Step 1: probe the global daemon. fastDialOK does a sub-second dial
	// that succeeds only when something is actually listening, so we
	// don't accidentally resolve to stale port/lock files left over from
	// a crashed daemon (the cleanup path in DaemonRunning handles those).
	globalAddress, globalToken, globalNetwork, globalSocket := globalDaemonTarget()
	if globalAddress != "" && fastDialOK(globalNetwork, globalAddress) {
		c := &Client{
			socketPath: globalSocket,
			network:    globalNetwork,
			address:    globalAddress,
			authToken:  globalToken,
			timeout:    5 * time.Second,
			sessionID:  resolveSessionID(),
			projectID:  resolvedProj,
			globalMode: true,
		}
		return c, nil
	}

	// Step 2: legacy per-project address. Same logic as pre-Phase-2 —
	// reads <project>/.dfmt/port (Windows) or builds the per-project
	// socket path (Unix).
	socketPath := project.SocketPath(projectPath)
	portFile := filepath.Join(projectPath, ".dfmt", "port")

	var network, address string

	if runtime.GOOS == goosWindows {
		// On Windows, use TCP with port from port file
		network = "tcp"
		address = addrLocalhost0 // Will be overridden by port file

		if port, _, err := readPortFile(portFile); err == nil && port > 0 {
			address = fmt.Sprintf("127.0.0.1:%d", port)
		}
	} else {
		// On Unix, use Unix socket
		network = netUnix
		address = socketPath
	}

	c := &Client{
		socketPath: socketPath, // For debugging
		network:    network,
		address:    address,
		timeout:    5 * time.Second,
		sessionID:  resolveSessionID(),
		projectID:  resolvedProj,
	}

	// Populate auth token after Client creation
	if runtime.GOOS == goosWindows {
		if _, token, err := readPortFile(portFile); err == nil {
			c.authToken = token
		}
	}

	// Step 2b: legacy per-project daemon already running? Use it.
	if fastDialOK(c.network, c.address) {
		return c, nil
	}

	// Step 2c: at this point the legacy address (loaded above) didn't
	// dial cleanly. The port file on disk pointed at a daemon that
	// no longer exists — most often a v0.3.x daemon that crashed or
	// was killed without removing its scaffolding. Reap it so the
	// next CLI run doesn't keep tripping over the stale port.
	// Skipped under DFMT_DISABLE_AUTOSTART so tests that seed a
	// legacy port file to assert client behavior don't see it
	// vanish out from under them.
	if os.Getenv("DFMT_DISABLE_AUTOSTART") == "" &&
		c.address != "" && c.address != addrLocalhost0 {
		_ = os.Remove(portFile)
	}

	// Step 3: nothing's running. Auto-spawn a global daemon and switch
	// the client over to it. ensureDaemon handles the wait + retry +
	// post-spawn port/token re-read. We flip globalMode and re-point
	// network / address at the global endpoint so the retry loop's
	// first dial doesn't burn a tick on the now-stale legacy address.
	// Under DFMT_DISABLE_AUTOSTART (tests) the spawn is short-circuited
	// — we keep the legacy values so test assertions about socketPath
	// keep matching the per-project shape they were written against.
	if os.Getenv("DFMT_DISABLE_AUTOSTART") == "" {
		c.globalMode = true
		c.network = globalNetwork
		c.address = globalAddress // may be empty — ensureDaemon refreshes it
		c.socketPath = globalSocket
		c.authToken = globalToken
	}
	if err := c.ensureDaemon(projectPath); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	return c, nil
}

// globalDaemonTarget returns the network + address + token + socket
// fields a Client should use to dial the host-wide daemon. The address
// can be empty when no port file or socket file exists yet — the
// caller decides whether to spawn a global daemon or fall back to
// legacy. The token is empty on Unix (Unix socket has no bearer token).
func globalDaemonTarget() (address, token, network, socketPath string) {
	if runtime.GOOS == goosWindows {
		network = "tcp"
		socketPath = ""
		port, t, err := readPortFile(project.GlobalPortPath())
		if err == nil && port > 0 {
			return fmt.Sprintf("127.0.0.1:%d", port), t, network, socketPath
		}
		// No port file → no running global daemon. Address empty.
		return "", "", network, socketPath
	}
	socketPath = project.GlobalSocketPath()
	return socketPath, "", netUnix, socketPath
}

// fastDialOK does a 500-ms dial against the address. Used as the
// liveness probe for global vs legacy daemon selection — a successful
// dial means something is actually listening, regardless of port/lock
// file state.
func fastDialOK(network, address string) bool {
	if address == "" {
		return false
	}
	if network == netUnix {
		if _, err := os.Stat(address); err != nil {
			return false
		}
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.Dial(network, address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ensureDaemon ensures the daemon is running, starting it if needed.
func (c *Client) ensureDaemon(projectPath string) error {
	// Opt-out for tests and embedded callers that manage their own daemon
	// lifecycle. Without this, auto-start re-execs os.Args[0] which under
	// `go test` is the test binary itself — resulting in a fork bomb.
	if os.Getenv("DFMT_DISABLE_AUTOSTART") != "" || isTestBinary() {
		return nil
	}

	// Pick the port file we'll re-read after the daemon comes up. In
	// global mode the daemon writes ~/.dfmt/port, not the per-project
	// one, so post-spawn we must look at the global path or we'll keep
	// dialing addrLocalhost0 forever.
	portFile := filepath.Join(projectPath, ".dfmt", "port")
	if c.globalMode {
		portFile = project.GlobalPortPath()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to connect
	if _, err := c.Connect(ctx); err == nil {
		return nil // Already running
	}

	// Daemon not running - try to start it
	// First, cleanup any stale daemon state
	cleanupStaleDaemon(projectPath)

	if startErr := startDaemon(projectPath, c.globalMode); startErr != nil {
		return fmt.Errorf("failed to start daemon: %w (try: dfmt daemon --foreground to see errors)", startErr)
	}

	// Wait for daemon to come up. Exponential backoff with cap keeps CI fast
	// while still tolerating slow startups. Total budget ≈ 3.9s.
	delays := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1200 * time.Millisecond,
		1200 * time.Millisecond,
	}
	for i, delay := range delays {
		time.Sleep(delay)

		// Update address and auth token from the appropriate port file.
		// On Unix the socket path is fixed at construction time, so the
		// only thing to refresh is auth token (which Unix sockets don't
		// use anyway), so this branch is a no-op there.
		if runtime.GOOS == goosWindows {
			if port, token, err := readPortFile(portFile); err == nil && port > 0 {
				c.address = fmt.Sprintf("127.0.0.1:%d", port)
				c.authToken = token
			}
		} else if c.globalMode {
			// Global mode on Unix: we may have raced the daemon writing
			// its socket file; if the address is still empty (no socket
			// existed at NewClient time) point at the global socket now.
			if c.address == "" {
				c.address = project.GlobalSocketPath()
				c.socketPath = c.address
			}
		}

		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := c.Connect(ctx2)
		cancel2()
		if err == nil {
			return nil
		}

		if i == len(delays)-1 {
			return fmt.Errorf("daemon started but not responding (try: dfmt daemon --foreground to debug): %w", err)
		}
	}
	return errors.New("daemon failed to start")
}

// isTestBinary reports whether the current process is a Go test binary.
// This is the key safety check: if we re-exec os.Args[0] with "daemon"
// from a test binary, the test framework ignores the extra arg and re-runs
// the entire test suite, which then spawns more children — a fork bomb.
func isTestBinary() bool {
	if flag.Lookup("test.v") != nil {
		return true
	}
	base := strings.ToLower(filepath.Base(os.Args[0]))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

// startDaemon starts the dfmt daemon. When globalMode is true the
// child is spawned as `dfmt daemon --global` so it owns the host-wide
// listener; otherwise it runs in legacy per-project mode for backward
// compatibility with operators who manually pinned a project.
func startDaemon(projectPath string, globalMode bool) error {
	// Belt-and-braces: refuse to re-exec a test binary even if a caller
	// reaches this function directly. See isTestBinary for the rationale.
	if isTestBinary() {
		return errors.New("refusing to spawn daemon from test binary")
	}

	exePath, err := os.Executable()
	if err != nil {
		// Try to find dfmt in PATH
		if path, err := exec.LookPath("dfmt"); err == nil {
			exePath = path
		} else {
			return errors.New("cannot find dfmt executable")
		}
	}

	// Extra guard: if the resolved executable still looks like a test binary
	// (e.g. os.Executable returned go-build temp path), bail out.
	exeBase := strings.ToLower(filepath.Base(exePath))
	if strings.HasSuffix(exeBase, ".test") || strings.HasSuffix(exeBase, ".test.exe") {
		return fmt.Errorf("refusing to spawn daemon from test binary: %s", exePath)
	}

	// Argv shape:
	//   global mode  → dfmt daemon --global    (no --project)
	//   legacy mode  → dfmt --project <p> daemon
	//
	// Global is the Phase 2 default — the spawned daemon serves every
	// project from one process. Legacy mode is preserved so manually-
	// pinned per-project daemons still come up the same way during the
	// v0.4.x deprecation window.
	var cmd *exec.Cmd
	if globalMode {
		cmd = exec.Command(exePath, "daemon", "--global")
	} else {
		cmd = exec.Command(exePath, "--project", projectPath, "daemon")
	}
	cmd.Dir = projectPath
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child when it exits so we don't leak process handles.
	// recover guards against future refactors where Wait could be replaced
	// by something panicking; today cmd.Wait itself does not panic.
	go func() {
		defer func() { _ = recover() }()
		_ = cmd.Wait()
	}()
	return nil
}

// Connect establishes a connection to the daemon.
func (c *Client) Connect(ctx context.Context) (*transport.Codec, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, c.network, c.address)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	return transport.NewCodec(conn), nil
}

// Remember submits an event to the daemon.
func (c *Client) Remember(ctx context.Context, params transport.RememberParams) (*transport.RememberResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "remember",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.RememberResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Search queries the daemon.
func (c *Client) Search(ctx context.Context, params transport.SearchParams) (*transport.SearchResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "search",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.SearchResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Recall requests a session snapshot.
func (c *Client) Recall(ctx context.Context, params transport.RecallParams) (*transport.RecallResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "recall",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.RecallResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// StreamEvents streams journal events from the daemon via SSE.
// The returned channel is closed when the stream ends or on error.
// Callers must range over the channel or drain it completely.
func (c *Client) StreamEvents(ctx context.Context, from string) (<-chan core.Event, error) {
	var url string
	client := &http.Client{Timeout: 0} // No timeout for streaming.
	// Phase 2: append project_id so the global daemon (which has no
	// default project) can route the stream to the right per-project
	// journal. Empty when c.projectID isn't set; daemon-side falls
	// back to its legacy default project pin in that case.
	pidQuery := ""
	if c.projectID != "" {
		pidQuery = "&project_id=" + neturl.QueryEscape(c.projectID)
	}
	if c.network == netUnix {
		url = "http://unix/api/stream?from=" + neturl.QueryEscape(from) + pidQuery
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: c.timeout}
				return d.DialContext(ctx, netUnix, c.address)
			},
		}
	} else {
		url = fmt.Sprintf("http://%s/api/stream?from=%s%s", c.address, neturl.QueryEscape(from), pidQuery)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req = req.WithContext(ctx)
	if c.sessionID != "" {
		req.Header.Set("X-DFMT-Session", c.sessionID)
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := client.Do(req) //nolint:bodyclose // body closed in goroutine below or short-circuit branch
	if err != nil {
		return nil, fmt.Errorf("stream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream status: %d", resp.StatusCode)
	}

	ch := make(chan core.Event, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		rd := bufio.NewReader(resp.Body)
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					return
				}
				return
			}
			line = strings.TrimSuffix(line, "\r\n")
			line = strings.TrimSuffix(line, "\n")
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			jsonStr := strings.TrimPrefix(line, "data: ")
			var e core.Event
			if err := json.Unmarshal([]byte(jsonStr), &e); err != nil {
				continue
			}
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// DaemonRunning checks if a daemon is reachable for the given project.
// Phase 2: a host-wide global daemon at ~/.dfmt/{port|daemon.sock} is
// the canonical answer. We probe it first; the per-project legacy
// endpoint is only consulted as a fallback for v0.3.x daemons that
// haven't been migrated yet via `dfmt setup --refresh`. Either path
// returning a successful dial is the ground truth.
func DaemonRunning(projectPath string) bool {
	// Step 1: probe the global daemon. fastDialOK is the same liveness
	// check NewClient uses, so the two stay in sync — `dfmt status`
	// will not say "not running" while the next `dfmt stats` succeeds.
	globalAddress, _, globalNetwork, _ := globalDaemonTarget()
	if fastDialOK(globalNetwork, globalAddress) {
		return true
	}

	// Step 2: legacy per-project endpoint. Same shape as pre-Phase-2
	// — read the port file (Windows) or build the socket path (Unix)
	// and dial. A v0.3.x daemon that survived `setup --refresh` lands
	// here; a fresh v0.4.x install never does.
	portFile := filepath.Join(projectPath, ".dfmt", "port")
	socketPath := project.SocketPath(projectPath)

	var network, address string

	if runtime.GOOS == goosWindows {
		network = "tcp"
		if port, _, err := readPortFile(portFile); err == nil && port > 0 {
			address = fmt.Sprintf("127.0.0.1:%d", port)
		}
	} else {
		network = netUnix
		address = socketPath
	}

	if address == "" || (runtime.GOOS == goosWindows && address == addrLocalhost0) {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, network, address)
	if err == nil {
		conn.Close()
		return true
	}

	// Dial failed. If there's a PID file for a dead process, remove it so
	// the next auto-start doesn't see stale state. isProcessRunning is
	// best-effort on Windows; on a false "not running" reading we would
	// still only delete the PID file (port/socket are untouched), so the
	// overall "not running" verdict remains consistent with the failed dial.
	if pid := readPID(projectPath); pid > 0 && !isProcessRunning(pid) {
		cleanupStaleDaemon(projectPath)
	}
	return false
}

// readPID reads the daemon PID from the pid file.
func readPID(projectPath string) int {
	pidPath := filepath.Join(projectPath, ".dfmt", "daemon.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	return pid
}

// isProcessRunning checks if a process with the given PID is running.
// The implementation is platform-specific — see client_unix.go and
// client_windows.go. os.FindProcess + Signal(0) cannot be used on Windows
// because syscall.Signal is not supported there and the call always errors.
func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return isProcessRunningPlatform(pid)
}

// cleanupStaleDaemon cleans up files from a crashed daemon.
func cleanupStaleDaemon(projectPath string) {
	pidPath := filepath.Join(projectPath, ".dfmt", "daemon.pid")
	os.Remove(pidPath)
	// Don't remove port/socket - new daemon will overwrite
}

// Stats returns aggregated statistics from the daemon. params.NoCache
// bypasses the daemon-side TTL cache; CLI callers set it so successive
// runs don't all return the same memoised snapshot.
func (c *Client) Stats(ctx context.Context, params transport.StatsParams) (*transport.StatsResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	// Use HTTP since daemon exposes HTTP endpoint
	body, err := c.doHTTP("/api/stats", transport.Request{
		Method: "stats",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.StatsResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Exec executes code via the daemon (journal logged, intent-filtered).
func (c *Client) Exec(ctx context.Context, params transport.ExecParams) (*transport.ExecResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "exec",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.ExecResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Read reads a file via the daemon (journal logged, intent-filtered).
func (c *Client) Read(ctx context.Context, params transport.ReadParams) (*transport.ReadResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "read",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.ReadResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Fetch fetches a URL via the daemon (journal logged, intent-filtered).
func (c *Client) Fetch(ctx context.Context, params transport.FetchParams) (*transport.FetchResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "fetch",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.FetchResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Glob performs glob pattern matching via the daemon.
func (c *Client) Glob(ctx context.Context, params transport.GlobParams) (*transport.GlobResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "glob",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.GlobResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Grep performs text search via the daemon.
func (c *Client) Grep(ctx context.Context, params transport.GrepParams) (*transport.GrepResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "grep",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.GrepResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Edit edits a file via the daemon.
func (c *Client) Edit(ctx context.Context, params transport.EditParams) (*transport.EditResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "edit",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.EditResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Write writes content to a file via the daemon.
func (c *Client) Write(ctx context.Context, params transport.WriteParams) (*transport.WriteResponse, error) {
	if params.ProjectID == "" {
		params.ProjectID = c.projectID
	}
	body, err := c.doHTTP("/", transport.Request{
		Method: "write",
		Params: mustMarshal(params),
		ID:     1,
	})
	if err != nil {
		return nil, err
	}

	var resp transport.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("rpc error: %s", resp.Error.Message)
	}

	var result transport.WriteResponse
	if err := json.Unmarshal(mustMarshal(resp.Result), &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// doHTTP makes an HTTP JSON-RPC request.
func (c *Client) doHTTP(method string, req transport.Request) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	var url string
	client := &http.Client{Timeout: c.timeout}
	if c.network == netUnix {
		url = "http://unix" + method
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: c.timeout}
				return d.DialContext(ctx, netUnix, c.address)
			},
		}
	} else {
		// net.JoinHostPort-style formatting; for IPv6 literals the address
		// must be bracketed in a URL. Our current caller writes "127.0.0.1:N"
		// so the default path stays correct.
		url = fmt.Sprintf("http://%s%s", c.address, method)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		httpReq.Header.Set("X-DFMT-Session", c.sessionID)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(result)) > maxRPCResponseBytes {
		return nil, fmt.Errorf("rpc response too large: exceeds %d bytes", maxRPCResponseBytes)
	}

	return result, nil
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage{}
	}
	return data
}
