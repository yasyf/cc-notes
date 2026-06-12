// Command cc-notes is a git-native notes and tasks layer for agents. All data
// lives as objects in the git ODB on refs/cc-notes/*, synced with the repo.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yasyf/cc-notes/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", cli.Label(err), err)
		stop()
		os.Exit(cli.ExitCode(err))
	}
}
