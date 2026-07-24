package helperapp

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yasyf/daemonkit/trust"
)

func TestReleaseUsesPinnedReusableFixedHelperWorkflow(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := filepath.Join(root, ".github", "workflows", "release.yml")
	assertFileContains(t, workflow,
		"uses: yasyf/homebrew-tap/.github/workflows/release-app.yml@83ee384b1d4fe25a8e4aa7258bb76d55e1593735",
		"app_name: CCNotesHelper",
		"asset_name: cc-notes-helper",
		"project_dir: helper-app",
		"scheme: CCNotesHelper",
		"bundle_id: com.yasyf.cc-notes.helper",
		"marketing_version: ${{ needs.version.outputs.marketing }}",
		"assert_script: .github/scripts/assert-helper-app.sh",
		"prebuild_brew_packages: macos-fuse-t/cask/fuse-t",
		"prebuild_script: helper-app/prepare-release.sh",
		`COMMIT="$GITHUB_SHA"`,
		`grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'`,
		"HELPER_ASSET_FILENAME: ${{ needs.helper-app.outputs.asset_filename }}",
		"HELPER_ASSET_URL: ${{ needs.helper-app.outputs.asset_url }}",
		"HELPER_SHA256: ${{ needs.helper-app.outputs.sha256 }}",
		`helper="$(sha "cc-notes-helper-${RELEASE_TAG}-darwin.zip")"`,
		"__SHA_HELPER__=${{ steps.shas.outputs.helper }}",
	)
	assertFileExcludes(t, workflow,
		"yasyf/homebrew-tap/.github/actions/sign-notarize-app@v2",
		".daemonkit-fetch",
		"github.com/yasyf/daemonkit/fetch",
		`"$BIN" init`,
		"cc-notes init",
	)
}

func TestReleasePinsEveryExternalActionByCommit(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	payload, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}

	uses := regexp.MustCompile(`(?m)^\s+(?:-\s+)?uses:\s+[^@\s]+@([^\s]+)\s*$`).FindAllStringSubmatch(string(payload), -1)
	if len(uses) == 0 {
		t.Fatal("release workflow has no external actions")
	}
	exactCommit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, match := range uses {
		if !exactCommit.MatchString(match[1]) {
			t.Errorf("release workflow uses mutable external ref %q", match[0])
		}
	}

	assertFileContains(t, workflow,
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38",
		"actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a",
		"actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c",
		"yasyf/homebrew-tap/.github/actions/render-formula@19c3d5013032ad9c88f9a8f1170d1f366c19b8d9",
		"yasyf/homebrew-tap/.github/actions/publish@9525763796fce4d1042cf3393d9479f791908eaa",
	)
}

func TestReleasePublishesOnlyOneCompleteCallerOwnedDraft(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	assertFileContains(t, workflow,
		"needs: [pure, smoke-linux, smoke-macos, helper-app]",
		"  bump-formula:\n    runs-on: ubuntu-latest\n    needs: [release, helper-app]",
		"name: ${{ needs.helper-app.outputs.artifact_name }}",
		`mv "$checksums" dist/SHA256SUMS.txt`,
		`cat dist/SHA256SUMS.txt`,
		`find dist -maxdepth 1 -type f -print | LC_ALL=C sort`,
		`test "$HELPER_ASSET_FILENAME" = "cc-notes-helper-${RELEASE_TAG}-darwin.zip"`,
		`uses: yasyf/homebrew-tap/.github/actions/stage-draft-release@e4c3108e693681df1a3c666bae80e890bc44cf3e`,
		`uses: yasyf/homebrew-tap/.github/actions/publish-draft-release@54e3e194bda69896894a82c17fcdb2822beefab5`,
		`manifest: ${{ runner.temp }}/cc-notes-release-assets.txt`,
		`release-id: ${{ steps.stage-release.outputs.release-id }}`,
		`make-latest: ${{ !contains(github.ref_name, '-') }}`,
		"contents: read",
	)
	assertFileExcludes(t, workflow,
		"softprops/action-gh-release",
		`releases/tags/$RELEASE_TAG`,
		`gh release create`,
		`gh release upload`,
		`gh release edit`,
		"          cd dist\n          checksums=",
		"contents: write\n    uses: yasyf/homebrew-tap/.github/workflows/release-app.yml",
	)
	payload, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(payload), "publish-draft-release@54e3e194bda69896894a82c17fcdb2822beefab5"); count != 1 {
		t.Fatalf("public release transitions = %d, want exactly 1", count)
	}
	text := string(payload)
	verify := strings.Index(text, `stage-draft-release@e4c3108e693681df1a3c666bae80e890bc44cf3e`)
	publish := strings.Index(text, `publish-draft-release@54e3e194bda69896894a82c17fcdb2822beefab5`)
	if verify < 0 || publish < 0 || publish <= verify {
		t.Fatal("public release transition does not follow draft redownload verification")
	}
}

