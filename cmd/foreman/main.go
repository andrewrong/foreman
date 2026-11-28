// Command foreman is the command line entry point.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/andrewrong/foreman/internal/sandbox/launch"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	launch.MaybeRunSession()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "foreman %s\n", version)
		return 0
	}
	if len(args) > 0 && args[0] == "daemon" {
		fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
		fs.SetOutput(stderr)
		tick := fs.Duration("tick", DefaultTick, "pause between scheduler rounds")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *tick <= 0 {
			fmt.Fprintln(stderr, "usage: foreman daemon [--tick <duration>]")
			return 2
		}
		return runDaemon(context.Background(), os.Getenv("FOREMAN_HOME"), *tick, stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: foreman version | daemon")
	return 2
}
