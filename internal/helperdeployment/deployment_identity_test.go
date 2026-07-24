package helperdeployment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/trust"
)

func TestConsumerBuildForExecutableHashesExactBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cc-notes")
	payload := []byte("exact updater bytes")
	if err := os.WriteFile(path, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	want := consumerBuildDomain + hex.EncodeToString(digest[:])
	got, err := consumerBuildForExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("consumer build = %q, want %q", got, want)
	}
}

func TestConsumerBuildForExecutableRejectsNonExecutableInput(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"relative":   "cc-notes",
		"directory":  dir,
		"plain file": plain,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := consumerBuildForExecutable(path); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDeploymentIdentityUsesStartupCacheAndFailsClosed(t *testing.T) {
	originalBuild, originalBuildErr := startupConsumerBuild, startupConsumerBuildErr
	originalPolicy, originalPolicyErr := startupPolicyDigest, startupPolicyDigestErr
	t.Cleanup(func() {
		startupConsumerBuild, startupConsumerBuildErr = originalBuild, originalBuildErr
		startupPolicyDigest, startupPolicyDigestErr = originalPolicy, originalPolicyErr
	})
	wantDigest := deployment.SHA256(sha256.Sum256([]byte("policy")))
	startupConsumerBuild, startupConsumerBuildErr = "cached-build", nil
	startupPolicyDigest, startupPolicyDigestErr = wantDigest, nil
	build, digest, err := DeploymentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if build != "cached-build" || digest != wantDigest {
		t.Fatalf("identity = (%q, %x), want (%q, %x)", build, digest, "cached-build", wantDigest)
	}

	unavailable := errors.New("updater unavailable")
	startupConsumerBuild, startupConsumerBuildErr = "", unavailable
	build, digest, err = DeploymentIdentity()
	if !errors.Is(err, unavailable) || build != "" || digest != (deployment.SHA256{}) {
		t.Fatalf("failed identity = (%q, %x, %v)", build, digest, err)
	}

	invalidPolicy := errors.New("policy unavailable")
	startupConsumerBuild, startupConsumerBuildErr = "cached-build", nil
	startupPolicyDigest, startupPolicyDigestErr = deployment.SHA256{}, invalidPolicy
	build, digest, err = DeploymentIdentity()
	if !errors.Is(err, invalidPolicy) || build != "" || digest != (deployment.SHA256{}) {
		t.Fatalf("failed policy identity = (%q, %x, %v)", build, digest, err)
	}
}

func TestDeploymentPolicyJSONAndDigestAreExact(t *testing.T) {
	payload, err := deploymentPolicyJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded deploymentPolicy
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, canonical) {
		t.Fatalf("policy JSON is not canonical:\n got %s\nwant %s", payload, canonical)
	}
	wantBudgets := deploymentRuntimeBudgetPolicy{
		NativeReadinessTimeout:  helpercontract.RuntimeNativeReadinessTimeout,
		CatalogReadinessTimeout: helpercontract.RuntimeCatalogReadinessTimeout,
		CatalogOperationTimeout: helpercontract.RuntimeCatalogOperationTimeout,
		ShutdownTimeout:         helpercontract.RuntimeShutdownTimeout,
	}
	if decoded.Runtime.Budgets != wantBudgets {
		t.Fatalf("runtime budgets = %+v, want %+v", decoded.Runtime.Budgets, wantBudgets)
	}
	wantRuntimePolicy, err := (trust.Requirement{
		TeamID: helperclient.TeamID, SigningIdentifier: helperclient.BundleID,
	}).ValidationDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Runtime.State.RuntimePolicyDigest; got != hex.EncodeToString(wantRuntimePolicy[:]) {
		t.Fatalf("runtime policy digest = %q, want %x", got, wantRuntimePolicy)
	}
	if wantBudgets.NativeReadinessTimeout <= 0 || wantBudgets.CatalogReadinessTimeout <= 0 ||
		wantBudgets.CatalogOperationTimeout <= 0 ||
		wantBudgets.ShutdownTimeout <= 0 || wantBudgets.ShutdownTimeout >= time.Minute {
		t.Fatalf("runtime budgets are not explicit bounded positive values: %+v", wantBudgets)
	}
	digest, err := makeDeploymentPolicyDigest()
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "196114742940d7e926cf55eb7c82d97cb80354c204a2adcab899beb28ca6e5c8"
	got := hex.EncodeToString(digest[:])
	if got != wantDigest {
		t.Fatalf("policy digest = %s, want %s", got, wantDigest)
	}
	if len(got) != sha256.Size*2 || strings.ToLower(got) != got {
		t.Fatalf("policy digest is not canonical lowercase SHA-256: %q", got)
	}
}
