package helperdeployment

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/version"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/mountproto"
)

type recordingStopper struct {
	result wire.StopResult
	err    error
	calls  int
	spec   service.StopControlSpec
}

type versionGatedStopper struct {
	recordingStopper
	incumbentBuild string
}

func (s *versionGatedStopper) StopRuntime(
	ctx context.Context,
	spec service.StopControlSpec,
) (wire.StopResult, error) {
	if spec.Intent == wire.StopIntentUpgrade && !version.Newer(spec.RuntimeBuild, s.incumbentBuild) {
		return wire.StopResult{
			ProcessGeneration: s.result.ProcessGeneration,
			RuntimeBuild:      s.incumbentBuild, RuntimeProtocol: s.result.RuntimeProtocol,
		}, nil
	}
	return s.recordingStopper.StopRuntime(ctx, spec)
}

func (s *recordingStopper) StopRuntime(
	_ context.Context,
	spec service.StopControlSpec,
) (wire.StopResult, error) {
	s.calls++
	s.spec = spec
	return s.result, s.err
}

func TestRuntimeQuiesceStopsExactObservedGeneration(t *testing.T) {
	operation := testQuiesceOperation(t, wire.StopIntentUpgrade)
	target := runtimeTarget{
		executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock",
		buildID: "v0.45.0", presentationRoot: "/presentation",
	}
	health := exactHealth(t, target)
	health.RuntimeBuild = "v0.44.0"
	stopper := &versionGatedStopper{
		incumbentBuild: health.RuntimeBuild,
		recordingStopper: recordingStopper{result: wire.StopResult{
			Stopped: true, ProcessGeneration: health.ProcessGeneration,
			RuntimeBuild: health.RuntimeBuild, RuntimeProtocol: int(health.RuntimeProtocol),
		}},
	}
	hooks := productHooks{
		buildID: target.buildID, policyDigest: sha256.Sum256([]byte("policy")),
		target: func(deployment.Operation) (runtimeTarget, error) { return target, nil },
		observe: func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
			return health, nil
		},
	}
	proof, err := hooks.runtimeQuiesce(t.Context(), stopper, operation)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Role != operation.Role || proof.ProcessGeneration != health.ProcessGeneration ||
		proof.Digest == (deployment.SHA256{}) {
		t.Fatalf("proof = %#v", proof)
	}
	if stopper.calls != 1 || stopper.spec.Executable != target.executable ||
		stopper.spec.TargetProcessGeneration != health.ProcessGeneration ||
		stopper.spec.RuntimeBuild != target.buildID || stopper.spec.Intent != wire.StopIntentUpgrade ||
		!reflect.DeepEqual(stopper.spec.Args, []string{"--fusekit-stop-control-v1"}) ||
		stopper.spec.Role != helperclient.DeploymentStopRole {
		t.Fatalf("stop spec = %#v", stopper.spec)
	}
}

func TestRuntimeQuiesceUsesInvokingBuildForNonUpgradeIntent(t *testing.T) {
	for _, intent := range []wire.StopIntent{wire.StopIntentRestart, wire.StopIntentUninstall} {
		t.Run(string(intent), func(t *testing.T) {
			operation := testQuiesceOperation(t, intent)
			target := runtimeTarget{
				executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock",
				buildID: "v0.46.0", presentationRoot: "/presentation",
			}
			health := exactHealth(t, runtimeTarget{
				executable: target.executable, socket: target.socket,
				buildID: "v0.45.0", presentationRoot: target.presentationRoot,
			})
			stopper := &recordingStopper{result: wire.StopResult{
				Stopped: true, ProcessGeneration: health.ProcessGeneration,
				RuntimeBuild: health.RuntimeBuild, RuntimeProtocol: int(health.RuntimeProtocol),
			}}
			hooks := productHooks{
				buildID: target.buildID, policyDigest: sha256.Sum256([]byte("policy")),
				target: func(deployment.Operation) (runtimeTarget, error) { return target, nil },
				observe: func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
					return health, nil
				},
			}
			if _, err := hooks.runtimeQuiesce(t.Context(), stopper, operation); err != nil {
				t.Fatal(err)
			}
			if stopper.spec.Intent != intent || stopper.spec.RuntimeBuild != target.buildID {
				t.Fatalf("stop spec = %#v", stopper.spec)
			}
		})
	}
}

