// Command cc-notes is the single binary behind both `cc-notes` and its `ccn`
// symlink. All data lives as objects in the git ODB on refs/cc-notes/*, synced
// with the repo.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/model"
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
		if hint := upgradeHint(err); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		stop()
		os.Exit(cli.ExitCode(err))
	}
}

// upgradeHint returns the remediation line for an op kind this binary does
// not speak (e.g. add_attachment or remove_attachment read by a pre-LFS
// binary): an unknown kind means the entity was written by a newer cc-notes,
// so the fix is always to upgrade. It returns "" for every other error.
func upgradeHint(err error) string {
	var unknown *model.UnknownKindError
	if !errors.As(err, &unknown) {
		return ""
	}
	return fmt.Sprintf("op kind %q was written by a newer cc-notes; run `brew upgrade yasyf/tap/cc-notes` and retry", unknown.Kind)
}
