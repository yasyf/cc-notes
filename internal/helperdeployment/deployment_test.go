package helperdeployment

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/bundle"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/fusekit/mountproto"
)

type recordingDeployer struct {
	receipt          serviceDeployment
	err              error
	calls            int
	config           deployment.Config
	configs          []deployment.Config
	complete         bool
	validateProofs   bool
	deactivation     deactivationResult
	deactivateErr    error
	deactivateCalls  int
	deactivateConfig deployment.DeactivateConfig
}

func (d *recordingDeployer) Deactivate(
	_ context.Context,
	config deployment.DeactivateConfig,
) (deactivationResult, error) {
	d.deactivateCalls++
	d.deactivateConfig = config
	return d.deactivation, d.deactivateErr
}

func (d *recordingDeployer) Deploy(
	ctx context.Context,
	config deployment.Config,
) (serviceDeployment, error) {
	d.calls++
	d.config = config
	d.configs = append(d.configs, config)
	if d.complete && d.receipt.current.Path != "" {
		if d.receipt.operationID == "" {
			d.receipt.operationID = strings.Repeat("a", 32)
		}
		plan, err := config.BuildPlan(ctx, deployment.Operation{
			ID: d.receipt.operationID, Generation: d.receipt.current,
		})
		if err != nil {
			return serviceDeployment{}, err
		}
		d.receipt.plan = plan
		d.receipt.activationPlan = plan
		if d.validateProofs {
			postOperation := deployment.Operation{
				ID: d.receipt.operationID, Generation: d.receipt.current, Role: deployment.ProofPostInstall,
			}
			post, err := config.PostInstallProof(ctx, postOperation)
			if err != nil {
				return serviceDeployment{}, err
			}
			if post.Role != postOperation.Role || post.PlanDigest != (deployment.SHA256{}) ||
				post.Digest == (deployment.SHA256{}) {
				return serviceDeployment{}, errors.New("post-install callback returned an unbound proof")
			}
			readyOperation := deployment.Operation{
				ID: d.receipt.operationID, Generation: d.receipt.current, Role: deployment.ProofCandidateReady,
				PlanDigest: deployment.SHA256(plan.Digest()),
			}
			ready, err := config.Readiness(ctx, readyOperation, plan)
			if err != nil {
				return serviceDeployment{}, err
			}
			if ready.Role != readyOperation.Role || ready.PlanDigest != readyOperation.PlanDigest ||
				ready.Digest == (deployment.SHA256{}) {
				return serviceDeployment{}, errors.New("readiness callback returned an unbound proof")
			}
		}
	}
	return d.receipt, d.err
}

func TestHelperReleasePinsExactVersionURLAndDigest(t *testing.T) {
	setHelperRelease(t, "v0.45.0", "0.45.0", strings.Repeat("ab", 32))
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.45.0" ||
		release.URL != "https://github.com/yasyf/cc-notes/releases/download/v0.45.0/cc-notes-helper-v0.45.0-darwin.zip" ||
		release.SHA256.String() != strings.Repeat("ab", 32) {
		t.Fatalf("release = %#v", release)
	}
}

func TestHelperReleaseRejectsInexactMetadata(t *testing.T) {
	for _, test := range []struct {
		release string
		helper  string
		digest  string
	}{
		{release: "v0.45.0", helper: "", digest: strings.Repeat("ab", 32)},
		{release: "v0.45.0", helper: " 0.45.0", digest: strings.Repeat("ab", 32)},
		{release: "v0.45.0", helper: "0.44.0", digest: strings.Repeat("ab", 32)},
		{release: "dev", helper: "0.45.0", digest: strings.Repeat("ab", 32)},
		{release: "v0.45", helper: "0.45.0", digest: strings.Repeat("ab", 32)},
		{release: "v0.45.0-rc..1", helper: "0.45.0", digest: strings.Repeat("ab", 32)},
		{release: "v0.45.0", helper: "0.45.0", digest: ""},
	} {
		setHelperRelease(t, test.release, test.helper, test.digest)
		if _, err := helperRelease(); err == nil {
			t.Fatalf("helperRelease accepted release=%q helper=%q digest=%q", test.release, test.helper, test.digest)
		}
	}
}

