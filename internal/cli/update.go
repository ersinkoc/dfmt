package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/ersinkoc/dfmt/internal/selfupdate"
	"github.com/ersinkoc/dfmt/internal/version"
)

// updateTimeout bounds the whole update: release lookup, two downloads,
// and the new binary's --version smoke test.
const updateTimeout = 5 * time.Minute

// runUpdate replaces this binary with a GitHub release (ADR-0028).
//
// The binary is replaced in place, so every agent config that points at
// this path keeps working, and the running daemon is stopped so the next
// command starts the new one (the identity check would also catch the
// replaced file, but only on the next daemon-backed command).
func runUpdate(args []string) int {
	var check, force bool
	var tag string
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.BoolVar(&check, "check", false, "Only report whether a newer release exists")
	fs.StringVar(&tag, "version", "", "Install this release tag (e.g. v0.8.0) instead of the latest")
	fs.BoolVar(&force, "force", false, "Install even when not newer, or over a development build")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "error: update takes no positional arguments (use --version <tag>)")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	client := &http.Client{Timeout: updateTimeout}

	rel, err := selfupdate.FetchRelease(ctx, client, tag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	current := version.Current
	cur, curOK := selfupdate.ParseVersion(current)
	next, nextOK := selfupdate.ParseVersion(rel.Tag)
	newer := curOK && nextOK && cur.Less(next)
	fmt.Printf("Current: %s\nRelease: %s\n", current, rel.Tag)

	if check {
		switch {
		case !curOK:
			fmt.Println("This is a development build; `dfmt update --force` replaces it with the release.")
		case newer:
			fmt.Println("An update is available: run `dfmt update`.")
		default:
			fmt.Println("Up to date.")
		}
		return 0
	}

	switch {
	case force:
	case !curOK:
		fmt.Fprintf(os.Stderr,
			"error: %s is a development build, not a release; pass --force to replace it with %s\n",
			current, rel.Tag)
		return 1
	case tag == "" && !newer:
		fmt.Println("Up to date.")
		return 0
	case tag != "" && current == rel.Tag:
		fmt.Printf("Already running %s.\n", rel.Tag)
		return 0
	}

	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	assetURL, ok := rel.Assets[asset]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: release %s has no build for %s/%s (%s)\n",
			rel.Tag, runtime.GOOS, runtime.GOARCH, asset)
		return 1
	}
	sumsURL, ok := rel.Assets["sha256sums.txt"]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: release %s publishes no sha256sums.txt; refusing an unverifiable binary\n", rel.Tag)
		return 1
	}

	exe, err := selfupdate.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: locate this binary: %v\n", err)
		return 1
	}

	fmt.Printf("Downloading %s...\n", asset)
	data, err := selfupdate.Download(ctx, client, assetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	sums, err := selfupdate.Download(ctx, client, sumsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := selfupdate.VerifyChecksum(sums, asset, data); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println("sha256 verified")

	if err := selfupdate.Install(exe, data, rel.Tag); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Updated %s: %s -> %s\n", exe, current, rel.Tag)

	// Retire the daemon so the next command runs the new code. Agents'
	// long-running `dfmt mcp` proxies keep the old code until the agent
	// restarts them; they talk to the new daemon meanwhile.
	if info := readGlobalDaemonInfo(); info.Alive {
		if rc := stopGlobalDaemon(); rc != 0 {
			fmt.Fprintln(os.Stderr, "warning: could not stop the running daemon; the next dfmt command restarts it")
		}
	}
	fmt.Println("Restart your AI agents to load the new `dfmt mcp`.")
	return 0
}
