package holderapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/daemonkit/trust"
)

func TestReleaseOwnsOneFixedSignedHolderTopology(t *testing.T) {
	root := filepath.Join("..", "..")
	assertFileContains(t, filepath.Join(root, ".github", "workflows", "release.yml"),
		"./cmd/cc-notes-holder",
		`MARKETING="${RELEASE_TAG#v}"`,
		`MARKETING="${MARKETING%%-*}"`,
		`version: ${{ steps.holder-version.outputs.version }}`,
		`MARKETING="${{ steps.holder-version.outputs.version }}"`,
		`APP_PATH="$APP" TEAM_ID="$TEAM_ID" DESIGNATED_REQUIREMENT_FILE="$REQUIREMENTS"`,
		"bash .github/scripts/assert-holder-app.sh",
		"yasyf/homebrew-tap/.github/actions/sign-notarize-app@v2",
		"release-tag: ${{ env.RELEASE_TAG }}",
		"needs: [release, holder-app]",
		"Publish the exact CLI to the tap",
		"cc-notes holder: fetch signed app",
		"$HOME/.cc-notes/holder/CCNotesHolder.app/Contents/MacOS/CCNotesHolder",
		"$HOME/.cc-notes/holder/.daemonkit-fetch/CCNotesHolder/receipt.json",
		"needs: [verify-tag-on-main, web-dist, holder-app]",
		"github.com/yasyf/cc-notes/internal/version.HolderVersion=${{ needs.holder-app.outputs.version }}",
		"github.com/yasyf/cc-notes/internal/version.HolderSHA256=${{ needs.holder-app.outputs.sha256 }}",
		"needs: [pure, smoke-linux, smoke-macos, holder-app]",
		"path: dist/cc-notes-holder-*.zip",
		`attach-to-release: "false"`,
	)
	assertFileContains(t, filepath.Join(root, "cmd", "cc-notes-holder", "main.go"),
		"holdercontract.ParseProvision(arguments)",
		"fusefs.ProvisionRepository",
	)
	assertFileContains(t, filepath.Join(root, "internal", "holderclient", "runner.go"),
		"reconcileHolder(ctx, fetch.New())",
		"/cc-notes-holder-%s-darwin.zip",
		"fetch.ParseSHA256(version.HolderSHA256)",
		"Release: release",
		"TeamID: TeamID, SigningIdentifier: BundleID",
	)
	assertFileContains(t, filepath.Join(root, "scripts", "install.sh"),
		"brew install yasyf/tap/cc-notes >/dev/null",
	)
	assertFileContains(t, filepath.Join(root, ".github", "scripts", "assert-holder-app.sh"),
		"go run ./cmd/cc-notes-fuse-package",
		"Contents/Frameworks/libfuse-t.dylib",
		"disable-library-validation",
	)
}

func TestReleasePinsExactHolderDesignatedRequirement(t *testing.T) {
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
	assertFileContains(t, filepath.Join(root, ".github", "scripts", "assert-holder-app.sh"),
		`codesign --verify --strict --verbose=2 -R "=$DESIGNATED_REQUIREMENT" "$APP"`,
		`CODE_DETAILS="$(codesign -d --verbose=4 "$APP" 2>&1)"`,
		`flags=.*\(([^,]+,)*runtime(,[^,]+)*\)`,
	)
}

func TestOrdinaryCLIHasNoHolderChildDispatch(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "cmd", "cc-notes", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"RunHolderChild", "holder.RunChild", "--fusekit-"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("ordinary CLI retains holder dispatch %q", forbidden)
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