func TestHelperReleaseAcceptsPrereleaseTagWithSameMarketingVersion(t *testing.T) {
	setHelperRelease(t, "v0.45.0-rc.1", "0.45.0", strings.Repeat("ab", 32))
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.45.0" || !strings.Contains(release.URL, "/v0.45.0-rc.1/") {
		t.Fatalf("release = %#v", release)
	}
}

func TestInstallServiceDeploysExactGeneration(t *testing.T) {
	setHelperRelease(t, "v0.45.0", "0.45.0", strings.Repeat("cd", 32))
	home := canonicalTestDir(t)
	dir := filepath.Join(home, "Applications")
	useTestInstaller(t, dir)
	t.Chdir(t.TempDir())
	app := bundle.AppPath(dir, helperclient.ExecutableName)
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	controller := &recordingDeployer{complete: true, validateProofs: true, receipt: serviceDeployment{
		current: *testCanonicalGeneration(t, app, release),
	}}
	receipt, err := installService(t.Context(), controller)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.current.Path != app {
		t.Fatalf("receipt = %#v", receipt)
	}
	if controller.calls != 1 {
		t.Fatalf("deploy calls = %d, want 1", controller.calls)
	}
	config := controller.config
	if config.Dir != dir || config.AppName != helperclient.ExecutableName || config.Release != release ||
		config.Identity.TeamID != helperclient.TeamID || config.Identity.SigningIdentifier != helperclient.BundleID ||
		config.ConsumerBuild == "" || config.PolicyDigest == (deployment.SHA256{}) ||
		config.RuntimeQuiesce == nil || config.PostInstallProof == nil || config.PriorAppRestoreProof == nil ||
		config.BuildPlan == nil || config.Readiness == nil {
		t.Fatalf("deployment config = %#v", config)
	}
}

func TestInstallServiceRejectsInvalidReleaseWithoutCreatingInstallDirectory(t *testing.T) {
	setHelperRelease(t, "dev", "0.45.0", strings.Repeat("cd", 32))
	dir := filepath.Join(canonicalTestDir(t), "Applications")
	useTestInstaller(t, dir)
	controller := &recordingDeployer{}
	if _, err := installService(t.Context(), controller); err == nil {
		t.Fatal("installService accepted invalid release metadata")
	}
	if controller.calls != 0 {
		t.Fatalf("deploy calls = %d, want 0", controller.calls)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install directory changed before validation: %v", err)
	}
}

func TestInstallServiceRequiresNewCompleteExactReceipt(t *testing.T) {
	setHelperRelease(t, "v0.45.0", "0.45.0", strings.Repeat("ef", 32))
	useTestInstaller(t, filepath.Join(canonicalTestDir(t), "Applications"))
	for _, receipt := range []serviceDeployment{
		{},
		{current: deployment.CanonicalGeneration{Path: "/wrong"}},
	} {
		if _, err := installService(t.Context(), &recordingDeployer{receipt: receipt}); err == nil {
			t.Fatalf("accepted receipt %#v", receipt)
		}
	}
}

