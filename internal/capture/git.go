package capture

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ersinkoc/dfmt/internal/core"
)

// GitCapture constructs git-hook events. The live ingestion path today is
// git hook → `dfmt capture git <subcmd>` → daemon Remember RPC → journal.
// These helpers return the Event so either the CLI-proxy path or an in-
// process caller (tests, future daemon-side git ingestion) can hand the
// event to the journal uniformly.
type GitCapture struct {
	projectPath string
}

// NewGitCapture creates a new git capturer.
func NewGitCapture(projectPath string) *GitCapture {
	return &GitCapture{projectPath: projectPath}
}

// BuildCommit builds a git-commit Event from a hook payload.
func (gc *GitCapture) BuildCommit(hash, message string) core.Event {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  gc.projectPath,
		Type:     core.EvtGitCommit,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"hash":    hash,
			"message": firstLine(message),
		},
	}
	e.Sig = e.ComputeSig()
	return e
}

// BuildCheckout builds a git-checkout Event from a hook payload.
func (gc *GitCapture) BuildCheckout(ref string, isBranch bool) core.Event {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  gc.projectPath,
		Type:     core.EvtGitCheckout,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"ref":       ref,
			"is_branch": isBranch,
		},
	}
	e.Sig = e.ComputeSig()
	return e
}

// BuildPush builds a git-push Event from a hook payload.
func (gc *GitCapture) BuildPush(remote, branch string) core.Event {
	e := core.Event{
		ID:       string(core.NewULID(time.Now())),
		TS:       time.Now(),
		Project:  gc.projectPath,
		Type:     core.EvtGitPush,
		Priority: core.PriP2,
		Source:   core.SrcGitHook,
		Data: map[string]any{
			"remote": remote,
			"branch": branch,
		},
	}
	e.Sig = e.ComputeSig()
	return e
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