func TestRuntimeQuiesceRejectsUnknownIntent(t *testing.T) {
	operation := testQuiesceOperation(t, wire.StopIntent("unknown"))
	target := runtimeTarget{
		executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock",
		buildID: "v0.45.0", presentationRoot: "/presentation",
	}
	health := exactHealth(t, target)
	hooks := productHooks{
		buildID: target.buildID, policyDigest: sha256.Sum256([]byte("policy")),
		target: func(deployment.Operation) (runtimeTarget, error) { return target, nil },
		observe: func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
			return health, nil
		},
	}
	stopper := &recordingStopper{}
	if _, err := hooks.runtimeQuiesce(t.Context(), stopper, operation); err == nil {
		t.Fatal("runtime quiesce accepted unknown intent")
	}
	if stopper.calls != 0 {
		t.Fatalf("stop calls = %d, want 0", stopper.calls)
	}
}

func TestInstalledProofUsesExactGenerationAndFUSEProof(t *testing.T) {
	operation := testOperation(t)
	restoreOperation := operation
	restoreOperation.Role = deployment.ProofPriorRestore
	var gotPath, gotBuild string
	hooks := productHooks{
		buildID: "v0.45.0", policyDigest: sha256.Sum256([]byte("policy")),
		verifyInstalled: func(_ context.Context, path, build string) (string, string, error) {
			gotPath, gotBuild = path, build
			return filepath.Join(path, "Contents", "Frameworks", "libfuse-t.dylib"), strings64("ab"), nil
		},
	}
	installed, err := hooks.postInstallProof(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := hooks.priorAppRestoreProof(t.Context(), restoreOperation)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != operation.Generation.Path || gotBuild != hooks.buildID ||
		installed.Digest == (deployment.SHA256{}) || restored.Digest == (deployment.SHA256{}) ||
		installed.Role != deployment.ProofPostInstall || restored.Role != deployment.ProofPriorRestore ||
		installed.PlanDigest != (deployment.SHA256{}) || restored.PlanDigest != (deployment.SHA256{}) ||
		installed.Digest == restored.Digest {
		t.Fatalf("installed/restored proofs = %#v / %#v", installed, restored)
	}
}

func TestReadinessRequiresExactPlanAndHealth(t *testing.T) {
	operation := testOperation(t)
	plan := testServicePlan(t, bundleExecutable(operation.Generation.Path), "v0.45.0")
	operation.Role = deployment.ProofCandidateReady
	operation.PlanDigest = deployment.SHA256(plan.Digest())
	target := runtimeTarget{
		executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock",
		buildID: "v0.45.0", presentationRoot: t.TempDir(),
	}
	hooks := productHooks{
		buildID: "v0.45.0", policyDigest: sha256.Sum256([]byte("policy")),
		servicePlanBuild: func(deployment.Operation, string) (service.Plan, error) { return plan, nil },
		targetBuild:      func(deployment.Operation, string) (runtimeTarget, error) { return target, nil },
	}
	health := exactHealth(t, target)
	hooks.observe = func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
		return health, nil
	}
	proof, err := hooks.readiness(t.Context(), operation, plan)
	if err != nil || proof.Role != operation.Role || proof.PlanDigest != operation.PlanDigest ||
		proof.Digest == (deployment.SHA256{}) {
		t.Fatalf("readiness = (%#v, %v)", proof, err)
	}
	wrong := testServicePlan(t, "/usr/bin/false", "v0.45.0")
	if _, err := hooks.readiness(t.Context(), operation, wrong); err == nil {
		t.Fatal("readiness accepted the wrong service plan")
	}
}

