//go:build darwin && cgo && fuse

// Command cc-notes-helper is the fixed signed FuseKit runtime executable.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/helperapp"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/fusekit/holder"
)

func run(ctx context.Context, arguments []string) error {
	if recognized, err := helperapp.RunStopControlChild(ctx, arguments); recognized {
		return err
	}
	repository, provisioning, err := helpercontract.ParseProvision(arguments)
	if err != nil {
		return err
	}
	if provisioning {
		if err := helperapp.EnsureService(ctx); err != nil {
			return err
		}
		plan, err := helperapp.NewRuntimePlan(ctx)
		if err != nil {
			return err
		}
		return fusefs.ProvisionRepository(ctx, plan, repository)
	}
	if len(arguments) == 1 {
		switch arguments[0] {
		case "--install-service":
			return helperapp.EnsureService(ctx)
		case "--stop-service":
			return helperapp.StopService(ctx)
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
		return errors.New("cc-notes helper: unknown invocation")
	}
	plan, err := helperapp.NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	runtime, err := fusefs.NewHelperRuntime(ctx, fusefs.HelperRuntimeConfig{
		Plan: plan, Drivers: drivers,
		StopRole: helperapp.StopControlRole, StopControlStore: helperapp.StopControlStore(plan),
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
