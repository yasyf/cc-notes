package helperdeployment

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
)

const readinessPoll = 100 * time.Millisecond

type productHooks struct {
	buildID          string
	policyDigest     deployment.SHA256
	verifyInstalled  func(context.Context, string, string) (string, string, error)
	servicePlan      func(deployment.Operation) (service.Plan, error)
	servicePlanBuild func(deployment.Operation, string) (service.Plan, error)
	target           func(deployment.Operation) (runtimeTarget, error)
	targetBuild      func(deployment.Operation, string) (runtimeTarget, error)
	observe          func(context.Context, string) (mountproto.RuntimeHealthResponse, error)
	identities       func(string) ([]proc.Identity, error)
}

func newProductHooks(buildID string, policyDigest deployment.SHA256) productHooks {
	hooks := productHooks{
		buildID: buildID, policyDigest: policyDigest,
		verifyInstalled: verifyInstalledGeneration, observe: observeRuntimeHealth,
		identities: proc.ExecutableIdentities,
	}
	hooks.servicePlan = hooks.servicePlanForOperation
	hooks.servicePlanBuild = hooks.servicePlanForBuild
	hooks.target = hooks.runtimeTargetForOperation
	hooks.targetBuild = hooks.runtimeTargetForBuild
	return hooks
}

type runtimeTarget struct {
	executable       string
	socket           string
	buildID          string
	presentationRoot string
}

func (h productHooks) runtimeQuiesce(
	ctx context.Context,
	stopper deployment.RuntimeStopper,
	operation deployment.RuntimeQuiesceOperation,
) (deployment.RuntimeProof, error) {
	productOperation := deployment.Operation{
		ID: operation.ID, Generation: operation.Generation, Role: operation.Role,
	}
	target, err := h.target(productOperation)
	if err != nil {
		return deployment.RuntimeProof{}, err
	}
	health, observeErr := h.observe(ctx, target.socket)
	if observeErr != nil {
		identities, inventoryErr := h.identities(target.executable)
		if inventoryErr != nil {
			return deployment.RuntimeProof{}, fmt.Errorf(
				"cc-notes helper: prove prior runtime absence: %w",
				errors.Join(observeErr, inventoryErr),
			)
		}
		if len(identities) != 0 {
			return deployment.RuntimeProof{}, fmt.Errorf(
				"cc-notes helper: prior runtime endpoint is unavailable while %d exact process(es) remain: %w",
				len(identities), observeErr,
			)
		}
		return deployment.RuntimeProof{
			Role: operation.Role, Absent: true,
			Digest: h.proofDigest("runtime-absent", productOperation, target.executable),
		}, nil
	}
	if err := validateRuntimeTarget(health); err != nil {
		return deployment.RuntimeProof{}, err
	}
	switch operation.Intent {
	case wire.StopIntentUpgrade, wire.StopIntentRestart, wire.StopIntentUninstall:
	default:
		return deployment.RuntimeProof{}, errors.New("cc-notes helper: runtime quiesce has an invalid stop intent")
	}
	result, err := stopper.StopRuntime(ctx, service.StopControlSpec{
		Executable: target.executable, Args: holder.StopControlChildArguments(),
		Role: helperclient.DeploymentStopRole, RuntimeBuild: h.buildID,
		RuntimeProtocol:         int(mountproto.RuntimeProtocolVersion),
		TargetProcessGeneration: health.ProcessGeneration, Intent: operation.Intent,
	})
	if err != nil {
		return deployment.RuntimeProof{}, fmt.Errorf("cc-notes helper: settle prior runtime: %w", err)
	}
	if !result.Stopped || result.ProcessGeneration != health.ProcessGeneration ||
		result.RuntimeBuild != health.RuntimeBuild || result.RuntimeProtocol != int(mountproto.RuntimeProtocolVersion) {
		return deployment.RuntimeProof{}, errors.New("cc-notes helper: stop result does not match the observed runtime generation")
	}
	return deployment.RuntimeProof{
		Role:              operation.Role,
		ProcessGeneration: health.ProcessGeneration,
		Digest: h.proofDigest(
			"runtime-quiesced", productOperation, string(operation.Intent), h.buildID,
			health.RuntimeBuild, health.ProcessGeneration,
		),
	}, nil
}