func TestInstallServiceReturnsDeploymentFailure(t *testing.T) {
	setHelperRelease(t, "v0.45.0", "0.45.0", strings.Repeat("ef", 32))
	useTestInstaller(t, filepath.Join(canonicalTestDir(t), "Applications"))
	want := errors.New("deployment failed")
	_, err := installService(t.Context(), &recordingDeployer{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestInstallServiceExactlyReconcilesSameGenerationOnRepeat(t *testing.T) {
	setHelperRelease(t, "v0.45.0", "0.45.0", strings.Repeat("ce", 32))
	home := canonicalTestDir(t)
	useTestInstaller(t, filepath.Join(home, "Applications"))
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	app := bundle.AppPath(filepath.Join(home, "Applications"), helperclient.ExecutableName)
	controller := &recordingDeployer{complete: true, receipt: serviceDeployment{
		current: *testCanonicalGeneration(t, app, release),
	}}
	for range 2 {
		if _, err := installService(t.Context(), controller); err != nil {
			t.Fatal(err)
		}
	}
	if controller.calls != 2 || len(controller.configs) != 2 {
		t.Fatalf("deploy calls/configs = %d/%d, want 2/2", controller.calls, len(controller.configs))
	}
	first, second := controller.configs[0], controller.configs[1]
	if first.Dir != second.Dir || first.AppName != second.AppName || first.Release != second.Release ||
		first.Identity != second.Identity || first.ConsumerBuild != second.ConsumerBuild ||
		first.PolicyDigest != second.PolicyDigest {
		t.Fatalf("repeat configs differ: %#v / %#v", first, second)
	}
	operation := deployment.Operation{ID: strings.Repeat("a", 32), Generation: controller.receipt.current}
	firstPlan, err := first.BuildPlan(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := second.BuildPlan(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.Digest() != secondPlan.Digest() {
		t.Fatalf("repeat plan digests differ: %s / %s", firstPlan.Digest(), secondPlan.Digest())
	}
}

func TestUninstallServiceAcceptsAbsentAndExactInactive(t *testing.T) {
	dir := filepath.Join(canonicalTestDir(t), "Applications")
	useTestInstaller(t, dir)
	absent := &recordingDeployer{deactivation: deactivationResult{state: deactivationAbsent}}
	if result, err := uninstallService(t.Context(), absent); err != nil ||
		result.state != deactivationAbsent {
		t.Fatalf("absent result = (%#v, %v)", result, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent deactivation changed install directory: %v", err)
	}
	app := bundle.AppPath(dir, helperclient.ExecutableName)
	activation := testInstallerPlan(t, dir, app, "v0.44.0")
	empty, err := service.NewPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	inactiveReceipt := exactInactiveReceipt(t, app, activation, empty)
	inactive := &recordingDeployer{deactivation: deactivationResult{
		state: deactivationInactive, inactive: inactiveReceipt,
	}}
	result, err := uninstallService(t.Context(), inactive)
	if err != nil || result.inactive.current.Path != app {
		t.Fatalf("inactive result = (%#v, %v)", result, err)
	}
	config := inactive.deactivateConfig
	if inactive.deactivateCalls != 1 || config.Dir != dir || config.AppName != helperclient.ExecutableName ||
		config.Identity.TeamID != helperclient.TeamID || config.Identity.SigningIdentifier != helperclient.BundleID ||
		config.ConsumerBuild == "" || config.PolicyDigest == (deployment.SHA256{}) ||
		config.RuntimeQuiesce == nil || config.Readiness == nil {
		t.Fatalf("deactivate config = %#v", config)
	}
}

func TestUninstallServiceFreshMachineIsZeroWrite(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "Applications")
	useTestInstaller(t, dir)
	result, err := uninstallService(t.Context(), daemonkitDeployer{controller: deployment.New()})
	if err != nil {
		t.Fatal(err)
	}
	if result.state != deactivationAbsent {
		t.Fatalf("deactivation result = %#v", result)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh deactivation changed install directory: %v", err)
	}
}

func TestUninstallServiceRejectsInexactResults(t *testing.T) {
	dir := filepath.Join(canonicalTestDir(t), "Applications")
	useTestInstaller(t, dir)
	app := bundle.AppPath(dir, helperclient.ExecutableName)
	activation := testInstallerPlan(t, dir, app, "v0.44.0")
	empty, err := service.NewPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	exact := exactInactiveReceipt(t, app, activation, empty)
	missingCurrent := exact
	missingCurrent.current = deployment.CanonicalGeneration{}
	nonempty := exact
	nonempty.plan = activation
	wrongApp := exact
	wrongApp.current = deployment.CanonicalGeneration{Path: "/wrong"}
	wrongRequirement := exact
	wrongRequirement.current.DesignatedRequirement = "identifier \"wrong\""
	missingCDHash := exact
	missingCDHash.current.CDHash = ""
	missingBundleDigest := exact
	missingBundleDigest.current.BundleDigest = deployment.SHA256{}
	noActivation := exact
	noActivation.activationPlan = empty
	for name, result := range map[string]deactivationResult{
		"unknown state":     {},
		"missing receipt":   {state: deactivationInactive},
		"missing current":   {state: deactivationInactive, inactive: missingCurrent},
		"nonempty plan":     {state: deactivationInactive, inactive: nonempty},
		"wrong app":         {state: deactivationInactive, inactive: wrongApp},
		"wrong requirement": {state: deactivationInactive, inactive: wrongRequirement},
		"missing cdhash":    {state: deactivationInactive, inactive: missingCDHash},
		"missing bundle":    {state: deactivationInactive, inactive: missingBundleDigest},
		"no activation":     {state: deactivationInactive, inactive: noActivation},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := uninstallService(t.Context(), &recordingDeployer{deactivation: result}); err == nil {
				t.Fatalf("accepted %#v", result)
			}
		})
	}
}

func TestUninstallServiceIsIdempotentAndRetainsApp(t *testing.T) {
	dir := filepath.Join(canonicalTestDir(t), "Applications")
	useTestInstaller(t, dir)
	app := bundle.AppPath(dir, helperclient.ExecutableName)
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(app, "marker")
	if err := os.WriteFile(marker, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	activation := testInstallerPlan(t, dir, app, "v0.44.0")
	empty, err := service.NewPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := exactInactiveReceipt(t, app, activation, empty)
	controller := &recordingDeployer{deactivation: deactivationResult{
		state: deactivationInactive, inactive: receipt,
	}}
	for range 2 {
		if _, err := uninstallService(t.Context(), controller); err != nil {
			t.Fatal(err)
		}
	}
	if controller.deactivateCalls != 2 {
		t.Fatalf("deactivate calls = %d, want 2", controller.deactivateCalls)
	}
	payload, err := os.ReadFile(marker)
	if err != nil || string(payload) != "retained" {
		t.Fatalf("retained app marker = %q, %v", payload, err)
	}
}

func TestUninstallServiceReturnsDeactivationFailure(t *testing.T) {
	useTestInstaller(t, filepath.Join(canonicalTestDir(t), "Applications"))
	want := errors.New("deactivation failed")
	_, err := uninstallService(t.Context(), &recordingDeployer{deactivateErr: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestProvisionRepositoryUsesOnlyInstalledHelper(t *testing.T) {
	home := canonicalTestDir(t)
	repoRoot := t.TempDir()
	wantExecutable := filepath.Join(home, "Applications", "CCNotesHelper.app", "Contents", "MacOS", "CCNotesHelper")
	var gotExecutable, gotRoot string
	err := provisionRepository(t.Context(), repoRoot, func() (string, error) {
		return wantExecutable, nil
	}, func(_ context.Context, executable, root string) error {
		gotExecutable, gotRoot = executable, root
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotExecutable != wantExecutable || gotRoot != repoRoot {
		t.Fatalf("invocation = (%q, %q), want (%q, %q)", gotExecutable, gotRoot, wantExecutable, repoRoot)
	}
}

func TestProvisionRepositoryDoesNotInvokeMissingHelper(t *testing.T) {
	want := os.ErrNotExist
	err := provisionRepository(t.Context(), t.TempDir(), func() (string, error) {
		return "", want
	}, func(context.Context, string, string) error {
		t.Fatal("provision invoked without an installed helper")
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestEnsureInstallDirectoryPreservesExistingContainerMode(t *testing.T) {
	dir := filepath.Join(canonicalTestDir(t), "Applications")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstallDirectory(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing mode = %o, want 755", info.Mode().Perm())
	}
}

func useTestInstaller(t *testing.T, dir string) {
	t.Helper()
	previousDir, previousHooks := installedDir, makeProductHooks
	installedDir = func() (string, error) { return dir, nil }
	makeProductHooks = func(buildID string, policyDigest deployment.SHA256) productHooks {
		hooks := productHooks{buildID: buildID, policyDigest: policyDigest}
		presentationRoot := filepath.Join(dir, "mount")
		hooks.servicePlan = func(operation deployment.Operation) (service.Plan, error) {
			return testInstallerPlan(t, dir, operation.Generation.Path, buildID), nil
		}
		hooks.servicePlanBuild = func(operation deployment.Operation, storedBuild string) (service.Plan, error) {
			return testInstallerPlan(t, dir, operation.Generation.Path, storedBuild), nil
		}
		hooks.verifyInstalled = func(context.Context, string, string) (string, string, error) {
			return filepath.Join(dir, "libfuse-t.dylib"), strings.Repeat("ab", 32), nil
		}
		hooks.targetBuild = func(operation deployment.Operation, storedBuild string) (runtimeTarget, error) {
			return runtimeTarget{
				executable: bundleExecutable(operation.Generation.Path), socket: filepath.Join(dir, "helper.sock"),
				buildID: storedBuild, presentationRoot: presentationRoot,
			}, nil
		}
		hooks.observe = func(_ context.Context, _ string) (mountproto.RuntimeHealthResponse, error) {
			return exactHealth(t, runtimeTarget{buildID: buildID, presentationRoot: presentationRoot}), nil
		}
		return hooks
	}
	t.Cleanup(func() {
		installedDir, makeProductHooks = previousDir, previousHooks
	})
}

func testInstallerPlan(t *testing.T, dir, app, buildID string) service.Plan {
	t.Helper()
	executable := bundle.ExePath(app, helperclient.ExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan, err := service.NewPlan([]service.Agent{{
		Label:   "com.yasyf.cc-notes.helper.fusekit",
		Program: executable,
		LogPath: filepath.Join(dir, "helper.log"), RestartPolicy: service.RestartAlways,
		LimitLoadToSessionType: service.SessionTypeAqua,
		Env:                    map[string]string{"FUSEKIT_BUILD_ID": buildID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func exactInactiveReceipt(t *testing.T, app string, activation, empty service.Plan) serviceDeployment {
	t.Helper()
	return serviceDeployment{
		operationID: strings.Repeat("b", 32),
		current: *testCanonicalGeneration(t, app, deployment.Release{
			Version: "0.44.0", URL: "https://example.test/helper.zip",
			SHA256: deployment.SHA256(sha256.Sum256([]byte("release"))),
		}),
		plan: empty, activationPlan: activation,
	}
}

func testCanonicalGeneration(
	t *testing.T,
	app string,
	release deployment.Release,
) *deployment.CanonicalGeneration {
	t.Helper()
	requirement, err := helperCodeIdentity().DRString()
	if err != nil {
		t.Fatal(err)
	}
	return &deployment.CanonicalGeneration{
		Path: app, Release: release, DesignatedRequirement: requirement,
		CDHash:       "0123456789abcdef0123456789abcdef01234567",
		BundleDigest: deployment.SHA256(sha256.Sum256([]byte("bundle"))),
		Device:       "1", Inode: "2",
	}
}

func setHelperRelease(t *testing.T, releaseVersion, helperVersion, digest string) {
	t.Helper()
	previousVersion, previousHelperVersion, previousDigest := version.Version, version.HelperVersion, version.HelperSHA256
	version.Version, version.HelperVersion, version.HelperSHA256 = releaseVersion, helperVersion, digest
	t.Cleanup(func() {
		version.Version, version.HelperVersion, version.HelperSHA256 = previousVersion, previousHelperVersion, previousDigest
	})
}
