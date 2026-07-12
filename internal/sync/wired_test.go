package sync_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

func TestWiredRemotes(t *testing.T) {
	track := func(name string) string { return "+refs/cc-notes/*:refs/cc-notes-sync/" + name + "/*" }
	const (
		oldForm = "+refs/cc-notes/*:refs/cc-notes/*"
		heads   = "+refs/heads/*:refs/remotes/origin/*"
	)
	for _, tc := range []struct {
		name string
		wire [][2]string
		want []string
	}{
		{"none wired", [][2]string{{"origin", heads}}, nil},
		{"origin wired", [][2]string{{"origin", heads}, {"origin", track("origin")}}, []string{"origin"}},
		{"non-origin only", [][2]string{{"origin", heads}, {"upstream", track("upstream")}}, []string{"upstream"}},
		{"two in config order", [][2]string{{"zulu", track("zulu")}, {"alpha", track("alpha")}}, []string{"zulu", "alpha"}},
		{"unrelated refspec ignored", [][2]string{{"origin", track("other")}}, nil},
		{"pre-fix form counted", [][2]string{{"origin", oldForm}}, []string{"origin"}},
		{"both forms deduped", [][2]string{{"origin", track("origin")}, {"origin", oldForm}}, []string{"origin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := gittest.InitRepo(t)
			for _, w := range tc.wire {
				gittest.Git(t, dir, "config", "--add", "remote."+w[0]+".fetch", w[1])
			}
			got, err := ccsync.WiredRemotes(t.Context(), gitcmd.Git{Dir: dir})
			if err != nil {
				t.Fatalf("WiredRemotes: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("WiredRemotes() = %v, want %v", got, tc.want)
			}
		})
	}
}