func TestReadinessAcceptsStoredPriorBuildPlan(t *testing.T) {
	operation := testOperation(t)
	prior := testServicePlan(t, bundleExecutable(operation.Generation.Path), "v0.44.0")
	operation.Role = deployment.ProofPriorReady
	operation.PlanDigest = deployment.SHA256(prior.Digest())
	target := runtimeTarget{
		executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock",
		buildID: "v0.44.0", presentationRoot: t.TempDir(),
	}
	hooks := productHooks{
		buildID: "v0.45.0", policyDigest: sha256.Sum256([]byte("policy")),
		servicePlanBuild: func(deployment.Operation, string) (service.Plan, error) { return prior, nil },
		targetBuild:      func(deployment.Operation, string) (runtimeTarget, error) { return target, nil },
	}
	health := exactHealth(t, target)
	hooks.observe = func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
		return health, nil
	}
	proof, err := hooks.readiness(t.Context(), operation, prior)
	if err != nil || proof.Role != operation.Role || proof.PlanDigest != operation.PlanDigest ||
		proof.Digest == (deployment.SHA256{}) {
		t.Fatalf("prior readiness = (%#v, %v)", proof, err)
	}
}

func TestRuntimeQuiesceProvesAbsenceByExactExecutableInventory(t *testing.T) {
	operation := testQuiesceOperation(t, wire.StopIntentUninstall)
	target := runtimeTarget{executable: bundleExecutable(operation.Generation.Path), socket: "/stale.sock"}
	want := errors.New("connection refused")
	hooks := productHooks{
		policyDigest: sha256.Sum256([]byte("policy")),
		target:       func(deployment.Operation) (runtimeTarget, error) { return target, nil },
		observe: func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
			return mountproto.RuntimeHealthResponse{}, want
		},
		identities: func(path string) ([]proc.Identity, error) {
			if path != target.executable {
				t.Fatalf("inventory path = %q", path)
			}
			return nil, nil
		},
	}
	proof, err := hooks.runtimeQuiesce(t.Context(), &recordingStopper{}, operation)
	if err != nil || proof.Role != operation.Role || !proof.Absent || proof.ProcessGeneration != "" ||
		proof.Digest == (deployment.SHA256{}) {
		t.Fatalf("absent proof = (%#v, %v)", proof, err)
	}
	hooks.identities = func(string) ([]proc.Identity, error) {
		return []proc.Identity{{PID: os.Getpid()}}, nil
	}
	if _, err := hooks.runtimeQuiesce(t.Context(), &recordingStopper{}, operation); !errors.Is(err, want) {
		t.Fatalf("live process error = %v, want %v", err, want)
	}
}

func TestValidateRuntimeReadinessRejectsInexactActivation(t *testing.T) {
	target := runtimeTarget{buildID: "v0.45.0", presentationRoot: t.TempDir()}
	exact := exactHealth(t, target)
	for name, mutate := range map[string]func(*mountproto.RuntimeHealthResponse){
		"build":      func(h *mountproto.RuntimeHealthResponse) { h.RuntimeBuild = "other" },
		"generation": func(h *mountproto.RuntimeHealthResponse) { h.ProcessGeneration = "" },
		"activation": func(h *mountproto.RuntimeHealthResponse) { h.ActivationGeneration = "" },
		"busy":       func(h *mountproto.RuntimeHealthResponse) { h.Busy = true },
		"native":     func(h *mountproto.RuntimeHealthResponse) { h.NativeMount = nil },
		"broker":     func(h *mountproto.RuntimeHealthResponse) { h.BrokerPhase = mountproto.BrokerPhaseLive },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := exact
			native := *exact.NativeMount
			candidate.NativeMount = &native
			mutate(&candidate)
			if err := validateRuntimeReadiness(target, candidate); err == nil {
				t.Fatal("inexact health was accepted")
			}
		})
	}
}

