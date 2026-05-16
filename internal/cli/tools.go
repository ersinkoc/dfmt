package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ersinkoc/dfmt/internal/config"
	"github.com/ersinkoc/dfmt/internal/sandbox"
	"github.com/ersinkoc/dfmt/internal/transport"
)

func runExec(args []string) int {
	var lang string
	var intent string

	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.StringVar(&lang, "lang", "bash", "Language (bash, sh, node, python, etc.)")
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: code required\n")
		return 1
	}

	code := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// acquireBackend nil means promote failed AND no remote daemon
	// reachable. The direct-sandbox fallback below preserves v0.5.x
	// degraded-mode behavior for `dfmt exec` standalone use.
	backend, ownedDaemon := acquireBackend(proj)
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	var execResp *transport.ExecResponse
	if backend != nil {
		execResp, err = backend.Exec(ctx, transport.ExecParams{
			Code:      code,
			Lang:      lang,
			Intent:    intent,
			ProjectID: proj,
		})
	} else {
		err = errors.New("backend unavailable")
	}
	if err != nil {
		// Fallback to direct sandbox if daemon not available. Honor
		// cfg.Exec.PathPrepend so the CLI fallback isn't stricter than
		// the daemon path.
		var pp []string
		if c, cerr := config.Load(proj); cerr == nil && c != nil {
			pp = c.Exec.PathPrepend
		}
		resp, err := sandbox.NewSandboxWithPolicy(proj, loadProjectPolicy(proj)).
			WithPathPrepend(pp).Exec(ctx, sandbox.ExecReq{
			Code:   code,
			Lang:   lang,
			Intent: intent,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagJSON {
			fmt.Println(mustMarshalJSON(resp))
		} else {
			fmt.Print(resp.Stdout)
		}
		return 0
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(execResp))
	} else {
		if execResp.Summary != "" {
			fmt.Print(execResp.Summary)
		} else {
			fmt.Print(execResp.Stdout)
		}
		if execResp.Stderr != "" {
			fmt.Fprintf(os.Stderr, "stderr: %s\n", execResp.Stderr)
		}
	}

	return 0
}

func runRead(args []string) int {
	var intent string
	var offset, limit int64

	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.Int64Var(&offset, "offset", 0, "Byte offset")
	fs.Int64Var(&limit, "limit", 0, "Max bytes to read")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: path required\n")
		return 1
	}
	path := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	var readResp *transport.ReadResponse
	if backend != nil {
		readResp, err = backend.Read(ctx, transport.ReadParams{
			Path:      path,
			Intent:    intent,
			Offset:    offset,
			Limit:     limit,
			ProjectID: proj,
		})
	} else {
		err = errors.New("backend unavailable")
	}
	if err != nil {
		// Fallback to direct sandbox
		resp, err := sandbox.NewSandboxWithPolicy(proj, loadProjectPolicy(proj)).Read(ctx, sandbox.ReadReq{
			Path:   path,
			Intent: intent,
			Offset: offset,
			Limit:  limit,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagJSON {
			fmt.Println(mustMarshalJSON(resp))
		} else {
			fmt.Print(resp.Content)
		}
		return 0
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(readResp))
	} else {
		if len(readResp.Matches) > 0 {
			for _, m := range readResp.Matches {
				fmt.Printf("%s\n", m.Text)
			}
		} else {
			fmt.Print(readResp.Content)
		}
	}

	return 0
}

func runFetch(args []string) int {
	var intent string
	var method string
	var body string
	var timeout int

	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.StringVar(&method, "method", "GET", "HTTP method")
	fs.StringVar(&body, "body", "", "Request body")
	fs.IntVar(&timeout, "timeout", 30, "Timeout in seconds")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: URL required\n")
		return 1
	}
	url := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	fetchResp, err := backend.Fetch(ctx, transport.FetchParams{
		URL:       url,
		Intent:    intent,
		Method:    method,
		Body:      body,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(fetchResp))
	} else {
		if len(fetchResp.Matches) > 0 {
			for _, m := range fetchResp.Matches {
				fmt.Printf("%s\n", m.Text)
			}
		} else {
			fmt.Print(fetchResp.Body)
		}
	}

	return 0
}

func runGlob(args []string) int {
	var intent string

	fs := flag.NewFlagSet("glob", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: pattern required\n")
		return 1
	}
	pattern := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	globResp, err := backend.Glob(ctx, transport.GlobParams{
		Pattern:   pattern,
		Intent:    intent,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(globResp))
	} else {
		for _, f := range globResp.Files {
			fmt.Println(f)
		}
	}

	return 0
}

func runGrep(args []string) int {
	var intent string
	var files string
	var caseInsensitive bool

	fs := flag.NewFlagSet("grep", flag.ContinueOnError)
	fs.StringVar(&intent, "intent", "", "Intent for content filtering")
	fs.StringVar(&files, "files", "*", "File pattern")
	fs.BoolVar(&caseInsensitive, "i", false, "Case insensitive")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: pattern required\n")
		return 1
	}
	pattern := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	grepResp, err := backend.Grep(ctx, transport.GrepParams{
		Pattern:         pattern,
		Files:           files,
		Intent:          intent,
		CaseInsensitive: caseInsensitive,
		ProjectID:       proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(grepResp))
	} else {
		for _, m := range grepResp.Matches {
			fmt.Printf("%s:%d: %s\n", m.File, m.Line, m.Content)
		}
	}

	return 0
}

func runEdit(args []string) int {
	var oldString string

	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	fs.StringVar(&oldString, "old", "", "String to replace (required)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "error: path and new-string required\n")
		return 1
	}
	path := fs.Arg(0)
	newString := fs.Arg(1)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	editResp, err := backend.Edit(ctx, transport.EditParams{
		Path:      path,
		OldString: oldString,
		NewString: newString,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(editResp))
	} else {
		fmt.Println(editResp.Summary)
	}

	return 0
}

func runWrite(args []string) int {
	var content string

	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	fs.StringVar(&content, "content", "", "Content to write")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "error: path required\n")
		return 1
	}
	path := fs.Arg(0)

	proj, err := getProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	backend, ownedDaemon := acquireBackend(proj)
	if backend == nil {
		return 1
	}
	defer func() {
		if ownedDaemon != nil {
			waitForDaemonShutdown(ownedDaemon)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	ctx = transport.WithProjectID(ctx, proj)

	writeResp, err := backend.Write(ctx, transport.WriteParams{
		Path:      path,
		Content:   content,
		ProjectID: proj,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if flagJSON {
		fmt.Println(mustMarshalJSON(writeResp))
	} else {
		fmt.Println(writeResp.Summary)
	}

	return 0
}

// loadProjectPolicy returns the merged sandbox policy for `proj`, composed
// from DefaultPolicy() plus any operator override at
// `<proj>/.dfmt/permissions.yaml`. Used by the local-fallback sandbox paths
// in this file when the daemon isn't available — the daemon path uses the
// same call directly in internal/daemon/daemon.go.
//
// Errors and hard-deny override warnings are emitted to stderr; the CLI
// fallback never hard-fails on policy load (a typo in permissions.yaml
// shouldn't keep the agent from running).
func loadProjectPolicy(proj string) sandbox.Policy {
	if proj == "" {
		return sandbox.DefaultPolicy()
	}
	res, err := sandbox.LoadPolicyMerged(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: permissions: %v\n", err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: permissions: %s\n", w)
	}
	return res.Policy
}