func TestReleaseBuildsOneXcodeGenCCNotesHelper(t *testing.T) {
	root := filepath.Join("..", "..")
	project := filepath.Join(root, "helper-app", "project.yml")
	assertFileContains(t, project,
		"name: CCNotesHelper",
		"CCNotesHelper:",
		"type: application",
		"PRODUCT_BUNDLE_IDENTIFIER: com.yasyf.cc-notes.helper",
		"./cmd/cc-notes-helper",
		`lipo -create "$arm64" "$amd64" -output "$executable"`,
		`commit="$GITHUB_SHA"`,
	)
	assertFileExcludes(t, project,
		"/"+"Applications/",
	)
	assertFileContains(t, filepath.Join(root, ".github", "scripts", "assert-helper-app.sh"),
		"go run ./cmd/cc-notes-helper-package",
		"Contents/Frameworks/libfuse-t.dylib",
		"disable-library-validation",
	)
	assertFileContains(t, filepath.Join(root, "cmd", "cc-notes-helper-package", "main.go"),
		"Command cc-notes-helper-package",
		"helperapp.PackageFUSE",
	)
}

func TestHelperDeploymentUsesDaemonkitSchemaOneState(t *testing.T) {
	root := filepath.Join("..", "..")
	deploymentSource := filepath.Join(root, "internal", "helperdeployment", "deployment.go")
	assertFileContains(t, deploymentSource,
		`"github.com/yasyf/daemonkit/deployment"`,
		"AttestInstalled(context.Context, deployment.InstalledSpec)",
		"StatusInstalled(context.Context, deployment.InstalledSpec)",
		"ActivateInstalled(context.Context, deployment.ActivateInstalledConfig)",
		"DeactivateInstalled(context.Context, deployment.DeactivateInstalledConfig)",
		"newInstalledController = func() installedController { return deployment.New() }",
		"controller.AttestInstalled(ctx, spec)",
		"controller.ActivateInstalled(ctx, deployment.ActivateInstalledConfig{",
		"Expected: attestation, ConsumerBuild: consumerBuild, PolicyDigest: policyDigest",
		"Readiness: hooks.readiness",
		"validateActivationReceipt(receipt, attestation, plan, hooks.buildID)",
		"controller.DeactivateInstalled(ctx, deployment.DeactivateInstalledConfig{",
		"Expected: activation, ConsumerBuild: consumerBuild, PolicyDigest: policyDigest",
		"RuntimeQuiesce: hooks.runtimeQuiesce",
		"!validDeploymentOperationID(receipt.OperationID())",
		"after.State() != deployment.InstalledVerifiedUnactivated",
	)
	assertFileExcludes(t, deploymentSource,
		"daemonkit/fetch",
		".daemonkit-fetch",
		"reconcileHelper",
		"service.NewController",
		"services.db",
		"service-workers.db",
		"launchctl",
		"os.Remove(",
		"os.RemoveAll(",
		"os.Rename(",
	)
	assertFileContains(t, filepath.Join(root, "internal", "helperdeployment", "deployment_identity.go"),
		`deploymentPolicyIdentity = "cc-notes.deployment-callbacks.v1"`,
		"Schema:   1",
		"startupConsumerBuild, startupConsumerBuildErr = currentConsumerBuild()",
	)
	assertFileContains(t, filepath.Join(root, "cmd", "cc-notes-helper", "main.go"),
		"deployment.RuntimeStopControlStore()",
		"holder.RunChild",
		"NativeReadinessTimeout:  helpercontract.RuntimeNativeReadinessTimeout",
		"CatalogReadinessTimeout: helpercontract.RuntimeCatalogReadinessTimeout",
		"CatalogOperationTimeout: helpercontract.RuntimeCatalogOperationTimeout",
		"ShutdownTimeout:         helpercontract.RuntimeShutdownTimeout",
	)
}

