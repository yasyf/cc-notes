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
	"github.com/yasyf/fusekit/transportproto"
	"github.com/yasyf/fusekit/trustroles"
)

const readinessPoll = 100 * time.Millisecond

type productHooks struct {
	buildID          string
	policyDigest     deployment.SHA256
	verifyInstalled  func(context.Context, string, string) (string, string, error)
	servicePlanBuild func(string, string) (service.Plan, error)
	targetBuild      func(string, string) (runtimeTarget, error)
	observe          func(context.Context, runtimeTarget) (mountproto.RuntimeHealthResponse, error)
	stop             func(context.Context, deployment.RuntimeStopper, runtimeTarget, service.StopRuntimeRequest) (runtimeStopProof, error)
	identities       func(string) ([]proc.Identity, error)
}

func newProductHooks(buildID string, policyDigest deployment.SHA256) productHooks {
	hooks := productHooks{
		buildID: buildID, policyDigest: policyDigest,
		verifyInstalled: verifyInstalledGeneration,
		observe:         observeRuntime,
		stop: func(
			ctx context.Context,
			stopper deployment.RuntimeStopper,
			_ runtimeTarget,
			request service.StopRuntimeRequest,
		) (runtimeStopProof, error) {
			receipt, err := stopper.StopRuntime(ctx, request)
			if err != nil {
				return runtimeStopProof{}, err
			}
			return runtimeStopProof{
				operationID: receipt.OperationID(), target: receipt.Target(),
				processRecordDigest: receipt.ProcessRecordDigest(), settlement: receipt.Settlement(),
				receiptDigest: receipt.Digest(),
			}, nil
		},
		identities: proc.ExecutableIdentities,
	}
	hooks.servicePlanBuild = hooks.servicePlanForBuild
	hooks.targetBuild = hooks.runtimeTargetForBuild
	return hooks
}

type runtimeTarget struct {
	executable       string
	socket           string
	buildID          string
	presentationRoot string
}

type runtimeStopProof struct {
	operationID         string
	target              wire.RuntimeIdentity
	processRecordDigest proc.RecordDigest
	settlement          service.StopSettlement
	receiptDigest       service.StopReceiptDigest
}

func observeRuntime(
	ctx context.Context,
	target runtimeTarget,
) (health mountproto.RuntimeHealthResponse, resultErr error) {
	client, err := mountservice.NewClient(ctx, wire.ClientConfig{
		Dial: wire.UnixDialer(target.socket), WireBuild: transportproto.WireBuild,
		Role: trustroles.ReadinessController,
	})
	if err != nil {
		return mountproto.RuntimeHealthResponse{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, client.Close()) }()
	return client.RuntimeHealth(ctx)
}

func (h productHooks) runtimeQuiesce(
	ctx context.Context,
	stopper deployment.RuntimeStopper,
	operation deployment.DeactivateInstalledOperation,
) (deployment.RuntimeProof, error) {
	activation := operation.Activation()
	generation := activation.Generation()
	plan := activation.Plan()
	buildID, err := exactPlanBuildID(generation.Path(), plan)
	if err != nil {
		return deployment.RuntimeProof{}, err
	}
	target, err := h.targetBuild(generation.Path(), buildID)
	if err != nil {
		return deployment.RuntimeProof{}, err
	}
	health, observeErr := h.observe(ctx, target)
	if observeErr != nil {
		identities, inventoryErr := h.identities(target.executable)
		if inventoryErr != nil {
			return deployment.RuntimeProof{}, fmt.Errorf(
				"cc-notes helper: prove prior runtime absence: %w", errors.Join(observeErr, inventoryErr),
			)
		}
		if len(identities) != 0 {
			return deployment.RuntimeProof{}, fmt.Errorf(
				"cc-notes helper: prior runtime endpoint is unavailable while %d exact process(es) remain: %w",
				len(identities), observeErr,
			)
		}
		return deployment.NewRuntimeProof(true, proc.OwnerGeneration{}, h.proofDigest(
			"runtime-absent", operation.OperationID(), generation, plan, target.executable,
		))
	}
	if err := validateRuntimeTarget(health); err != nil {
		return deployment.RuntimeProof{}, err
	}
	processGeneration, err := proc.ParseOwnerGeneration(health.ProcessGeneration)
	if err != nil {
		return deployment.RuntimeProof{}, fmt.Errorf("cc-notes helper: parse observed runtime generation: %w", err)
	}
	request := runtimeStopRequest(operation.OperationID(), target, health.RuntimeBuild)
	receipt, err := h.stop(ctx, stopper, target, request)
	if err != nil {
		return deployment.RuntimeProof{}, fmt.Errorf("cc-notes helper: settle prior runtime: %w", err)
	}
	if err := validateRuntimeStopProof(operation.OperationID(), processGeneration, health.RuntimeBuild, receipt); err != nil {
		return deployment.RuntimeProof{}, err
	}
	return deployment.NewRuntimeProof(true, processGeneration, h.proofDigest(
		"runtime-quiesced", operation.OperationID(), generation, plan,
		health.RuntimeBuild, health.ProcessGeneration,
		fmt.Sprintf("%x", receipt.processRecordDigest), fmt.Sprintf("%x", receipt.receiptDigest),
	))
}

