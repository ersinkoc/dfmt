package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ersinkoc/dfmt/internal/project"
	"github.com/ersinkoc/dfmt/internal/transport"
)

// Client is a DFMT daemon client.
type Client struct {
	socketPath string
	timeout   time.Duration
}

// NewClient creates a new client for the given project.
func NewClient(projectPath string) (*Client, error) {
	socketPath := project.SocketPath(projectPath)
	return &Client{
		socketPath: socketPath,
		timeout:   5 * time.Second,
	}, nil
}

// Connect establishes a connection to the daemon.
func (c *Client) Connect(ctx context.Context) (*transport.Codec, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
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
	socketPath := project.SocketPath(projectPath)
	_, err := os.Stat(socketPath)
	return err == nil
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
