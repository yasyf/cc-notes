package cli

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestDeriveRemote(t *testing.T) {
	track := func(name string) string { return "+refs/cc-notes/*:refs/cc-notes-sync/" + name + "/*" }
	for _, tc := range []struct {
		name string
		wire [][2]string
		want string
	}{
		{"zero wired falls back to origin", [][2]string{{"origin", "+refs/heads/*:refs/remotes/origin/*"}}, "origin"},
		{"sole non-origin wins", [][2]string{{"upstream", track("upstream")}}, "upstream"},
		{"two wired fall back to origin", [][2]string{{"origin", track("origin")}, {"upstream", track("upstream")}}, "origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := gittest.InitRepo(t)
			for _, w := range tc.wire {
				gittest.Git(t, dir, "config", "--add", "remote."+w[0]+".fetch", w[1])
			}
			got, err := deriveRemote(t.Context(), gitcmd.Git{Dir: dir})
			if err != nil {
				t.Fatalf("deriveRemote: %v", err)
			}
			if got != tc.want {
				t.Fatalf("deriveRemote() = %q, want %q", got, tc.want)
			}
		})
	}
}