func TestFixedHelperLivesOnlyInUserApplications(t *testing.T) {
	root := filepath.Join("..", "..")
	identity := filepath.Join(root, "internal", "helperclient", "identity.go")
	assertFileContains(t, identity,
		`return filepath.Join(home, "Applications"), nil`,
		"return bundle.AppPath(dir, ExecutableName), nil",
	)
	assertFileExcludes(t, identity,
		`filepath.Join("/`+`Applications"`,
		`"/`+`Applications/CCNotesHelper.app"`,
	)
}

func TestReleasedNativePresentationRootIsUserVisible(t *testing.T) {
	root := filepath.Join("..", "..")
	assertFileContains(t, filepath.Join(root, "internal", "helperclient", "identity.go"),
		`return filepath.Join(home, "CCNotes"), nil`,
	)
	assertFileContains(t, filepath.Join(root, "internal", "helperdeployment", "deployment_identity.go"),
		`PresentationRootHomeRelative: "CCNotes"`,
	)
}

func TestServiceCommandsAreExplicitMachineOnlyOperations(t *testing.T) {
	serviceSource := filepath.Join("..", "..", "internal", "cli", "service.go")
	assertFileContains(t, serviceSource,
		`Use:   "install"`,
		`Use:   "uninstall"`,
		"Args:  exactArgs(0)",
		`"installed: CCNotesHelper service"`,
		`"uninstalled: CCNotesHelper service"`,
		`errors.New("cc-notes service commands do not accept --repo")`,
	)
	assertFileExcludes(t, serviceSource,
		"provisionRepository",
		"openStore",
		"repoRoot",
	)
}

func TestReleasePinsExactHelperDesignatedRequirement(t *testing.T) {
	requirement, err := (trust.Requirement{
		TeamID: "SXKCTF23Q2", SigningIdentifier: "com.yasyf.cc-notes.helper",
	}).DRString()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..")
	assertFileContains(t, filepath.Join(root, "helper-app", "prepare-release.sh"),
		"designated => "+requirement,
		"DESIGNATED_REQUIREMENT_FILE",
	)
	assertFileContains(t, filepath.Join(root, ".github", "scripts", "assert-helper-app.sh"),
		`codesign --verify --strict --verbose=2 -R "=$DESIGNATED_REQUIREMENT" "$APP"`,
		`CODE_DETAILS="$(codesign -d --verbose=4 "$APP" 2>&1)"`,
		`flags=.*\(([^,]+,)*runtime(,[^,]+)*\)`,
	)
}

func TestOrdinaryCLIHasNoHelperChildDispatch(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "cmd", "cc-notes", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"RunHelperChild", "holder.RunChild", "--fusekit-"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("ordinary CLI retains helper dispatch %q", forbidden)
		}
	}
}

