package helperdeployment

import (
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/trust"
	"github.com/yasyf/fusekit/holder"
)

func TestRuntimeAndDeploymentSpecsShareOneFixedContract(t *testing.T) {
	verifier := new(holder.FUSEVerifier)
	runtime := RuntimePlanSpec(
		filepath.Join("/Users/example", "Applications", "CCNotesHelper.app"), "/runtime", "/presentation", "v0.45.0", verifier,
	)
	digest := codeidentity.PolicyDigest{1}
	deployment := DeploymentPlanSpec(
		runtime.Application.AppPath, runtime.RuntimeDirectory, runtime.Native.PresentationRoot, runtime.BuildID, digest,
	)
	if runtime.Application != deployment.Application || runtime.Application.BundleID != helperclient.BundleID ||
		runtime.Application.TeamID != helperclient.TeamID ||
		runtime.Application.Runtime.ExecutableName != helperclient.ExecutableName ||
		deployment.Native == nil || deployment.Native.PresentationRoot != runtime.Native.PresentationRoot ||
		deployment.BuildID != runtime.BuildID || deployment.Readiness != runtime.Readiness ||
		!deployment.SourceCapable || deployment.RuntimePolicyDigest != digest {
		t.Fatalf("runtime/deployment specs = %#v / %#v", runtime, deployment)
	}
}

func TestRuntimePolicyDigestMatchesSignedHelperRequirement(t *testing.T) {
	got, err := runtimePolicyDigest()
	if err != nil {
		t.Fatal(err)
	}
	want, err := (trust.Requirement{
		TeamID: helperclient.TeamID, SigningIdentifier: helperclient.BundleID,
	}).ValidationDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("runtime policy digest = %x, want %x", got, want)
	}
}
