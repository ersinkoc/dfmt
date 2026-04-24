package capture

import (
	"os"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// ShellCapture constructs shell-integration events. Live path today is the
// shell prompt hook → `dfmt capture env.cwd <args>` → daemon Remember RPC →
// journal. The helper returns the Event so either the CLI path or an in-
// process caller can forward it to the journal the same way.
type ShellCapture struct {
	projectPath string
}

// NewShellCapture creates a new shell capturer.
func NewShellCapture(projectPath string) *ShellCapture {
	return &ShellCapture{projectPath: projectPath}
}

// BuildCommand builds a shell-command Event.
func (sc *ShellCapture) BuildCommand(cmd, dir string) core.Event {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  sc.projectPath,
		Type:     core.EvtMCPCall,
		Priority: core.PriP4,
		Source:   core.SrcShell,
		Data: map[string]any{
			"command": cmd,
			"cwd":     dir,
		},
	}
	e.Sig = e.ComputeSig()
	return e
}

// DetectShell detects the current shell.
func DetectShell() string {
	shell := "unknown"
	if s := os.Getenv("SHELL"); s != "" {
		shell = s
	}
	return shell
}
