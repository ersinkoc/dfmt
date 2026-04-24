package capture

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// GitCapture captures git events.
type GitCapture struct {
	projectPath string
}

// NewGitCapture creates a new git capturer.
func NewGitCapture(projectPath string) *GitCapture {
	return &GitCapture{projectPath: projectPath}
}

// SubmitCommit submits a git commit event.
//
// Deprecated: This method is a CLI-proxy stub. The live git-commit ingestion
// path is: git post-commit hook → dfmt capture git commit <args> → daemon
// Remember RPC → journal. This stub exists so the GitCapture type can be used
// directly if a journal.Writer is available, but currently the daemon-side
// submission always goes through the CLI binary.
func (gc *GitCapture) SubmitCommit(ctx context.Context, hash string, message string) error {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitCommit,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"hash":    hash,
			"message": firstLine(message),
		},
	}
	e.Sig = e.ComputeSig()

	// Live path: git hook → dfmt capture git commit <args> → client.Remember
	_ = ctx
	_ = e
	return nil
}

// SubmitCheckout submits a git checkout event.
//
// Deprecated: This method is a CLI-proxy stub. The live git-checkout ingestion
// path is: git post-checkout hook → dfmt capture git checkout <args> → daemon
// Remember RPC → journal. This stub exists so the GitCapture type can be used
// directly if a journal.Writer is available, but currently the daemon-side
// submission always goes through the CLI binary.
func (gc *GitCapture) SubmitCheckout(ctx context.Context, ref string, isBranch bool) error {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitCheckout,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"ref":       ref,
			"is_branch": isBranch,
		},
	}
	e.Sig = e.ComputeSig()

	// Live path: git hook → dfmt capture git checkout <args> → client.Remember
	_ = ctx
	return nil
}

// SubmitPush submits a git push event.
//
// Deprecated: This method is a CLI-proxy stub. The live git-push ingestion path
// is: git pre-push hook → dfmt capture git push <args> → daemon Remember RPC
// → journal. This stub exists so the GitCapture type can be used directly if a
// journal.Writer is available, but currently the daemon-side submission always
// goes through the CLI binary.
func (gc *GitCapture) SubmitPush(ctx context.Context, remote string, branch string) error {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Type:     core.EvtGitPush,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"remote": remote,
			"branch": branch,
		},
	}
	e.Sig = e.ComputeSig()

	// Live path: git hook → dfmt capture git push <args> → client.Remember
	_ = ctx
	return nil
}

// GitLog parses git log output.
func GitLog(limit int) ([]GitCommit, error) {
	cmd := exec.Command("git", "log", "--oneline", "-n", strconv.Itoa(limit))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var commits []GitCommit
	for line := range strings.SplitSeq(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			commits = append(commits, GitCommit{
				Hash:    parts[0],
				Message: parts[1],
			})
		}
	}
	return commits, nil
}

// GitCommit represents a git commit.
type GitCommit struct {
	Hash    string
	Message string
}

func firstLine(s string) string {
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[0])
}
