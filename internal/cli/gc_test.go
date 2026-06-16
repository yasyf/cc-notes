package cli_test

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
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
	mustGit(t, bare, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "add", "origin", bare)

	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Doomed", "--json"))
	ref := refs.Note(model.EntityID(note.ID))
	mustRun(t, dir, "note", "rm", note.ID)
	mustGit(t, dir, "push", "origin", ref+":"+ref)

	got := mustJSON[gcReport](t, mustRun(t, dir, "gc", "--prune-remote", "--json"))
	if got.Pruned != 1 || got.Failed != 0 {
		t.Fatalf("gc --prune-remote --json = %+v, want pruned 1 / failed 0", got)
	}
	if out := mustGit(t, bare, "for-each-ref", "--format=%(refname)", ref); out != "" {
		t.Fatalf("remote ref still present after prune: %q", out)
	}
	if out := mustGit(t, dir, "for-each-ref", "--format=%(refname)", ref); out != "" {
		t.Fatalf("local ref still present after prune: %q", out)
	}
}
