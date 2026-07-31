package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/transport"
)

// Regression: the MCP stdio loop was read → handle → write, one request at a
// time, so a single slow tool call blocked every other dfmt tool in the
// session. Observed during review: a dfmt_read waited more than two minutes
// behind a running `go test` exec. JSON-RPC allows concurrent in-flight
// requests and out-of-order responses; the loop just never used that.

// stubHandler answers "fast" immediately and holds "slow" until released.
type stubHandler struct {
	release chan struct{}

	mu      sync.Mutex
	started []string
}

func (s *stubHandler) Handle(ctx context.Context, req *transport.MCPRequest) (*transport.MCPResponse, error) {
	var params struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &params)

	s.mu.Lock()
	s.started = append(s.started, params.Name)
	s.mu.Unlock()

	// Mirror MCPProtocol.Handle: a notification (no id) gets no response.
	if req.ID == nil {
		return nil, nil
	}

	if params.Name == "slow" {
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	return &transport.MCPResponse{JSONRPC: "2.0", Result: params.Name, ID: req.ID}, nil
}

// mcpLine renders one JSON-RPC request line for the given id and tool name.
func mcpLine(id int, name string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q}}`+"\n", id, name)
}

// syncBuffer is an io.Writer safe for the concurrent writers under test. The
// loop guards stdout with its own mutex, but the test's own reads of the
// buffer race with those writes without this.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeMCPStdioDoesNotHeadOfLineBlock(t *testing.T) {
	h := &stubHandler{release: make(chan struct{})}
	out := &syncBuffer{}

	// A blocked "slow" call first, then a "fast" one behind it. Keep the pipe
	// open so the loop cannot finish on EOF before the fast call is answered.
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, mcpLine(1, "slow"))
		_, _ = io.WriteString(pw, mcpLine(2, "fast"))
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveMCPStdio(t.Context(), pr, out, h)
	}()

	// The fast response must arrive while the slow call is still blocked.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), `"result":"fast"`) {
		select {
		case <-deadline:
			t.Fatalf("fast call never answered while a slow call was in flight (out=%q)", out.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if strings.Contains(out.String(), `"result":"slow"`) {
		t.Fatal("slow call answered before it was released; the stub is not blocking")
	}

	close(h.release)
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveMCPStdio did not return after EOF")
	}

	// Both responses must be present and correlatable by id.
	got := out.String()
	if !strings.Contains(got, `"result":"slow"`) {
		t.Errorf("slow response missing from output: %q", got)
	}
	if strings.Count(got, `"jsonrpc":"2.0"`) != 2 {
		t.Errorf("want exactly 2 responses, got %q", got)
	}
}

// TestServeMCPStdioWaitsForInFlightOnEOF: when stdin closes mid-call, the
// response must still be written. Dropping it would leave the client waiting
// on an id that never arrives.
func TestServeMCPStdioWaitsForInFlightOnEOF(t *testing.T) {
	h := &stubHandler{release: make(chan struct{})}
	out := &syncBuffer{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Reader hits EOF immediately after the single request line.
		serveMCPStdio(t.Context(), strings.NewReader(mcpLine(7, "slow")), out, h)
	}()

	// Give the loop time to reach EOF with the call still running, then let it finish.
	time.Sleep(100 * time.Millisecond)
	close(h.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveMCPStdio did not return")
	}
	if !strings.Contains(out.String(), `"id":7`) {
		t.Errorf("in-flight response was dropped at EOF: %q", out.String())
	}
}

// Notifications (no id) must produce no bytes at all, concurrency or not.
func TestServeMCPStdioNotificationsAreSilent(t *testing.T) {
	h := &stubHandler{release: make(chan struct{})}
	close(h.release)
	out := &syncBuffer{}

	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	serveMCPStdio(t.Context(), in, out, h)

	if got := out.String(); got != "" {
		t.Errorf("notification produced output: %q", got)
	}
}
