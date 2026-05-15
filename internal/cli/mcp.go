package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ersinkoc/dfmt/internal/daemon"
	"github.com/ersinkoc/dfmt/internal/logging"
	"github.com/ersinkoc/dfmt/internal/setup"
	"github.com/ersinkoc/dfmt/internal/transport"
)

func runMCP(_ []string) int {
	// MCP over stdio - read MCP JSON-RPC from stdin, write to stdout.
	//
	// v0.6.0: single-binary self-promotion. If a global daemon is
	// already running this process is a thin client (NewClient + the
	// ClientBackend adapter). If no daemon is running, *this* process
	// becomes the daemon via daemon.PromoteInProcess and uses the
	// in-process Handlers as its Backend — no exec.Command, no second
	// dfmt.exe in tasklist, no spawn-and-handoff dance. When the MCP
	// stdio loop exits (Claude Code closes), the daemon role keeps
	// running until SIGINT or the idle-exit timer fires; another
	// dfmt invocation on the same host can connect during that window.
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
	var ownedDaemon *daemon.Daemon
	if proj != "" {
		// Auto-init the project on every MCP startup. Same idempotent
		// steps as `dfmt init`. Failure of any single step is non-fatal —
		// the daemon will handle missing-config the same way it handles
		// fresh projects.
		if ierr := setup.EnsureProjectInitialized(proj); ierr != nil {
			logging.Warnf("auto-init %s: %v", proj, ierr)
		}

		// MCP is long-lived: prefer in-process promotion so an MCP-
		// driven session shows exactly one dfmt.exe in tasklist instead
		// of "agent → dfmt mcp → spawned dfmt daemon" (two processes).
		backend, ownedDaemon = acquireBackendForLongRunner(proj)
		if backend == nil {
			fmt.Fprintln(os.Stderr,
				"dfmt mcp: cannot reach or promote daemon — tool calls will return -32603.")
		}
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

	// Read/write MCP JSON-RPC. Use bufio.Reader with a per-message cap so
	// an oversized line produces a -32700 parse error and the session
	// continues, instead of bufio.Scanner's ErrTooLong which kills the
	// entire stdio loop.
	const mcpMaxLineBytes = 1 << 20 // 1 MiB per JSON-RPC message
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

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

		// Handle via MCP protocol with panic recovery. Without this guard a
		// nil-deref or out-of-bounds inside any handler would tear down the
		// entire stdio loop and the agent would silently lose all dfmt tools
		// mid-session — exactly the "MCP fail olunca patlayan sistem" failure
		// mode this project exists to prevent.
		var resp *transport.MCPResponse
		func() {
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
			resp, _ = mcp.Handle(mcpCtx, &req)
		}()

		// JSON-RPC notifications (no ID) yield a nil response and MUST NOT
		// produce any bytes on stdout — writing {} or null would confuse
		// the client's request/response correlation.
		if resp == nil {
			continue
		}

		// Write response
		if err := json.NewEncoder(writer).Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "write error: %v\n", err)
			break
		}
		_ = writer.Flush()
	}

	// Stdin EOF (Claude Code closed the MCP transport). When this
	// process owns the daemon, keep it running so other dfmt CLI
	// invocations on the same host stay served — exit only on
	// SIGINT/SIGTERM or when the daemon idle-exits.
	//
	// DFMT_MCP_EXIT_ON_EOF=1 short-circuits this: the process stops the
	// owned daemon and exits as soon as stdin closes. This exists for
	// CI / smoke-test harnesses (dev.ps1's MCP smoke test, in-tree
	// integration tests) that pipe a finite request stream through
	// `dfmt mcp` and need the process to terminate before their job
	// timeout fires. Production agents (Claude Code, Cursor, …) keep
	// stdin open for the entire session so the env var is irrelevant
	// to them.
	if ownedDaemon != nil {
		if os.Getenv("DFMT_MCP_EXIT_ON_EOF") == "1" {
			stopCtx, cancel := context.WithTimeout(context.Background(), ownedDaemon.ShutdownGrace())
			defer cancel()
			_ = ownedDaemon.Stop(stopCtx)
			return 0
		}
		waitForDaemonShutdown(ownedDaemon)
	}
	return 0
}
