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

	// Handle global flags before dispatch
	args := os.Args[1:]

	// Check for --project flag early
	for i, arg := range args {
		if arg == "--project" && i+1 < len(args) {
			val := args[i+1]
			if len(val) > 0 && val[0] == '-' {
				fmt.Fprintf(os.Stderr, "dfmt: invalid --project value: %q\n", val)
				os.Exit(1)
			}
			_ = os.Setenv("DFMT_PROJECT", val)
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	// Dispatch
	code := cli.Dispatch(args)
	os.Exit(code)
}