func TestRuntimeQuiesceReturnsOperationalFailure(t *testing.T) {
	want := errors.New("stop failed")
	operation := testQuiesceOperation(t, wire.StopIntentRestart)
	target := runtimeTarget{executable: bundleExecutable(operation.Generation.Path), socket: "/runtime/helper.sock"}
	health := exactHealth(t, runtimeTarget{buildID: "v0.45.0", presentationRoot: t.TempDir()})
	hooks := productHooks{
		policyDigest: sha256.Sum256([]byte("policy")),
		target:       func(deployment.Operation) (runtimeTarget, error) { return target, nil },
		observe: func(context.Context, string) (mountproto.RuntimeHealthResponse, error) {
			return health, nil
		},
	}
	if _, err := hooks.runtimeQuiesce(t.Context(), &recordingStopper{err: want}, operation); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestProofDigestBindsRolePlanAndExactGeneration(t *testing.T) {
	hooks := productHooks{policyDigest: sha256.Sum256([]byte("policy"))}
	operation := testOperation(t)
	base := hooks.proofDigest("proof", operation)
	mutations := map[string]func(*deployment.Operation){
		"role":          func(op *deployment.Operation) { op.Role = deployment.ProofPriorRestore },
		"plan digest":   func(op *deployment.Operation) { op.PlanDigest = sha256.Sum256([]byte("plan")) },
		"cdhash":        func(op *deployment.Operation) { op.Generation.CDHash = "different" },
		"bundle digest": func(op *deployment.Operation) { op.Generation.BundleDigest = sha256.Sum256([]byte("other")) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := operation
			mutate(&candidate)
			if got := hooks.proofDigest("proof", candidate); got == base {
				t.Fatalf("proof digest did not bind %s", name)
			}
		})
	}
}

func testQuiesceOperation(t *testing.T, intent wire.StopIntent) deployment.RuntimeQuiesceOperation {
	t.Helper()
	operation := testOperation(t)
	return deployment.RuntimeQuiesceOperation{
		ID: operation.ID, Generation: operation.Generation, Intent: intent, Role: deployment.ProofPriorRuntime,
	}
}

func testOperation(t *testing.T) deployment.Operation {
	t.Helper()
	app := filepath.Join(canonicalTestDir(t), "Applications", "CCNotesHelper.app")
	executable := bundleExecutable(app)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	return deployment.Operation{
		ID: "0123456789abcdef0123456789abcdef", Role: deployment.ProofPostInstall,
		Generation: deployment.CanonicalGeneration{
			Path: app,
			Release: deployment.Release{
				Version: "0.45.0", URL: "https://example.test/helper.zip",
				SHA256: sha256.Sum256([]byte("release")),
			},
			DesignatedRequirement: "designated => helper", CDHash: "0123456789abcdef0123456789abcdef01234567",
			BundleDigest: sha256.Sum256([]byte("bundle")), Device: "1", Inode: "2",
		},
	}
}

func exactHealth(t *testing.T, target runtimeTarget) mountproto.RuntimeHealthResponse {
	t.Helper()
	source, err := mountproto.NativeMountSource(target.presentationRoot)
	if err != nil {
		t.Fatal(err)
	}
	return mountproto.RuntimeHealthResponse{
		Protocol: mountproto.Version, Code: mountproto.ErrorCodeOk,
		RuntimeBuild: target.buildID, RuntimeProtocol: mountproto.RuntimeProtocolVersion,
		RuntimePID: 42, ProcessGeneration: "process-1", ActivationGeneration: "activation-1",
		State: mountproto.RuntimeStateHealthy, Ready: true,
		ReadinessPhase: mountproto.ReadinessPhaseReady, ReadinessStep: mountproto.ReadinessStepPublished,
		NativePhase: mountproto.NativePhaseLive,
		NativeMount: &mountproto.NativeMountProof{
			PresentationRoot: target.presentationRoot, Filesystem: mountproto.NativeMountFilesystem,
			Source: source, RootReadEpoch: 1,
		},
		BrokerPhase: mountproto.BrokerPhaseDisabled,
	}
}

func testServicePlan(t *testing.T, program, buildID string) service.Plan {
	t.Helper()
	plan, err := service.NewPlan([]service.Agent{{
		Label: "com.yasyf.cc-notes.helper.fusekit", Program: program,
		LogPath: filepath.Join(t.TempDir(), "helper.log"), RestartPolicy: service.RestartAlways,
		LimitLoadToSessionType: service.SessionTypeAqua,
		Env:                    map[string]string{"FUSEKIT_BUILD_ID": buildID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func bundleExecutable(app string) string {
	return filepath.Join(app, "Contents", "MacOS", helperclient.ExecutableName)
}

func strings64(pair string) string {
	result := ""
	for range 32 {
		result += pair
	}
	return result
}
