//go:build darwin && cgo && fuse

// Command cc-notes-holder is the fixed signed FuseKit runtime executable.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/holderapp"
	"github.com/yasyf/cc-notes/internal/holdercontract"
	"github.com/yasyf/fusekit/holder"
)

func run(ctx context.Context, arguments []string) error {
	repository, provisioning, err := holdercontract.ParseProvision(arguments)
	if err != nil {
		return err
	}
	if provisioning {
		if err := holderapp.EnsureService(ctx); err != nil {
			return err
		}
		plan, err := holderapp.NewRuntimePlan(ctx)
		if err != nil {
			return err
		}
		return fusefs.ProvisionRepository(ctx, plan, repository)
	}
	if len(arguments) == 1 {
		switch arguments[0] {
		case "--install-service":
			return holderapp.EnsureService(ctx)
		case "--stop-service":
			return holderapp.StopService(ctx)
		}
	}
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
		return errors.New("cc-notes holder: unknown invocation")
	}
	plan, err := holderapp.NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	runtime, err := fusefs.NewHolderRuntime(ctx, fusefs.HolderRuntimeConfig{
		Plan: plan, Drivers: drivers,
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
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
