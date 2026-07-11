package gitcmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

// verbLogHelper installs an inline credential.helper that appends the verb
// git invoked it with (get/store/erase) to a log file and answers get with
// the given username/password.
func verbLogHelper(t *testing.T, g gitcmd.Git, username, password string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "verbs.log")
	helper := fmt.Sprintf(
		`!f() { echo "$1" >>"%s"; if [ "$1" = get ]; then echo username=%s; echo password=%s; fi; }; f`,
		logPath, username, password)
	gittest.Git(t, g.Dir, "config", "credential.helper", helper)
	return logPath
}

func loggedVerbs(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read verb log: %v", err)
	}
	return strings.Fields(string(data))
}

func TestCredentialFillApproveReject(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	logPath := verbLogHelper(t, g, "alice", "s3cret")
	rawurl := "https://git-server.com/foo/bar.git/info/lfs"

	cred, err := g.CredentialFill(ctx, rawurl)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if cred.Username != "alice" || cred.Password != "s3cret" {
		t.Fatalf("fill = %+v, want alice/s3cret", cred)
	}

	if err := g.CredentialApprove(ctx, rawurl, cred); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := g.CredentialReject(ctx, rawurl, cred); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// fill runs the helper's get verb, approve its store verb, reject its
	// erase verb — the contract the LFS auth flow depends on.
	if got, want := loggedVerbs(t, logPath), []string{"get", "store", "erase"}; !slices.Equal(got, want) {
		t.Fatalf("helper verbs = %q, want %q", got, want)
	}
}

func TestCredentialFillNoHelperNeverPrompts(t *testing.T) {
	g := initRepo(t)
	_, err := g.CredentialFill(t.Context(), "https://git-server.com/foo/bar.git/info/lfs")
	if err == nil {
		t.Fatal("fill with no helper: want error, not a terminal prompt hang")
	}
	if !strings.Contains(err.Error(), "credential fill") {
		t.Fatalf("fill error %q lacks credential fill context", err)
	}
}
