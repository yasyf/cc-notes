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
	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/model"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if handled, err := fusefs.RunHolderChild(ctx, os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "cc-notes holder child: %s\n", err)
			os.Exit(1)
		}
		return
	}

	root := cli.NewRootCmd()
	// Present the invoked name (ccn or cc-notes) in help/usage.
	if filepath.Base(os.Args[0]) == "ccn" {
		root.Use = "ccn"
	}

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", cli.Label(err), cli.Message(err))
		if hint := cli.Hint(err); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		if hint := upgradeHint(err); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		stop()
		os.Exit(cli.ExitCode(err))
	}
}

// upgradeHint returns the remediation line for history this binary does not
// speak: an unknown op kind (e.g. add_attachment read by a pre-LFS binary)
// means the entity was written by a newer cc-notes, and a fold kind mismatch
// (e.g. add_anchor on a runbook read by a pre-anchor binary) usually does too.
// It returns "" for every other error.
func upgradeHint(err error) string {
	var unknown *model.UnknownKindError
	if errors.As(err, &unknown) {
		return fmt.Sprintf("op kind %q was written by a newer cc-notes; run `brew upgrade yasyf/tap/cc-notes` and retry", unknown.Kind)
	}
	if errors.Is(err, fold.ErrKindMismatch) {
		return "this entity carries history this cc-notes cannot fold; if it was written by a newer cc-notes, run `brew upgrade yasyf/tap/cc-notes` and retry"
	}
	return ""
}
