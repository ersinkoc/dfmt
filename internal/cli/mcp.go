package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

func runMCP(_ []string) int {
	// MCP over stdio - read MCP JSON-RPC from stdin, write to stdout.
	//
	// This process is a PURE PROXY: it connects to the global daemon
	// (starting one if necessary via ensureGlobalDaemon) and forwards
	// MCP calls over the socket. It never adopts the daemon role itself.
	// The global daemon outlives this process — closing stdin simply
	// terminates the proxy; the daemon keeps running for other callers.
	//
	// Project resolution: getProject() walks up looking for .dfmt/ or
	// .git/. A miss leaves the backend nil so every tool call returns
	// -32603 ("daemon not connected"). The MCP server itself still
	// starts so the agent's MCP host doesn't see a dead transport.
	proj, projErr := getProject()
	if projErr != nil {
		proj = ""
	}

	var backend transport.Backend
	if proj != "" {
		// Auto-init the project on every MCP startup. Same idempotent
		// steps as `dfmt init`. Failure of any single step is non-fatal —
		// the daemon will handle missing-config the same way it handles
		// fresh projects.
		if ierr := setup.EnsureProjectInitialized(proj); ierr != nil {
			logging.Warnf("auto-init %s: %v", proj, ierr)
		}

		// acquireBackendForLongRunner ensures the global daemon is up
		// before we connect. Always returns nil daemon — pure proxy.
		initial, _ := acquireBackendForLongRunner(proj)
		if initial == nil {
			fmt.Fprintln(os.Stderr,
				"dfmt mcp: cannot reach global daemon — will retry on the first tool call.")
		}
		// Wrap so this long-lived proxy survives the daemon going away
		// underneath it. The daemon idle-exits after 30 minutes and agent
		// sessions routinely idle longer, so without this every tool call
		// after that point failed with a raw dial error until the user
		// manually reconnected the MCP server. A nil initial backend is
		// fine — the wrapper dials on first use.
		backend = newReconnectingBackend(proj, initial)
	} else {
		fmt.Fprintln(os.Stderr,
			"dfmt mcp: no project found — tool calls will return -32603. "+
				"Open dfmt from a project root or set DFMT_PROJECT.")
	}

	mcp := transport.NewMCPProtocol(backend)
	// SetProjectID still pins the per-call project_id stamp the proxy
	// adds to every Backend invocation. Empty-proj is fine: the daemon
	// then routes via its default project (legacy single-project mode)
	// or returns errProjectIDRequired (global mode) — which agent-side
	// surfaces as -32603.
	mcp.SetProjectID(proj)

	// Per-process cancellable context. Canceled on stdin EOF (deferred
	// cancel below) or on SIGINT/SIGTERM (signal goroutine). Threaded into
	// every mcp.Handle call so a long-running tool invocation honors
	// graceful shutdown — pre-fix, handleToolsCall used context.Background()
	// and a Ctrl-C waited for the handler's own per-call timeout.
	mcpCtx, mcpCancel := context.WithCancel(context.Background())
	defer mcpCancel()
	mcpSig := make(chan os.Signal, 1)
	signal.Notify(mcpSig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(mcpSig)
	go func() {
		select {
		case <-mcpSig:
			mcpCancel()
		case <-mcpCtx.Done():
		}
	}()

	// The stdio loop lives in serveMCPStdio so it can be exercised with
	// ordinary pipes in a test; runMCP is the wiring around it.
	serveMCPStdio(mcpCtx, os.Stdin, os.Stdout, mcp)

	// Stdin EOF (Claude Code closed the MCP transport). This process is
	// a pure proxy — exit immediately. The global daemon keeps running.
	return 0
}

// mcpHandler is the slice of MCPProtocol that the stdio loop needs. Narrowing
// it to one method is what lets the loop be tested with a stub that can be
// made slow on demand — the property under test (a slow call must not delay
// the others) is otherwise only observable against a real daemon.
type mcpHandler interface {
	Handle(context.Context, *transport.MCPRequest) (*transport.MCPResponse, error)
}

// serveMCPStdio runs the MCP JSON-RPC loop over one reader/writer pair until
// the reader reports EOF, then waits for in-flight calls to finish.
func serveMCPStdio(ctx context.Context, in io.Reader, out io.Writer, h mcpHandler) {
	// Read/write MCP JSON-RPC. Use bufio.Reader with a per-message cap so
	// an oversized line produces a -32700 parse error and the session
	// continues, instead of bufio.Scanner's ErrTooLong which kills the
	// entire stdio loop.
	const mcpMaxLineBytes = 1 << 20 // 1 MiB per JSON-RPC message

	// mcpMaxConcurrentCalls bounds how many tool calls this proxy will have
	// in flight at once. It is a backstop, not a throughput knob: the daemon
	// enforces its own per-tool limits (exec 4, read/fetch 8, write 4), and
	// an agent that batches more than this just queues here instead of
	// piling goroutines onto the daemon.
	const mcpMaxConcurrentCalls = 16
	reader := bufio.NewReader(in)
	writer := bufio.NewWriter(out)

	writeParseError := func() {
		resp := transport.MCPResponse{
			JSONRPC: "2.0",
			Error: &transport.RPCError{
				Code:    -32700,
				Message: "Parse error",
			},
		}
		_ = json.NewEncoder(writer).Encode(resp)
		_ = writer.Flush()
	}

	// readCapped reads one line up to max bytes. If max is exceeded, the
	// remaining bytes of that line are discarded until the next newline so
	// the next call starts at a fresh message boundary.
	readCapped := func(max int) (line []byte, overflow bool, err error) {
		buf := make([]byte, 0, 512)
		for {
			b, rerr := reader.ReadByte()
			if rerr != nil {
				if rerr == io.EOF && len(buf) == 0 {
					return nil, false, rerr
				}
				return buf, false, rerr
			}
			if b == '\n' {
				return buf, false, nil
			}
			if len(buf) >= max {
				// Drain to next newline so the next iteration starts clean.
				for {
					b2, derr := reader.ReadByte()
					if derr != nil || b2 == '\n' {
						return nil, true, nil
					}
				}
			}
			buf = append(buf, b)
		}
	}

	// Requests are read serially (one stdin) but HANDLED concurrently.
	//
	// The loop used to be read → handle → write, so a single slow call
	// blocked every other tool in the session. That is not a rare case: an
	// agent issues parallel tool calls routinely, and dfmt_exec may legally
	// run for minutes. Observed during review: a dfmt_read sat behind a
	// running `go test` exec for over two minutes and the agent gave up and
	// fell back to native tools — the one outcome this project exists to
	// prevent. JSON-RPC has always allowed concurrent in-flight requests and
	// out-of-order responses; the `id` is what correlates them.
	//
	// Three things keep that safe:
	//   - one writer, guarded by writeMu, so two responses never interleave
	//     on stdout;
	//   - a bounded worker count, so a pathological client cannot spawn
	//     unbounded goroutines against the daemon (which has its own
	//     per-tool semaphores behind this);
	//   - `initialize` stays inline, because handleInitialize mutates the
	//     session ID and is documented as running before any tool call.
	var (
		writeMu sync.Mutex
		inFlag  sync.WaitGroup
		slots   = make(chan struct{}, mcpMaxConcurrentCalls)
	)

	writeResp := func(resp *transport.MCPResponse) bool {
		// JSON-RPC notifications (no ID) yield a nil response and MUST NOT
		// produce any bytes on stdout — writing {} or null would confuse
		// the client's request/response correlation.
		if resp == nil {
			return true
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := json.NewEncoder(writer).Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "write error: %v\n", err)
			return false
		}
		_ = writer.Flush()
		return true
	}

	// handle runs one request with panic recovery. Without this guard a
	// nil-deref or out-of-bounds inside any handler would tear down the
	// entire stdio loop and the agent would silently lose all dfmt tools
	// mid-session — exactly the "MCP fail olunca patlayan sistem" failure
	// mode this project exists to prevent. Recovery is per-request, so one
	// bad call no longer takes its concurrent siblings with it either.
	handle := func(req *transport.MCPRequest) (resp *transport.MCPResponse) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "mcp handle panic recovered: %v\n", r)
				if req.ID != nil {
					resp = &transport.MCPResponse{
						JSONRPC: "2.0",
						Error: &transport.RPCError{
							Code:    -32603,
							Message: fmt.Sprintf("Internal error: %v", r),
						},
						ID: req.ID,
					}
				}
				// Notifications (req.ID == nil) get no response on panic;
				// per JSON-RPC 2.0 they never get one.
			}
		}()
		resp, _ = h.Handle(ctx, req)
		return resp
	}

	for {
		line, overflow, err := readCapped(mcpMaxLineBytes)
		if overflow {
			writeParseError()
			continue
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "read error: %v\n", err)
			}
			break
		}
		if len(line) == 0 {
			continue
		}

		// Parse MCP request
		var req transport.MCPRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeParseError()
			continue
		}

		// The handshake is ordering-sensitive and cheap: run it on this
		// goroutine so no tool call can observe a half-initialized session.
		if req.Method == "initialize" {
			if !writeResp(handle(&req)) {
				break
			}
			continue
		}

		slots <- struct{}{}
		inFlag.Add(1)
		go func(req transport.MCPRequest) {
			defer inFlag.Done()
			defer func() { <-slots }()
			// A write failure here (client closed stdout) cannot break the
			// read loop from a worker; the next read returns EOF and the
			// loop ends on its own.
			_ = writeResp(handle(&req))
		}(req)
	}

	// Stdin is closed, but in-flight calls may still be writing. Waiting
	// keeps their responses from racing the process exit — and, more
	// importantly, keeps a half-written JSON line off stdout.
	inFlag.Wait()
}
