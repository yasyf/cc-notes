package cli_test

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// gcReport mirrors the gc command's JSON shape for assertions.
type gcReport struct {
	Tidied int `json:"tidied"`
	Pruned int `json:"pruned"`
	Failed int `json:"failed"`
}

func TestGCDefaultJSON(t *testing.T) {
	dir := initRepo(t)
	got := mustJSON[gcReport](t, mustRun(t, dir, "gc", "--json"))
	if got != (gcReport{}) {
		t.Fatalf("gc --json on a clean repo = %+v, want all zero", got)
	}
}

func TestGCPruneRemoteDeletesTombstone(t *testing.T) {
	dir := initRepo(t)
	bare := t.TempDir()
	gittest.Git(t, bare, "init", "-q", "--bare")
	gittest.Git(t, dir, "remote", "add", "origin", bare)

	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Doomed", "--json"))
	ref := refs.For(model.KindNote, model.EntityID(note.ID))
	mustRun(t, dir, "note", "rm", note.ID)
	gittest.Git(t, dir, "push", "origin", ref+":"+ref)

	got := mustJSON[gcReport](t, mustRun(t, dir, "gc", "--prune-remote", "--json"))
	if got.Pruned != 1 || got.Failed != 0 {
		t.Fatalf("gc --prune-remote --json = %+v, want pruned 1 / failed 0", got)
	}
	if out := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); out != "" {
		t.Fatalf("remote ref still present after prune: %q", out)
	}
	if out := gittest.Git(t, dir, "for-each-ref", "--format=%(refname)", ref); out != "" {
		t.Fatalf("local ref still present after prune: %q", out)
	}
}
