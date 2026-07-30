package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/homeguard"
)

// TestBinary is the cc-notes binary TestMain builds once per test-binary run
// for the e2e suite.
var TestBinary string

// TestHome is the CC_NOTES_HOME every e2e subprocess runs under, so a command
// that records per-user state — `init` registers the repository — writes into
// the test binary's own temp directory instead of the developer's ~/.cc-notes.
var TestHome string

func TestMain(m *testing.M) {
	os.Exit(homeguard.MainWith(func() int {
		dir, err := os.MkdirTemp("", "cc-notes-bin-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = os.RemoveAll(dir) }()
		TestBinary = filepath.Join(dir, "cc-notes")
		TestHome = filepath.Join(dir, "home")
		//nolint:gosec // G204: fixed go-build of this repo's own binary in the e2e test setup.
		build := exec.Command("go", "build", "-tags", "ccnotes_test", "-o", TestBinary, "github.com/yasyf/cc-notes/cmd/cc-notes")
		build.Env = append(os.Environ(), "HOME="+homeguard.RealHome())
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build cc-notes: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}))
}
