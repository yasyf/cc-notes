package gitcmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPushClassifiesGitHubRefusals(t *testing.T) {
	const ref = "refs/cc-notes/ledgers/5b91baca4e233792d0bb32a5c8a4008aba86c5d6"
	cases := []struct {
		name   string
		reason string
		want   error
		not    error
	}{
		{"ref moved under the remote", "cannot lock ref '" + ref + "': is at 64632b87fababfef04b6483a65c7fcb593dc5294 but expected 884a0a24a3f9cf0aab6f3d33dc2e68ddd2822731", ErrNonFastForward, ErrRemoteRejected},
		{"failed server-side update", "failed", ErrRemoteRejected, ErrNonFastForward},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := fmt.Sprintf("#!/bin/sh\nprintf 'To https://github.com/Forge-AI/monorepo\\n ! [remote rejected]         %s -> %s (%s)\\nerror: failed to push some refs\\n' >&2\nexit 1\n", ref, ref, tc.reason)
			//nolint:gosec // G306: the git stub must be executable (0o755) for the test's PATH override to run it.
			if err := os.WriteFile(filepath.Join(dir, "git"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := Git{Dir: t.TempDir()}.Push(t.Context(), "origin", ref)
			if !errors.Is(err, tc.want) || errors.Is(err, tc.not) {
				t.Fatalf("Push refused with (%s) = %v, want %v and not %v", tc.reason, err, tc.want, tc.not)
			}
		})
	}
}
