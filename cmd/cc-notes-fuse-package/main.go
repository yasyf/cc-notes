// Command cc-notes-fuse-package embeds the reviewed FUSE-T bundle in CCNotesHelper.app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yasyf/cc-notes/internal/helperapp"
	"github.com/yasyf/cc-notes/internal/helperclient"
)

func run(ctx context.Context, arguments []string) (resultErr error) {
	flags := flag.NewFlagSet("cc-notes-fuse-package", flag.ContinueOnError)
	appPath := flags.String("app", "", "exact application bundle path")
	signingIdentity := flags.String("signing-identity", "", "Developer ID signing identity")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *appPath == "" || *signingIdentity == "" {
		return errors.New("cc-notes-fuse-package: -app and -signing-identity are required")
	}
	runner, err := helperclient.NewToolRunner(ctx)
	if err != nil {
		return fmt.Errorf("cc-notes-fuse-package: start tool runner: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Close(ctx)) }()
	if err := helperapp.PackageFUSE(ctx, runner, *signingIdentity, *appPath); err != nil {
		return fmt.Errorf("cc-notes-fuse-package: %w", err)
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
