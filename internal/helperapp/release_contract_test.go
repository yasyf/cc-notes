package helperapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/daemonkit/trust"
)

func TestReleaseOwnsOneFixedSignedHelperTopology(t *testing.T) {
	root := filepath.Join("..", "..")
	assertFileContains(t, filepath.Join(root, ".github", "workflows", "release.yml"),
		"./cmd/cc-notes-helper",
		`MARKETING="${RELEASE_TAG#v}"`,
		`MARKETING="${MARKETING%%-*}"`,
		`version: ${{ steps.helper-version.outputs.version }}`,
		`MARKETING="${{ steps.helper-version.outputs.version }}"`,
		`APP_PATH="$APP" TEAM_ID="$TEAM_ID" DESIGNATED_REQUIREMENT_FILE="$REQUIREMENTS"`,
		"bash .github/scripts/assert-helper-app.sh",
		"yasyf/homebrew-tap/.github/actions/sign-notarize-app@v2",
		"release-tag: ${{ env.RELEASE_TAG }}",
		"needs: [release, helper-app]",
		"Publish the exact CLI to the tap",
		"cc-notes helper: fetch signed app",
		"$HOME/Applications/CCNotesHelper.app/Contents/MacOS/CCNotesHelper",
		"$HOME/Applications/.daemonkit-fetch/CCNotesHelper/receipt.json",
		"needs: [verify-tag-on-main, web-dist, helper-app]",
		"github.com/yasyf/cc-notes/internal/version.HelperVersion=${{ needs.helper-app.outputs.version }}",
		"github.com/yasyf/cc-notes/internal/version.HelperSHA256=${{ needs.helper-app.outputs.sha256 }}",
		"needs: [pure, smoke-linux, smoke-macos, helper-app]",
		"path: dist/cc-notes-helper-*.zip",
		`attach-to-release: "false"`,
	)
	assertFileContains(t, filepath.Join(root, "cmd", "cc-notes-helper", "main.go"),
		"helpercontract.ParseProvision(arguments)",
		"fusefs.ProvisionRepository",
	)
	assertFileContains(t, filepath.Join(root, "internal", "helperclient", "runner.go"),
		"reconcileHelper(ctx, fetch.New())",
		"/cc-notes-helper-%s-darwin.zip",
		"fetch.ParseSHA256(version.HelperSHA256)",
		"Release: release",
		"TeamID: TeamID, SigningIdentifier: BundleID",
	)
	assertFileContains(t, filepath.Join(root, "scripts", "install.sh"),
		"brew install yasyf/tap/cc-notes >/dev/null",
	)
	assertFileContains(t, filepath.Join(root, ".github", "scripts", "assert-helper-app.sh"),
		"go run ./cmd/cc-notes-fuse-package",
		"Contents/Frameworks/libfuse-t.dylib",
		"disable-library-validation",
	)
}

func TestReleasePinsExactHelperDesignatedRequirement(t *testing.T) {
	requirement, err := (trust.Requirement{
		TeamID: TeamID, SigningIdentifier: BundleID,
	}).DRString()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..")
	assertFileContains(t, filepath.Join(root, ".github", "workflows", "release.yml"),
		"designated => "+requirement,
		`--requirements "$REQUIREMENTS"`,
		`DESIGNATED_REQUIREMENT_FILE="$REQUIREMENTS"`,
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

func TestReleaseDoesNotPublishStandaloneRuntimeCask(t *testing.T) {
	root := filepath.Join("..", "..")
	assertFileExcludes(t, filepath.Join(root, ".github", "workflows", "release.yml"),
		"render-cask",
		"Casks/cc-notes-helper.rb",
		"Casks/cc-notes-holder.rb",
	)
	for _, path := range []string{
		filepath.Join(root, ".github", "cask", "cc-notes-helper.rb.tmpl"),
		filepath.Join(root, ".github", "cask", "cc-notes-holder.rb.tmpl"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("standalone runtime cask %q exists or could not be inspected: %v", path, err)
		}
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
