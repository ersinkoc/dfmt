package client

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// Client is a DFMT daemon client.
type Client struct {
	socketPath string // For debugging/testing only
	network    string
	address    string
	timeout    time.Duration
}

const (
	goosWindows    = "windows"
	netUnix        = "unix"
	addrLocalhost0 = "127.0.0.1:0"
)

// NewClient creates a new client for the given project.
// If the project is not initialized, it auto-initializes.
// If the daemon is not running, it automatically starts it.
func NewClient(projectPath string) (*Client, error) {
	// Auto-init project if not initialized
	if err := autoInitProject(projectPath); err != nil {
		return nil, fmt.Errorf("auto-init project: %w", err)
	}

	socketPath := project.SocketPath(projectPath)
	portFile := filepath.Join(projectPath, ".dfmt", "port")

	var network, address string

	if runtime.GOOS == goosWindows {
		// On Windows, use TCP with port from port file
		network = "tcp"
		address = addrLocalhost0 // Will be overridden by port file

		// Try to read port file
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(string(data)); err == nil {
				address = fmt.Sprintf("127.0.0.1:%d", port)
			}
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
	}

	// Try to connect; if fails, auto-start daemon and retry
	if err := c.ensureDaemon(projectPath); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	return c, nil
}

// ensureDaemon ensures the daemon is running, starting it if needed.
func (c *Client) ensureDaemon(projectPath string) error {
	// Opt-out for tests and embedded callers that manage their own daemon
	// lifecycle. Without this, auto-start re-execs os.Args[0] which under
	// `go test` is the test binary itself — resulting in a fork bomb.
	if os.Getenv("DFMT_DISABLE_AUTOSTART") != "" || isTestBinary() {
		return nil
	}

	portFile := filepath.Join(projectPath, ".dfmt", "port")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to connect
	if _, err := c.Connect(ctx); err == nil {
		return nil // Already running
	}

	// Daemon not running - try to start it
	// First, cleanup any stale daemon state
	cleanupStaleDaemon(projectPath)

	if startErr := startDaemon(projectPath); startErr != nil {
		return fmt.Errorf("failed to start daemon: %w (try: dfmt daemon --foreground to see errors)", startErr)
	}

	// Give daemon time to start (with retry)
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		time.Sleep(500 * time.Millisecond)

		// Update address from port file on Windows
		if runtime.GOOS == goosWindows {
			if data, err := os.ReadFile(portFile); err == nil {
				if port, err := strconv.Atoi(string(data)); err == nil {
					c.address = fmt.Sprintf("127.0.0.1:%d", port)
				}
			}
		}

		// Try to connect
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := c.Connect(ctx2)
		cancel2()
		if err == nil {
			return nil
		}

		// Check if we should retry
		if i == maxRetries-1 {
			return fmt.Errorf("daemon started but not responding (try: dfmt daemon --foreground to debug)")
		}
	}

	return fmt.Errorf("daemon failed to start after %d retries", maxRetries)
}

