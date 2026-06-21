// Command cc-notes is the single binary behind both `cc-notes` and its `ccn`
// symlink. All data lives as objects in the git ODB on refs/cc-notes/*, synced
// with the repo.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yasyf/cc-notes/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCmd()
	// Present the invoked name (ccn or cc-notes) in help/usage.
	if filepath.Base(os.Args[0]) == "ccn" {
		root.Use = "ccn"
	}

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", cli.Label(err), err)
		stop()
		os.Exit(cli.ExitCode(err))
	}
}
