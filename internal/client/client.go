package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

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
	addrLocalhost0 = "localhost:0"
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
				address = fmt.Sprintf("localhost:%d", port)
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
	portFile := filepath.Join(projectPath, ".dfmt", "port")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to connect
	if _, err := c.Connect(ctx); err == nil {
		return nil // Already running
	}

	// Daemon not running - try to start it
	if startErr := startDaemon(projectPath); startErr != nil {
		return fmt.Errorf("start daemon: %w", startErr)
	}

	// Give daemon time to start
	time.Sleep(500 * time.Millisecond)

	// Update address from port file on Windows
	if runtime.GOOS == goosWindows {
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(string(data)); err == nil {
				c.address = fmt.Sprintf("localhost:%d", port)
			}
		}
	}

	// Retry connection once
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()

	if _, err := c.Connect(ctx2); err != nil {
		return fmt.Errorf("connect to daemon after start: %w", err)
	}

	return nil
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
	defaultConfig := `version: 1

capture:
  mcp:
    enabled: true
  fs:
    enabled: true
    watch:
      - "**"
    ignore:
      - ".git/**"
      - "node_modules/**"

storage:
  durability: batched
  journal_max_bytes: 10485760

lifecycle:
  idle_timeout: 30m
`

	if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
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
	claudeDir := filepath.Join(projectPath, ".claude")
	os.MkdirAll(claudeDir, 0755)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settingsData := `{
  "permissions": {
    "deny": ["Bash", "Read", "WebFetch"],
    "allow": [
      "mcp__dfmt__dfmt.read",
      "mcp__dfmt__dfmt.exec",
      "mcp__dfmt__dfmt.fetch",
      "mcp__dfmt__dfmt.remember",
      "mcp__dfmt__dfmt.search",
      "mcp__dfmt__dfmt.recall",
      "mcp__dfmt__dfmt.stats"
    ]
  }
}
`
	os.WriteFile(settingsPath, []byte(settingsData), 0644)

	return nil
}

// startDaemon starts the dfmt daemon for the given project.
func startDaemon(projectPath string) error {
	exePath, err := os.Executable()
	if err != nil {
		// Try to find dfmt in PATH
		if path, err := exec.LookPath("dfmt"); err == nil {
			exePath = path
		} else {
			return fmt.Errorf("cannot find dfmt executable")
		}
	}

	cmd := exec.Command(exePath, "daemon")
	cmd.Dir = projectPath
	cmd.Stdout = nil
	cmd.Stderr = nil

	return cmd.Start()
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
// It actually tries to connect to verify the daemon is responsive.
func DaemonRunning(projectPath string) bool {
	portFile := filepath.Join(projectPath, ".dfmt", "port")
	socketPath := project.SocketPath(projectPath)

	var network, address string

	if runtime.GOOS == goosWindows {
		network = "tcp"
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(string(data)); err == nil {
				address = fmt.Sprintf("localhost:%d", port)
			}
		}
	} else {
		network = netUnix
		address = socketPath
	}

	if address == "" || (runtime.GOOS == goosWindows && address == addrLocalhost0) {
		return false
	}

	// Actually try to connect
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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

	url := fmt.Sprintf("http://%s%s", c.address, method)
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: c.timeout}
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
