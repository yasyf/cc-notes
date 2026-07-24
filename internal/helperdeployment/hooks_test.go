package helperdeployment

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/transportproto"
	"github.com/yasyf/fusekit/trustroles"
)

func TestRuntimeStopRequestUsesExactScopedAuthority(t *testing.T) {
	target := runtimeTarget{socket: filepath.Join(t.TempDir(), "runtime.sock")}
	request := runtimeStopRequest(strings64("ab"), target, "v1.2.3 (commit)")
	if request.OperationID != strings64("ab") || request.ExpectedRuntimeBuild != "v1.2.3 (commit)" ||
		request.ControlRole != trustroles.StopController ||
		request.RuntimeClientConfig.Client.WireBuild != transportproto.WireBuild ||
		request.RuntimeClientConfig.Client.Role != trustroles.StopController ||
		request.RuntimeClientConfig.Client.Dial == nil ||
		request.RuntimeClientConfig.NoProgressTimeout != holder.StandardReadinessContract().PreparationNoProgressTimeout() {
		t.Fatalf("request = %#v", request)
	}
}

func TestValidateRuntimeStopProofRejectsEveryInexactField(t *testing.T) {
	generation, err := proc.ParseOwnerGeneration("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	exact := runtimeStopProof{
		operationID:         "operation",
		target:              wire.RuntimeIdentity{RuntimeBuild: "build", ProcessGeneration: generation},
		processRecordDigest: proc.RecordDigest(sha256.Sum256([]byte("process"))),
		settlement:          service.StopSettlementGone,
		receiptDigest:       service.StopReceiptDigest(sha256.Sum256([]byte("receipt"))),
	}
	if err := validateRuntimeStopProof("operation", generation, "build", exact); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*runtimeStopProof){
		"operation":      func(proof *runtimeStopProof) { proof.operationID = "other" },
		"build":          func(proof *runtimeStopProof) { proof.target.RuntimeBuild = "other" },
		"generation":     func(proof *runtimeStopProof) { proof.target.ProcessGeneration = proc.OwnerGeneration{} },
		"process digest": func(proof *runtimeStopProof) { proof.processRecordDigest = proc.RecordDigest{} },
		"settlement":     func(proof *runtimeStopProof) { proof.settlement = 0 },
		"receipt digest": func(proof *runtimeStopProof) { proof.receiptDigest = service.StopReceiptDigest{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := exact
			mutate(&candidate)
			if err := validateRuntimeStopProof("operation", generation, "build", candidate); err == nil {
				t.Fatal("inexact stop proof was accepted")
			}
		})
	}
}

func TestExactPlanBuildIDRequiresCanonicalHelperAgent(t *testing.T) {
	app := filepath.Join(realTempDir(t), "CCNotesHelper.app")
	executable := filepath.Join(app, "Contents", "MacOS", helperclient.ExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := testServicePlan(t, executable, "v1.2.3")
	build, err := exactPlanBuildID(app, plan)
	if err != nil || build != "v1.2.3" {
		t.Fatalf("exactPlanBuildID = (%q, %v)", build, err)
	}
	wrong := testServicePlan(t, "/usr/bin/false", "v1.2.3")
	if _, err := exactPlanBuildID(app, wrong); err == nil {
		t.Fatal("exactPlanBuildID accepted a foreign executable")
	}
}

func TestValidateRuntimeReadinessRejectsInexactActivation(t *testing.T) {
	target := runtimeTarget{buildID: "v1.2.3", presentationRoot: t.TempDir()}
	exact := exactHealth(t, target)
	if err := validateRuntimeReadiness(target, exact); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*mountproto.RuntimeHealthResponse){
		"build":      func(health *mountproto.RuntimeHealthResponse) { health.RuntimeBuild = "other" },
		"generation": func(health *mountproto.RuntimeHealthResponse) { health.ProcessGeneration = "" },
		"activation": func(health *mountproto.RuntimeHealthResponse) { health.ActivationGeneration = "" },
		"busy":       func(health *mountproto.RuntimeHealthResponse) { health.Busy = true },
		"native":     func(health *mountproto.RuntimeHealthResponse) { health.NativeMount = nil },
		"broker":     func(health *mountproto.RuntimeHealthResponse) { health.BrokerPhase = mountproto.BrokerPhaseLive },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := exact
			native := *exact.NativeMount
			candidate.NativeMount = &native
			mutate(&candidate)
			if err := validateRuntimeReadiness(target, candidate); err == nil {
				t.Fatal("inexact runtime readiness was accepted")
			}
		})
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
		RuntimePID: 42, ProcessGeneration: "0102030405060708090a0b0c0d0e0f10", ActivationGeneration: "activation-1",
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
		LogPath: filepath.Join(realTempDir(t), "helper.log"), RestartPolicy: service.RestartAlways,
		LimitLoadToSessionType: service.SessionTypeAqua,
		Env:                    map[string]string{"FUSEKIT_BUILD_ID": buildID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func strings64(pair string) string {
	result := ""
	for range 32 {
		result += pair
	}
	return result
}
