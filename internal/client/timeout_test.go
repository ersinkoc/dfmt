package client

import (
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/timeouts"
)

// Regression tests for the client-side timeout cap.
//
// Every tool call went through one http.Client built with the client's
// single `timeout` field, which is timeouts.RPC (5 s). The sandbox honours
// DefaultExecTimeout (60 s), MaxExecTimeout (300 s), and the `timeout`
// argument the MCP schema advertises as "Timeout in seconds. Default: 60" —
// but the caller had already hung up at 5 s, so nothing slower than that was
// reachable through the daemon regardless of what the caller asked for.
//
// It did not present as a cap: the subprocess kept running server-side and
// the client returned "context deadline exceeded", which reads as a hung
// command. Running a build or a test suite through dfmt_exec was simply
// impossible, and no error named the real limit.

func TestExecRPCTimeoutExceedsTheOldFiveSecondCap(t *testing.T) {
	// The specific regression: a default-timeout exec must be given far more
	// than timeouts.RPC, or `go build` / `go test` can never complete.
	got := execRPCTimeout(0)
	if got <= timeouts.RPC {
		t.Fatalf("execRPCTimeout(0) = %v, must exceed the RPC timeout %v — "+
			"this is the cap that made long commands unreachable", got, timeouts.RPC)
	}
	if got <= sandboxDefaultExecTimeout {
		t.Errorf("execRPCTimeout(0) = %v, want > the sandbox default %v so the "+
			"client outlives the work it asked for", got, sandboxDefaultExecTimeout)
	}
}

func TestExecRPCTimeoutDerivesFromCallerRequest(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{"zero means the sandbox default", 0, sandboxDefaultExecTimeout + rpcHeadroom},
		{"negative treated as default", -5, sandboxDefaultExecTimeout + rpcHeadroom},
		{"honours an explicit request", 120, 120*time.Second + rpcHeadroom},
		{"clamped at the sandbox maximum", 9999, sandboxMaxExecTimeout + rpcHeadroom},
		{"exactly the maximum", 300, sandboxMaxExecTimeout + rpcHeadroom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := execRPCTimeout(tc.seconds); got != tc.want {
				t.Errorf("execRPCTimeout(%d) = %v, want %v", tc.seconds, got, tc.want)
			}
		})
	}
}

// The client must always outlive the daemon-side deadline. If the two were
// equal they would race, and the caller would see a transport timeout
// instead of the sandbox's own timed_out result — which carries the partial
// output the caller actually wants.
func TestExecRPCTimeoutAlwaysLeavesHeadroom(t *testing.T) {
	for _, secs := range []int{0, 1, 30, 60, 300, 100000} {
		got := execRPCTimeout(secs)
		serverSide := time.Duration(secs) * time.Second
		if secs <= 0 {
			serverSide = sandboxDefaultExecTimeout
		}
		if serverSide > sandboxMaxExecTimeout {
			serverSide = sandboxMaxExecTimeout
		}
		if got <= serverSide {
			t.Errorf("execRPCTimeout(%d) = %v, must exceed the daemon-side deadline %v",
				secs, got, serverSide)
		}
	}
}

// The mirrored constants must track internal/sandbox. They are duplicated
// rather than imported (sandbox pulls in the policy/runtime tree, and client
// is linked into every short-lived CLI invocation), so drift is possible and
// this test is what catches it.
//
// Keep in sync with sandbox.DefaultExecTimeout / sandbox.MaxExecTimeout.
func TestExecRPCTimeoutMatchesSandboxLimits(t *testing.T) {
	if sandboxDefaultExecTimeout != 60*time.Second {
		t.Errorf("sandboxDefaultExecTimeout = %v, want 60s (sandbox.DefaultExecTimeout)",
			sandboxDefaultExecTimeout)
	}
	if sandboxMaxExecTimeout != 300*time.Second {
		t.Errorf("sandboxMaxExecTimeout = %v, want 300s (sandbox.MaxExecTimeout)",
			sandboxMaxExecTimeout)
	}
}

// File and tree operations get the Tool budget, not the 5 s RPC budget: a
// grep over a large monorepo routinely takes longer than 5 s and is not
// governed by any caller-supplied timeout.
func TestToolTimeoutExceedsRPCTimeout(t *testing.T) {
	if timeouts.Tool <= timeouts.RPC {
		t.Errorf("timeouts.Tool (%v) must exceed timeouts.RPC (%v); read/glob/grep/"+
			"edit/write are bounded by disk work, not by daemon bookkeeping",
			timeouts.Tool, timeouts.RPC)
	}
}
