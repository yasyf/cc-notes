package helperapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
)

const (
	serviceWorkerLimit    = 1
	serviceReadinessLimit = 15 * time.Second
	serviceCloseLimit     = 30 * time.Second
)

// EnsureService installs the fixed helper LaunchAgent and proves its exact transport ready.
func EnsureService(ctx context.Context) error {
	plan, err := NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	if err := withServiceController(ctx, func(controller *service.Controller) error {
		health, healthErr := observeServiceHealth(ctx, plan.Paths().Socket)
		if healthErr != nil && !errors.Is(healthErr, os.ErrNotExist) {
			return fmt.Errorf("cc-notes helper: observe service before converge: %w", healthErr)
		}
		if healthErr == nil && (health.RuntimeBuild != plan.BuildID() ||
			health.RuntimeProtocol != mountproto.RuntimeProtocolVersion) {
			if err := settleServiceRuntime(ctx, controller, plan, health, wire.StopIntentUpgrade); err != nil {
				return err
			}
		}
		return controller.Converge(ctx, []service.Agent{plan.Deployment().Agent()})
	}); err != nil {
		return fmt.Errorf("cc-notes helper: install service: %w", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, serviceReadinessLimit)
	defer cancel()
	var lastErr error
	for {
		health, healthErr := observeServiceHealth(readyCtx, plan.Paths().Socket)
		if healthErr == nil {
			ready, readinessErr := serviceHealthReady(plan.BuildID(), health)
			if readinessErr != nil {
				lastErr = readinessErr
			}
			if ready {
				return nil
			}
			if readinessErr == nil {
				lastErr = errors.New("helper runtime is not healthy")
			}
		} else {
			lastErr = healthErr
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("cc-notes helper: wait for service readiness: %w", errors.Join(readyCtx.Err(), lastErr))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func observeServiceHealth(ctx context.Context, socket string) (health mountproto.RuntimeHealthResponse, resultErr error) {
	client, err := mountservice.NewClient(ctx, wire.ClientConfig{Dial: wire.UnixDialer(socket)})
	if err != nil {
		return mountproto.RuntimeHealthResponse{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	return client.RuntimeHealth(ctx)
}

func serviceHealthReady(build string, health mountproto.RuntimeHealthResponse) (bool, error) {
	if err := validateServiceHealthIdentity(build, health); err != nil {
		return false, err
	}
	return health.Ready && !health.Draining && health.State == mountproto.RuntimeStateHealthy, nil
}

func validateServiceHealthIdentity(build string, health mountproto.RuntimeHealthResponse) error {
	if health.RuntimeBuild != build {
		return errors.New("cc-notes helper: service health has the wrong runtime identity")
	}
	return validateServiceHealthTarget(health)
}

func validateServiceHealthTarget(health mountproto.RuntimeHealthResponse) error {
	if health.RuntimeBuild == "" || health.RuntimeProtocol != mountproto.RuntimeProtocolVersion {
		return errors.New("cc-notes helper: service health has the wrong runtime identity")
	}
	if health.ProcessGeneration == "" {
		return errors.New("cc-notes helper: service health has no process generation")
	}
	return nil
}

// StopService settles the exact runtime and removes the complete desired service set.
func StopService(ctx context.Context) error {
	plan, err := NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	health, healthErr := observeServiceHealth(ctx, plan.Paths().Socket)
	if healthErr != nil && !errors.Is(healthErr, os.ErrNotExist) {
		return fmt.Errorf("cc-notes helper: observe service before stop: %w", healthErr)
	}
	return withServiceController(ctx, func(controller *service.Controller) error {
		if healthErr == nil {
			if err := settleServiceRuntime(ctx, controller, plan, health, wire.StopIntentUninstall); err != nil {
				return err
			}
		}
		if err := controller.Converge(ctx, nil); err != nil {
			return fmt.Errorf("cc-notes helper: remove service: %w", err)
		}
		return nil
	})
}

func settleServiceRuntime(
	ctx context.Context,
	controller *service.Controller,
	plan holder.RuntimePlan,
	health mountproto.RuntimeHealthResponse,
	intent wire.StopIntent,
) error {
	if err := validateServiceHealthTarget(health); err != nil {
		return err
	}
	if _, err := controller.StopRuntime(ctx, service.StopControlSpec{
		Executable: plan.Deployment().RuntimeExecutable(), Args: holder.StopControlChildArguments(),
		Role: StopControlRole, RuntimeBuild: plan.BuildID(),
		RuntimeProtocol:         int(mountproto.RuntimeProtocolVersion),
		TargetProcessGeneration: health.ProcessGeneration,
		Intent:                  intent,
	}); err != nil {
		return fmt.Errorf("cc-notes helper: settle service runtime: %w", err)
	}
	return nil
}

func withServiceController(ctx context.Context, operation func(*service.Controller) error) (err error) {
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return err
	}
	controller, err := service.NewController(ctx, service.ControllerConfig{
		StatePath:   filepath.Join(runtimeDirectory, "service-state.db"),
		ProcessPath: serviceProcessPath(runtimeDirectory),
		WorkerLimit: serviceWorkerLimit,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serviceCloseLimit)
		defer cancel()
		err = errors.Join(err, controller.Close(closeCtx))
	}()
	return operation(controller)
}