func (h productHooks) postInstallProof(ctx context.Context, operation deployment.Operation) (deployment.Proof, error) {
	return h.installedProof(ctx, "post-install", operation)
}

func (h productHooks) priorAppRestoreProof(ctx context.Context, operation deployment.Operation) (deployment.Proof, error) {
	return h.installedProof(ctx, "prior-restored", operation)
}

func (h productHooks) installedProof(
	ctx context.Context,
	kind string,
	operation deployment.Operation,
) (deployment.Proof, error) {
	library, digest, err := h.verifyInstalled(ctx, operation.Generation.Path, h.buildID)
	if err != nil {
		return deployment.Proof{}, err
	}
	if library == "" || digest == "" {
		return deployment.Proof{}, errors.New("cc-notes helper: installed app has no exact FUSE proof")
	}
	return deployment.Proof{
		Role: operation.Role, Digest: h.proofDigest(kind, operation, library, digest),
	}, nil
}

func (h productHooks) buildPlan(_ context.Context, operation deployment.Operation) (service.Plan, error) {
	return h.servicePlan(operation)
}

func (h productHooks) readiness(
	ctx context.Context,
	operation deployment.Operation,
	got service.Plan,
) (deployment.Proof, error) {
	buildID, err := exactPlanBuildID(operation, got)
	if err != nil {
		return deployment.Proof{}, err
	}
	want, err := h.servicePlanBuild(operation, buildID)
	if err != nil {
		return deployment.Proof{}, err
	}
	if got.Digest() != want.Digest() || !reflect.DeepEqual(got.Agents(), want.Agents()) {
		return deployment.Proof{}, errors.New("cc-notes helper: readiness plan is not the exact helper plan")
	}
	target, err := h.targetBuild(operation, buildID)
	if err != nil {
		return deployment.Proof{}, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, holder.StandardReadinessContract().ObservationTimeout())
	defer cancel()
	var lastErr error
	for {
		health, observeErr := h.observe(readyCtx, target.socket)
		if observeErr == nil {
			validateErr := validateRuntimeReadiness(target, health)
			if validateErr == nil {
				return deployment.Proof{
					Role: operation.Role, PlanDigest: operation.PlanDigest,
					Digest: h.proofDigest(
						"runtime-ready", operation, got.Digest().String(), health.ProcessGeneration,
						health.ActivationGeneration,
					),
				}, nil
			}
			lastErr = validateErr
		} else {
			lastErr = observeErr
		}
		select {
		case <-readyCtx.Done():
			return deployment.Proof{}, fmt.Errorf(
				"cc-notes helper: wait for deployment readiness: %w",
				errors.Join(readyCtx.Err(), lastErr),
			)
		case <-time.After(readinessPoll):
		}
	}
}

func (h productHooks) planForBuild(
	operation deployment.Operation,
	buildID string,
) (holder.DeploymentPlan, error) {
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return holder.DeploymentPlan{}, err
	}
	presentationRoot, err := PresentationRoot()
	if err != nil {
		return holder.DeploymentPlan{}, err
	}
	digest, err := runtimePolicyDigest()
	if err != nil {
		return holder.DeploymentPlan{}, err
	}
	return holder.NewDeploymentPlan(DeploymentPlanSpec(
		operation.Generation.Path, runtimeDirectory, presentationRoot, buildID, digest,
	))
}

func (h productHooks) servicePlanForOperation(operation deployment.Operation) (service.Plan, error) {
	return h.servicePlanForBuild(operation, h.buildID)
}

func (h productHooks) servicePlanForBuild(
	operation deployment.Operation,
	buildID string,
) (service.Plan, error) {
	plan, err := h.planForBuild(operation, buildID)
	if err != nil {
		return service.Plan{}, err
	}
	return service.NewPlan([]service.Agent{plan.Agent()})
}

func (h productHooks) runtimeTargetForOperation(operation deployment.Operation) (runtimeTarget, error) {
	return h.runtimeTargetForBuild(operation, h.buildID)
}

