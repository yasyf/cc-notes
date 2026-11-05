package gitcmd

import (
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestMemoizedConfigRereadsAnEditOnceItAges(t *testing.T) {
	dir := gittest.InitRepo(t)
	g := Git{Dir: dir}
	ctx := WithCommandMemo(t.Context())
	read := func() int {
		t.Helper()
		pairs, err := g.ConfigGetRegexp(ctx, `^cc-notes\.local`)
		if err != nil {
			t.Fatal(err)
		}
		return len(pairs)
	}
	if got := read(); got != 0 {
		t.Fatalf("initial = %d", got)
	}
	gittest.Git(t, dir, "config", "cc-notes.localLabel", "outside")
	if got := read(); got != 0 {
		t.Fatalf("fresh listing was not reused: %d", got)
	}
	g.repo(memoOf(ctx)).read = time.Now().Add(-configMemoMaxAge)
	if got := read(); got != 1 {
		t.Fatalf("aged listing kept: %d", got)
	}
}
