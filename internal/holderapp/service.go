package holderapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/transportproto"
)

const (
	serviceWorkerLimit    = 1
	serviceReadinessLimit = 15 * time.Second
	serviceCloseLimit     = 30 * time.Second
)

var errServiceBootstrapFailed = errors.New("cc-notes holder: service bootstrap failed")

// EnsureService installs the fixed holder LaunchAgent and proves its exact transport ready.
func EnsureService(ctx context.Context) error {
	plan, err := NewRuntimePlan(ctx)
	if err != nil {
		return err
	}
	if err := convergeServices(ctx, []service.Agent{plan.Deployment().Agent()}); err != nil {
		return fmt.Errorf("cc-notes holder: install service: %w", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, serviceReadinessLimit)
	defer cancel()
	peer := &wire.LifecyclePeer{Config: wire.ClientConfig{
		Dial: wire.UnixDialer(plan.Paths().Socket), Build: transportproto.Build,
		LifecycleBuild: plan.BuildID(),
	}}
	var lastErr error
	for {
		health, healthErr := peer.Health(readyCtx)
		if healthErr == nil {
			ready, readinessErr := serviceHealthReady(plan.BuildID(), health)
			if readinessErr != nil {
				if errors.Is(readinessErr, errServiceBootstrapFailed) {
					_ = peer.Close()
					return readinessErr
				}
				lastErr = readinessErr
			}
			if ready {
				return peer.Close()
			}
			if readinessErr == nil {
				lastErr = errors.New("holder runtime is not healthy")
			}
		} else {
			lastErr = healthErr
		}
		select {
		case <-readyCtx.Done():
			_ = peer.Close()
			return fmt.Errorf("cc-notes holder: wait for service readiness: %w", errors.Join(readyCtx.Err(), lastErr))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func serviceHealthReady(build string, health daemon.Health) (bool, error) {
	if health.Build != build || health.Protocol != int(wire.ProtocolVersion) {
		return false, errors.New("cc-notes holder: service health has the wrong build or protocol")
	}
	if health.State == daemon.StateFailed {
		return false, errServiceBootstrapFailed
	}
	return health.State == daemon.StateHealthy && !health.Draining, nil
}

// StopService removes the holder from the complete desired service set.
func StopService(ctx context.Context) error {
	if err := convergeServices(ctx, nil); err != nil {
		return fmt.Errorf("cc-notes holder: remove service: %w", err)
	}
	return nil
}

func convergeServices(ctx context.Context, agents []service.Agent) (err error) {
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return err
	}
	controller, err := service.NewController(ctx, service.ControllerConfig{
		StatePath:   filepath.Join(runtimeDirectory, "service-state.db"),
		ProcessPath: filepath.Join(runtimeDirectory, "service-processes.db"),
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
	return controller.Converge(ctx, agents)
}