func (h productHooks) runtimeTargetForBuild(
	operation deployment.Operation,
	buildID string,
) (runtimeTarget, error) {
	plan, err := h.planForBuild(operation, buildID)
	if err != nil {
		return runtimeTarget{}, err
	}
	native, ok := plan.NativePresentation()
	if !ok {
		return runtimeTarget{}, errors.New("cc-notes helper: deployment plan has no native presentation")
	}
	return runtimeTarget{
		executable: plan.RuntimeExecutable(), socket: plan.Paths().Socket,
		buildID: plan.BuildID(), presentationRoot: native.PresentationRoot,
	}, nil
}

func exactPlanBuildID(operation deployment.Operation, plan service.Plan) (string, error) {
	agents := plan.Agents()
	if len(agents) != 1 {
		return "", errors.New("cc-notes helper: readiness plan must contain exactly one helper agent")
	}
	agent := agents[0]
	buildID := agent.Env["FUSEKIT_BUILD_ID"]
	if agent.Program != helperExecutablePath(operation.Generation.Path) || buildID == "" {
		return "", errors.New("cc-notes helper: readiness plan does not target the exact helper generation")
	}
	return buildID, nil
}

func helperExecutablePath(appPath string) string {
	return filepath.Join(appPath, "Contents", "MacOS", helperclient.ExecutableName)
}

func verifyInstalledGeneration(ctx context.Context, appPath, buildID string) (string, string, error) {
	plan, err := NewRuntimePlan(ctx, appPath, buildID)
	if err != nil {
		return "", "", err
	}
	library, digest, ok := plan.FUSELibrary()
	if !ok {
		return "", "", errors.New("cc-notes helper: installed app has no FUSE bundle")
	}
	return library, digest, nil
}

func observeRuntimeHealth(
	ctx context.Context,
	socket string,
) (health mountproto.RuntimeHealthResponse, resultErr error) {
	client, err := mountservice.NewClient(ctx, wire.ClientConfig{Dial: wire.UnixDialer(socket)})
	if err != nil {
		return mountproto.RuntimeHealthResponse{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	return client.RuntimeHealth(ctx)
}

func validateRuntimeTarget(health mountproto.RuntimeHealthResponse) error {
	if health.Protocol != mountproto.Version || health.Code != mountproto.ErrorCodeOk || health.Message != "" ||
		health.RuntimeBuild == "" || health.RuntimeProtocol != mountproto.RuntimeProtocolVersion ||
		health.RuntimePID <= 0 || health.ProcessGeneration == "" {
		return errors.New("cc-notes helper: prior runtime health has the wrong exact generation")
	}
	return nil
}

func validateRuntimeReadiness(target runtimeTarget, health mountproto.RuntimeHealthResponse) error {
	if err := validateRuntimeTarget(health); err != nil {
		return err
	}
	source, err := mountproto.NativeMountSource(target.presentationRoot)
	if err != nil {
		return err
	}
	proof := health.NativeMount
	if health.RuntimeBuild != target.buildID || health.ActivationGeneration == "" ||
		health.State != mountproto.RuntimeStateHealthy || health.Draining || health.Busy || !health.Ready ||
		health.ReadinessPhase != mountproto.ReadinessPhaseReady ||
		health.ReadinessStep != mountproto.ReadinessStepPublished ||
		health.NativePhase != mountproto.NativePhaseLive || proof == nil ||
		proof.PresentationRoot != target.presentationRoot || proof.Filesystem != mountproto.NativeMountFilesystem ||
		proof.Source != source || proof.RootReadEpoch == 0 ||
		health.BrokerPhase != mountproto.BrokerPhaseDisabled {
		return errors.New("cc-notes helper: runtime is not the exact healthy deployment activation")
	}
	return nil
}

func (h productHooks) proofDigest(kind string, operation deployment.Operation, details ...string) deployment.SHA256 {
	digest := sha256.New()
	values := make([]string, 0, 15+len(details))
	values = append(values,
		helperclient.DeploymentProofIdentity, kind, operation.ID,
		string(operation.Role), operation.PlanDigest.String(),
		operation.Generation.Path, operation.Generation.Release.Version,
		operation.Generation.Release.URL, operation.Generation.Release.SHA256.String(),
		operation.Generation.DesignatedRequirement, operation.Generation.CDHash,
		operation.Generation.BundleDigest.String(), operation.Generation.Device, operation.Generation.Inode,
		h.policyDigest.String(),
	)
	values = append(values, details...)
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	var result deployment.SHA256
	copy(result[:], digest.Sum(nil))
	return result
}
