package gitobj_test

import (
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// TestWriteOpsCommitGoldenSHA pins the exact commit shas WriteOpsCommit
// derives from a frozen clock, a fixed signature, and pinned packs. Entity
// ids are these shas: TestWriteOpsCommitDeterministic proves the derivation
// is repeatable, but only this golden proves it is *unchanged* — any drift in
// pack marshaling, blob/tree/commit encoding, or signature formatting
// relocates every entity id in every existing repository and must fail here.
func TestWriteOpsCommitGoldenSHA(t *testing.T) {
	repo := open(t, initRepo(t))

	root := write(t, repo, nil, t0, createPack)
	if want := model.SHA("f57d14ab5880b1868a2816645c9ee50b613a4297"); root != want {
		t.Errorf("root commit sha = %s, want %s", root, want)
	}

	child := write(t, repo, []model.SHA{root}, t1, retitlePack)
	if want := model.SHA("e98863f303e3ec26ca4f2f2c6ac71e1b621fce10"); child != want {
		t.Errorf("child commit sha = %s, want %s", child, want)
	}
}
