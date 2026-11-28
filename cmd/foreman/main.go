// Command foreman is the command line entry point.
package main

import (
	"context"
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
	if len(args) == 1 && args[0] == "daemon" {
		return runDaemon(context.Background(), os.Getenv("FOREMAN_HOME"), stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: foreman version | daemon")
	return 2
}
