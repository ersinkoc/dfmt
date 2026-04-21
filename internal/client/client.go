package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
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
	network   string
	address   string
	timeout   time.Duration
}

// NewClient creates a new client for the given project.
func NewClient(projectPath string) (*Client, error) {
	socketPath := project.SocketPath(projectPath)
	portFile := filepath.Join(projectPath, ".dfmt", "port")

	var network, address string

	if runtime.GOOS == "windows" {
		// On Windows, use TCP with port from port file
		network = "tcp"
		address = "localhost:0" // Will be overridden by port file

		// Try to read port file
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(string(data)); err == nil {
				address = fmt.Sprintf("localhost:%d", port)
			}
		}
	} else {
		// On Unix, use Unix socket
		network = "unix"
		address = socketPath
	}

	return &Client{
		socketPath: socketPath, // For debugging
		network:   network,
		address:   address,
		timeout:   5 * time.Second,
	}, nil
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
	codec, err := c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer codec.ReadResponse() // drain

	req := &transport.Request{
		Method: "remember",
		Params: mustMarshal(params),
		ID:     1,
	}

	if err := codec.WriteRequest(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
	codec, err := c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer codec.ReadResponse()

	req := &transport.Request{
		Method: "search",
		Params: mustMarshal(params),
		ID:     1,
	}

	if err := codec.WriteRequest(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
	codec, err := c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer codec.ReadResponse()

	req := &transport.Request{
		Method: "recall",
		Params: mustMarshal(params),
		ID:     1,
	}

	if err := codec.WriteRequest(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
	if runtime.GOOS == "windows" {
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
	codec, err := c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer codec.ReadResponse()

	req := &transport.Request{
		Method: "stats",
		Params: mustMarshal(transport.StatsParams{}),
		ID:     1,
	}

	if err := codec.WriteRequest(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	resp, err := codec.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage{}
	}
	return data
}