func TestActiveTreeHasNoRetiredHelperDeliverySurface(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := []string{
		"github.com/yasyf/daemonkit/" + "fetch",
		".daemonkit-" + "fetch",
		"cc-notes-" + "fuse-package",
		"sign-notarize-app@" + "v2",
		"CCNotes" + "Holder",
		"cc-notes-" + "holder",
		"fusekit-" + "holder",
		"Holder " + "v2",
		`"/` + `Applications/CCNotesHelper.app"`,
		`'/` + `Applications/CCNotesHelper.app'`,
	}
	for _, path := range []string{
		filepath.Join(root, ".claude", "fragments"),
		filepath.Join(root, ".github"),
		filepath.Join(root, "helper-app"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "internal"),
		filepath.Join(root, "model"),
		filepath.Join(root, "notes"),
		filepath.Join(root, "docs"),
		filepath.Join(root, "plugin"),
		filepath.Join(root, "web"),
		filepath.Join(root, "README.md"),
		filepath.Join(root, "CHANGELOG.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "scripts"),
	} {
		assertTreeExcludes(t, path, forbidden...)
	}
}

func TestReleasePackagesHelperWithoutPublishingRuntimeCask(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := filepath.Join(root, ".github", "workflows", "release.yml")
	assertFileExcludes(t, workflow,
		"render-cask",
		"Casks/",
		"cc-notes-helper.rb",
		"cc-notes-runtime",
	)
	assertFileContains(t, workflow,
		`test "$HELPER_ASSET_URL" = "https://github.com/${GITHUB_REPOSITORY}/releases/download/${RELEASE_TAG}/${HELPER_ASSET_FILENAME}"`,
	)
	formula := filepath.Join(root, ".github", "formula", "cc-notes.rb.tmpl")
	assertFileContains(t, formula,
		`resource "helper" do`,
		`cc-notes-helper-v#{version}-darwin.zip`,
		`sha256 "__SHA_HELPER__"`,
		`system "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", "CCNotesHelper.app"`,
		`libexec.install "CCNotesHelper.app"`,
		`cc-notes package install`,
	)
	assertFileExcludes(t, formula,
		"Casks/",
		`/Applications`,
		`FileUtils.cp_r`,
	)
	installer := filepath.Join(root, "scripts", "install.sh")
	assertFileContains(t, installer,
		`helper_asset="cc-notes-helper-${VERSION}-darwin.zip"`,
		`ditto -x -k "$helper_zip" "$helper_stage"`,
		`"$DEST" package install`,
	)
	assertFileExcludes(t, installer, "/"+"Applications/")
	assertFileExcludes(t, filepath.Join(root, "plugin", "hooks", "ensure-cc-notes.sh"),
		"CCNotesHelper.app", "cc-notes-helper-", "/"+"Applications/",
	)
	for _, path := range []string{
		filepath.Join(root, "Casks"),
		filepath.Join(root, ".github", "cask"),
	} {
		assertPathAbsent(t, path)
	}
}

func TestPluginBootstrapEnforcesV045WithoutInstallingService(t *testing.T) {
	root := filepath.Join("..", "..")
	hook := filepath.Join(root, "plugin", "hooks", "hooks.json")
	script := filepath.Join(root, "plugin", "hooks", "ensure-cc-notes.sh")
	assertFileContains(t, hook, `sh \"${CLAUDE_PLUGIN_ROOT}/hooks/ensure-cc-notes.sh\"`)
	assertFileContains(t, script,
		`($2 + 0) >= 45`,
		"https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh",
	)
	assertFileExcludes(t, script, "service install", "service uninstall", "cc-notes init")
	assertFileContains(t, filepath.Join(root, "plugin", "capt-hook", "hooks", "bootstrap.py"),
		"MIN_VERSION = (0, 45, 0)",
	)

	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "installed")
	writeExecutable(t, filepath.Join(bin, "cc-notes"), "#!/bin/sh\nprintf '%s\\n' \"$TEST_VERSION\"\n")
	writeExecutable(t, filepath.Join(bin, "curl"), "#!/bin/sh\n: > \"$INSTALL_MARKER\"\nprintf ':\\n'\n")
	run := func(version string) {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(),
			"PATH="+bin+":/usr/bin:/bin",
			"TEST_VERSION="+version,
			"INSTALL_MARKER="+marker,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bootstrap %s: %v: %s", version, err, output)
		}
	}
	run("v0.44.0")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("v0.44 did not trigger binary upgrade: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	run("v0.45.0")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("v0.45 unexpectedly triggered binary upgrade: %v", err)
	}
}

func TestHookCIUsesOneExactCaptainHookRelease(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := filepath.Join(root, ".github", "workflows", "ci.yml")
	assertFileContains(t, workflow,
		`CAPT_HOOK_VERSION: "12.15.3"`,
		`key: capt-hook-${{ env.CAPT_HOOK_VERSION }}-nlp-v5`,
		`--with "capt-hook==$CAPT_HOOK_VERSION"`,
		`uvx --isolated "capt-hook==$CAPT_HOOK_VERSION" pack test plugin`,
	)
	assertFileExcludes(t, workflow,
		"--with capt-hook ",
		"capt-hook>=",
	)
	assertFileContains(t,
		filepath.Join(root, "plugin", "capt-hook", "hooks", "tests", "test_cc_notes.py"),
		`# dependencies = ["capt-hook==12.15.3", "pydantic>=2"]`,
	)
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(payload), fragment) {
			t.Errorf("%s lacks %q", path, fragment)
		}
	}
}

func assertFileExcludes(t *testing.T, path string, fragments ...string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if strings.Contains(string(payload), fragment) {
			t.Errorf("%s retains forbidden %q", path, fragment)
		}
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("retired path %q exists or could not be inspected: %v", path, err)
	}
}

func assertTreeExcludes(t *testing.T, root string, fragments ...string) {
	t.Helper()
	rootApplications := regexp.MustCompile("(?:^|[\\s\"'=(`])/Applications(?:/|[\\s\"')`]|$)")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "release_contract_test.go" {
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rootApplications.Match(payload) {
			t.Errorf("%s retains a system /Applications path", path)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(payload), fragment) {
				t.Errorf("%s retains forbidden %q", path, fragment)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