func runtimeStopRequest(operationID string, target runtimeTarget, buildID string) service.StopRuntimeRequest {
	return service.StopRuntimeRequest{
		OperationID: operationID,
		RuntimeClientConfig: wire.RuntimeClientConfig{
			Client: wire.ClientConfig{
				Dial: wire.UnixDialer(target.socket), WireBuild: transportproto.WireBuild,
				Role: trustroles.StopController,
			},
			NoProgressTimeout: holder.StandardReadinessContract().PreparationNoProgressTimeout(),
		},
		ExpectedRuntimeBuild: buildID,
		ControlRole:          trustroles.StopController,
	}
}

func validateRuntimeStopProof(
	operationID string,
	processGeneration proc.OwnerGeneration,
	buildID string,
	receipt runtimeStopProof,
) error {
	if receipt.operationID != operationID || receipt.target.ProcessGeneration != processGeneration ||
		receipt.target.RuntimeBuild != buildID ||
		receipt.processRecordDigest == (proc.RecordDigest{}) || receipt.settlement != service.StopSettlementGone ||
		receipt.receiptDigest == (service.StopReceiptDigest{}) {
		return errors.New("cc-notes helper: stop result does not match the observed runtime generation")
	}
	return nil
}

func (h productHooks) readiness(
	ctx context.Context,
	operation deployment.InstalledOperation,
) (deployment.ReadinessProof, error) {
	generation := operation.Generation()
	got := operation.Plan()
	buildID, err := exactPlanBuildID(generation.Path(), got)
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	want, err := h.servicePlanBuild(generation.Path(), buildID)
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	if got.Digest() != want.Digest() || !reflect.DeepEqual(got.Agents(), want.Agents()) {
		return deployment.ReadinessProof{}, errors.New("cc-notes helper: readiness plan is not the exact helper plan")
	}
	library, fuseDigest, err := h.verifyInstalled(ctx, generation.Path(), buildID)
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	if library == "" || fuseDigest == "" {
		return deployment.ReadinessProof{}, errors.New("cc-notes helper: installed app has no exact FUSE proof")
	}
	target, err := h.targetBuild(generation.Path(), buildID)
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, holder.StandardReadinessContract().ObservationTimeout())
	defer cancel()
	var lastErr error
	for {
		health, observeErr := h.observe(readyCtx, target)
		if observeErr == nil {
			validateErr := validateRuntimeReadiness(target, health)
			if validateErr == nil {
				processGeneration, parseErr := proc.ParseOwnerGeneration(health.ProcessGeneration)
				if parseErr != nil {
					return deployment.ReadinessProof{}, parseErr
				}
				return deployment.NewReadinessProof(
					health.RuntimeBuild, processGeneration,
					h.proofDigest(
						"runtime-ready", operation.OperationID(), generation, got,
						library, fuseDigest, health.ProcessGeneration, health.ActivationGeneration,
					),
				)
			}
			lastErr = validateErr
		} else {
			lastErr = observeErr
		}
		select {
		case <-readyCtx.Done():
			return deployment.ReadinessProof{}, fmt.Errorf(
				"cc-notes helper: wait for deployment readiness: %w", errors.Join(readyCtx.Err(), lastErr),
			)
		case <-time.After(readinessPoll):
		}
	}
}

func (h productHooks) planForBuild(appPath, buildID string) (holder.DeploymentPlan, error) {
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
		appPath, runtimeDirectory, presentationRoot, buildID, digest,
	))
}

func (h productHooks) servicePlanForBuild(appPath, buildID string) (service.Plan, error) {
	plan, err := h.planForBuild(appPath, buildID)
	if err != nil {
		return service.Plan{}, err
	}
	return service.NewPlan([]service.Agent{plan.Agent()})
}

func (h productHooks) runtimeTargetForBuild(appPath, buildID string) (runtimeTarget, error) {
	plan, err := h.planForBuild(appPath, buildID)
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

func exactPlanBuildID(appPath string, plan service.Plan) (string, error) {
	agents := plan.Agents()
	if len(agents) != 1 {
		return "", errors.New("cc-notes helper: readiness plan must contain exactly one helper agent")
	}
	agent := agents[0]
	buildID := agent.Env["FUSEKIT_BUILD_ID"]
	if agent.Program != helperExecutablePath(appPath) || buildID == "" {
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

func validateRuntimeTarget(health mountproto.RuntimeHealthResponse) error {
	if health.Protocol != mountproto.Version || health.Code != mountproto.ErrorCodeOk || health.Message != "" ||
		health.RuntimeBuild == "" || health.RuntimeProtocol != mountproto.RuntimeProtocolVersion ||
		health.RuntimePID <= 0 || health.ProcessGeneration == "" {
		return errors.New("cc-notes helper: prior runtime health has the wrong exact generation")
	}
	if _, err := proc.ParseOwnerGeneration(health.ProcessGeneration); err != nil {
		return errors.New("cc-notes helper: prior runtime health has a malformed process generation")
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

func (h productHooks) proofDigest(
	kind, operationID string,
	generation deployment.InstalledAttestation,
	plan service.Plan,
	details ...string,
) deployment.SHA256 {
	digest := sha256.New()
	values := make([]string, 0, 14+len(details))
	values = append(values,
		DeploymentProofIdentity, kind, operationID, plan.Digest().String(),
		generation.Path(), generation.Version(), generation.TeamID(), generation.SigningIdentifier(),
		generation.DesignatedRequirement(), generation.CDHash(), generation.BundleDigest().String(),
		generation.EntitlementsDigest().String(), h.policyDigest.String(),
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
