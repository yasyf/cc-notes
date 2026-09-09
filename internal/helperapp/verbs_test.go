//go:build darwin

package helperapp

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
)

// RunVerb must leave every argument shape it does not own to the caller's
// "unknown invocation" error, and must reject a wrong arity before running the
// operation — a deployment verb that ran on a typo would land a generation.
func TestRunVerbClaimsOnlyItsOwnVerbs(t *testing.T) {
	cases := []struct {
		name       string
		arguments  []string
		recognized bool
		wantErr    string
	}{
		{name: "no arguments", arguments: nil},
		{name: "empty slice", arguments: []string{}},
		{name: "unknown verb", arguments: []string{"nonsense"}},
		{name: "fusekit child mode", arguments: []string{"--fusekit-catalog-worker-v1", "x"}},
		{
			name: "version", arguments: []string{helperclient.VerbVersion}, recognized: true,
		},
		{
			name:      "version with argument",
			arguments: []string{helperclient.VerbVersion, "extra"},
			//nolint:lll // the error text is the assertion.
			recognized: true, wantErr: "version takes no arguments",
		},
		{
			name:       "package install with argument",
			arguments:  []string{helperclient.VerbPackageInstall, "extra"},
			recognized: true, wantErr: "package-install takes no arguments",
		},
		{
			name:       "provision without a path",
			arguments:  []string{helperclient.VerbProvisionRepo},
			recognized: true, wantErr: "provision-repository takes exactly one repository path",
		},
		{
			name:       "provision with a relative path",
			arguments:  []string{helperclient.VerbProvisionRepo, "relative/path"},
			recognized: true, wantErr: "repository path is not an exact absolute path",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recognized, err := RunVerb(t.Context(), testCase.arguments)
			if recognized != testCase.recognized {
				t.Fatalf("recognized = %v, want %v", recognized, testCase.recognized)
			}
			switch {
			case testCase.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case testCase.wantErr != "" && err == nil:
				t.Fatalf("error = nil, want one containing %q", testCase.wantErr)
			case testCase.wantErr != "" && !strings.Contains(err.Error(), testCase.wantErr):
				t.Fatalf("error = %q, want one containing %q", err, testCase.wantErr)
			}
		})
	}
}

// The test binary is not an app child, so the installer cannot mistake it for
// the payload it lands.
func TestPackagedApplicationRejectsANonBundledExecutable(t *testing.T) {
	appPath, err := PackagedApplication()
	if err == nil {
		t.Fatalf("packaged application = %q, want a refusal", appPath)
	}
	if want := "not the packaged CCNotesHelper app child"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want one containing %q", err, want)
	}
}
