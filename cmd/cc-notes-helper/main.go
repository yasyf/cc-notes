//go:build darwin && cgo && fuse

// Command cc-notes-helper is the fixed signed FuseKit runtime executable.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/helperapp"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/fusekit/holder"
)

func run(ctx context.Context, arguments []string) error {
	drivers, err := fusefs.NewGitDriverFactories()
	if err != nil {
		return err
	}
	recognized, err := holder.RunChild(ctx, arguments, holder.ChildConfig{
		Stdout: os.Stdout, Drivers: drivers,
	})
	if recognized {
		return err
	}
	if len(arguments) != 0 {
		return errors.New("FuseKit runtime: unknown invocation")
	}
	plan, err := helperapp.NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	stopControlStore, err := deployment.RuntimeStopControlStore()
	if err != nil {
		return fmt.Errorf("FuseKit runtime: resolve deployment stop authority: %w", err)
	}
	runtime, err := fusefs.NewHelperRuntime(ctx, fusefs.HelperRuntimeConfig{
		Plan: plan, Drivers: drivers,
		TrustRequirements: helperapp.RuntimeTrustRequirements(), StopControlStore: stopControlStore,
		NativeReadinessTimeout:  helpercontract.RuntimeNativeReadinessTimeout,
		CatalogReadinessTimeout: helpercontract.RuntimeCatalogReadinessTimeout,
		CatalogOperationTimeout: helpercontract.RuntimeCatalogOperationTimeout,
		ShutdownTimeout:         helpercontract.RuntimeShutdownTimeout,
	})
	if err != nil {
		return err
	}
	return runtime.Run(ctx)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		if !strings.HasPrefix(err.Error(), "FuseKit runtime:") {
			err = fmt.Errorf("FuseKit runtime: %w", err)
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
