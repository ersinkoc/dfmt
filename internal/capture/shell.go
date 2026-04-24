package capture

import (
	"context"
	"os"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// ShellCapture captures shell events.
type ShellCapture struct {
	projectPath string
}

// NewShellCapture creates a new shell capturer.
func NewShellCapture(projectPath string) *ShellCapture {
	return &ShellCapture{projectPath: projectPath}
}

// SubmitCommand submits a shell command event.
func (sc *ShellCapture) SubmitCommand(ctx context.Context, cmd string, dir string) error {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtMCPCall,
		Priority: core.PriP4,
		Source:   core.SrcShell,
		Data: map[string]any{
			"command": cmd,
			"cwd":     dir,
		},
	}
	e.Sig = e.ComputeSig()

	// Would submit to daemon here
	_ = ctx
	_ = e
	return nil
}

// DetectShell detects the current shell.
func DetectShell() string {
	shell := "unknown"
	if s := getEnv("SHELL"); s != "" {
		shell = s
	}
	return shell
}

func getEnv(key string) string {
	return os.Getenv(key)
}