// autoInitProject initializes the project if .dfmt/ doesn't exist.
func autoInitProject(projectPath string) error {
	dfmtDir := filepath.Join(projectPath, ".dfmt")
	configPath := filepath.Join(dfmtDir, "config.yaml")

	// Check if already initialized
	if _, err := os.Stat(configPath); err == nil {
		return nil // Already initialized
	}

	// Create .dfmt/ directory
	if err := os.MkdirAll(dfmtDir, 0755); err != nil {
		return fmt.Errorf("create .dfmt dir: %w", err)
	}

	// Write default config
	if err := os.WriteFile(configPath, []byte(config.DefaultConfigYAML()), 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Add .dfmt/ to .gitignore if exists
	gitignorePath := filepath.Join(projectPath, ".gitignore")
	if content, err := os.ReadFile(gitignorePath); err == nil {
		if !bytes.Contains(content, []byte(".dfmt/")) {
			f, _ := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
			f.WriteString("\n.dfmt/\n")
			f.Close()
		}
	}

	// Write project-level Claude Code settings to enforce DFMT tools
	// and wire session continuity hooks (PreCompact save, SessionStart load).
	claudeDir := filepath.Join(projectPath, ".claude")
	os.MkdirAll(claudeDir, 0755)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settingsData := `{
  "hooks": {
    "PreCompact": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "cd \"$CLAUDE_PROJECT_PATH\" && .dfmt/dfmt recall --format md 2>/dev/null > .dfmt/last-recall.md || echo '# No recall data' > .dfmt/last-recall.md",
        "timeout": 30,
        "statusMessage": "Saving session snapshot for next session..."
      }]
    }],
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "if [ -f \"$CLAUDE_PROJECT_PATH/.dfmt/last-recall.md\" ]; then echo '--- Previous session summary ---' && cat \"$CLAUDE_PROJECT_PATH/.dfmt/last-recall.md\" && echo '--- End of previous session ---'; fi",
        "timeout": 10,
        "statusMessage": "Loading previous session summary..."
      }]
    }]
  },
  "permissions": {
    "allow": [
      "mcp__dfmt__dfmt.read",
      "mcp__dfmt__dfmt.exec",
      "mcp__dfmt__dfmt.fetch",
      "mcp__dfmt__dfmt.remember",
      "mcp__dfmt__dfmt.search",
      "mcp__dfmt__dfmt.recall",
      "mcp__dfmt__dfmt.stats",
      "Bash(.dfmt/dfmt recall --format md *)"
    ]
  }
}
`
	os.WriteFile(settingsPath, []byte(settingsData), 0644)

	return nil
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

// startDaemon starts the dfmt daemon for the given project.
func startDaemon(projectPath string) error {
	// Belt-and-braces: refuse to re-exec a test binary even if a caller
	// reaches this function directly. See isTestBinary for the rationale.
	if isTestBinary() {
		return fmt.Errorf("refusing to spawn daemon from test binary")
	}

	exePath, err := os.Executable()
	if err != nil {
		// Try to find dfmt in PATH
		if path, err := exec.LookPath("dfmt"); err == nil {
			exePath = path
		} else {
			return fmt.Errorf("cannot find dfmt executable")
		}
	}

	// Extra guard: if the resolved executable still looks like a test binary
	// (e.g. os.Executable returned go-build temp path), bail out.
	exeBase := strings.ToLower(filepath.Base(exePath))
	if strings.HasSuffix(exeBase, ".test") || strings.HasSuffix(exeBase, ".test.exe") {
		return fmt.Errorf("refusing to spawn daemon from test binary: %s", exePath)
	}

	cmd := exec.Command(exePath, "daemon")
	cmd.Dir = projectPath
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the child when it exits so we don't leak process handles.
	go func() { _ = cmd.Wait() }()
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

// DaemonRunning checks if a daemon is running for the project.
// It actually tries to connect and verify via health check.
func DaemonRunning(projectPath string) bool {
	portFile := filepath.Join(projectPath, ".dfmt", "port")
	socketPath := project.SocketPath(projectPath)

	var network, address string

	if runtime.GOOS == goosWindows {
		network = "tcp"
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(string(data)); err == nil {
				address = fmt.Sprintf("127.0.0.1:%d", port)
			}
		}
	} else {
		network = netUnix
		address = socketPath
	}

	if address == "" || (runtime.GOOS == goosWindows && address == addrLocalhost0) {
		return false
	}

	// Try to connect — a successful dial is the ground truth that a daemon
	// is accepting requests, regardless of PID file state.
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

// Stats returns aggregated statistics from the daemon.
func (c *Client) Stats(ctx context.Context) (*transport.StatsResponse, error) {
	// Use HTTP since daemon exposes HTTP endpoint
	params := mustMarshal(transport.StatsParams{})
	body, err := c.doHTTP("/api/stats", transport.Request{
		Method: "stats",
		Params: params,
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
		url = fmt.Sprintf("http://%s%s", c.address, method)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
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
