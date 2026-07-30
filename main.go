// Command deployer is the command-line client for a Deployer instance.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sidewalk-developer-group/deployer-cli/internal/cli"
	"github.com/sidewalk-developer-group/deployer-cli/internal/exit"
)

// Overwritten at build time by goreleaser's ldflags.
var (
	version = "dev"
	commit  = ""
)

func main() {
	// One place owns signal handling for the whole process; the follow loop
	// derives its own cancellable context from the command's.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCommand(version, commit)
	root.SetVersionTemplate(version + "\n")
	root.Version = version

	if err := root.ExecuteContext(ctx); err != nil {
		// A message of "" means the command already printed a human-readable
		// failure (a failed deployment, for instance) and only the code matters.
		if message := err.Error(); message != "" {
			fmt.Fprintf(os.Stderr, "%s\n", message)
		}

		os.Exit(exit.Code(err))
	}

	// A cancelled context that reached here without an error still means the
	// operator interrupted us; don't report success.
	if errors.Is(ctx.Err(), context.Canceled) {
		os.Exit(130)
	}
}
