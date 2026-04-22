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
// If the daemon is not running, it automatically starts it.
func NewClient(projectPath string) (*Client, error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.Connect(ctx); err != nil {
		// Daemon not running - try to start it
		if startErr := startDaemon(projectPath); startErr == nil {
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
		}
	}

	return c, nil
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
func DaemonRunning(projectPath string) bool {
	if runtime.GOOS == goosWindows {
		// On Windows, check for port file first
		portFile := filepath.Join(projectPath, ".dfmt", "port")
		if _, err := os.Stat(portFile); err == nil {
			return true
		}
		// Also check for socket file (for testing/compatibility)
		socketPath := project.SocketPath(projectPath)
		_, err := os.Stat(socketPath)
		return err == nil
	}
	// On Unix, check for socket file
	socketPath := project.SocketPath(projectPath)
	_, err := os.Stat(socketPath)
	return err == nil
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
