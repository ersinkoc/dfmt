package main

import (
	"fmt"
	"os"

	"github.com/ersinkoc/dfmt/internal/cli"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("dfmt %s\n", version)
		return
	}

	// Handle global flags before dispatch. We strip them out of args[] so
	// subcommand flagsets don't see them. Global-flag state is propagated via
	// exported setter functions rather than a package-level flag.Parse (which
	// is never called in this binary — relying on it is how the --json flag
	// has been broken for all subcommands historically).
	args := os.Args[1:]
	cleaned := args[:0]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--project":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "dfmt: --project requires a value")
				os.Exit(1)
			}
			val := args[i+1]
			if len(val) > 0 && val[0] == '-' {
				fmt.Fprintf(os.Stderr, "dfmt: invalid --project value: %q\n", val)
				os.Exit(1)
			}
			_ = os.Setenv("DFMT_PROJECT", val)
			cli.SetGlobalProject(val)
			i++
		case arg == "--json":
			cli.SetGlobalJSON(true)
		default:
			cleaned = append(cleaned, arg)
		}
	}

	code := cli.Dispatch(cleaned)
	os.Exit(code)
}
