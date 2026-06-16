package fold_test

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/model"
)

// cp builds a checkpoint commit: a single-parent commit carrying one Checkpoint
// op whose State is the folded snapshot of the covered prefix.
func cp(sha, parent, author string, at int64, lamport uint64, state model.Snapshot, coversLamport uint64, covers ...string) model.PackCommit {
	shas := make([]model.SHA, len(covers))
	for i, c := range covers {
		shas[i] = model.SHA(c)
	}
	op := model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: model.Lamport(coversLamport), CoversShas: shas}
	return mk(sha, []string{parent}, author, at, lamport, op)
}

func shuffled(commits []model.PackCommit, r *rand.Rand) []model.PackCommit {
	out := slices.Clone(commits)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestFoldNoteCompactedEqualsFull folds a note chain that contains a seed-safe
// checkpoint and the identical chain whose checkpoint slot is an empty no-op
// commit. The lamports, parents, authors, and tip are identical; only whether
// the prefix seeds or replays differs. The two folds must be byte-for-byte
// identical, including Head.
func TestFoldNoteCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Body: "B0", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"}, model.AddTag{Tag: "b"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	state, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.SetBody{Body: "B3"}, model.RemoveTag{Tag: "a"})
	c4 := mk("c4", []string{"c3"}, "erin", 500, 6,
		model.AddTag{Tag: "c"},
		model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "oid"}}, VerifiedCommit: "c4"},
	)
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Note(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Note(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotCompact.Head != "c4" {
		t.Fatalf("Head = %q, want c4", gotCompact.Head)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 30 {
		got, err := fold.Note(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}

// TestFoldTaskCompactedEqualsFull is the task analog: a claim, comment, renew,
// and status transition straddle a seed-safe checkpoint. The lease fields
// (HeartbeatAt, HeartbeatLamport, StartedAt, ClosedAt) must match the full fold.
func TestFoldTaskCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "Fix", Type: model.TypeBug, Priority: 1, Branch: "main", Labels: []string{"ci"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.Claim{Assignee: "bob"})
	c2 := mk("c2", []string{"c1"}, "bob", 300, 3, model.AddComment{Body: "on it"}, model.AddLabel{Label: "sync"})
	state, err := fold.Task([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	cKempty := mk("cK", []string{"c2"}, "compactor", 350, 4)
	c3 := mk("c3", []string{"cK"}, "bob", 400, 5, model.Renew{})
	c4 := mk("c4", []string{"c3"}, "bob", 500, 6, model.SetStatus{Status: model.StatusDone})
	compacted := []model.PackCommit{c0, c1, c2, cK, c3, c4}
	full := []model.PackCommit{c0, c1, c2, cKempty, c3, c4}

	gotFull, err := fold.Task(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Task(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	if gotFull.HeartbeatLamport != 6 || gotFull.HeartbeatAt != 500 {
		t.Fatalf("heartbeat = (%d,%d), want (6,500)", gotFull.HeartbeatLamport, gotFull.HeartbeatAt)
	}
	if gotFull.StartedAt != 200 || gotFull.ClosedAt != 500 {
		t.Fatalf("started/closed = (%d,%d), want (200,500)", gotFull.StartedAt, gotFull.ClosedAt)
	}
}

// TestFoldTwoConcurrentCheckpoints is the convergence trap: two replicas diverge
// from a common prefix, each appends an op and compacts over its own frontier,
// then a union merge joins both checkpoints. The newest-coverage checkpoint is
// not seed-safe (the other branch's op has a lamport <= its CoversLamport), so
// the fold falls back to the full history where both checkpoints are no-ops and
// every op replays through its original commit. The result must equal folding
// the same history without checkpoints, on every input ordering.
func TestFoldTwoConcurrentCheckpoints(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Tags: []string{"base"}})
	c1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AddTag{Tag: "common"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.SetTitle{Title: "from-a"}, model.AddTag{Tag: "a"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.SetTitle{Title: "from-b"}, model.AddTag{Tag: "b"})

	stateA, err := fold.Note([]model.PackCommit{c0, c1, a1})
	if err != nil {
		t.Fatalf("fold A: %v", err)
	}
	stateB, err := fold.Note([]model.PackCommit{c0, c1, b1})
	if err != nil {
		t.Fatalf("fold B: %v", err)
	}
	ka := cp("ka", "a1", "alice", 250, 4, stateA, 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "bob", 260, 4, stateB, 3, "c0", "c1", "b1")
	merge := mk("mmm", []string{"ka", "kb"}, "alice", 300, 5)
	combined := []model.PackCommit{c0, c1, a1, ka, b1, kb, merge}

	// Equivalent history with no checkpoints: the checkpoint commits become
	// empty no-op commits at the same identities, the merge joins the same tips.
	kaEmpty := mk("ka", []string{"a1"}, "alice", 250, 4)
	kbEmpty := mk("kb", []string{"b1"}, "bob", 260, 4)
	plain := []model.PackCommit{c0, c1, a1, kaEmpty, b1, kbEmpty, merge}

	want, err := fold.Note(plain)
	if err != nil {
		t.Fatalf("fold plain: %v", err)
	}
	got, err := fold.Note(combined)
	if err != nil {
		t.Fatalf("fold combined: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined = %+v\nplain = %+v", got, want)
	}
	// b1 sorts after a1 in linearization (same lamport, later time), so b1's
	// title wins — confirm the covered-only-by-a checkpoint did not drop it.
	if got.Title != "from-b" {
		t.Fatalf("Title = %q, want from-b", got.Title)
	}
	if !slices.Equal(got.Tags, []string{"a", "b", "base", "common"}) {
		t.Fatalf("Tags = %v, want [a b base common]", got.Tags)
	}
	r := rand.New(rand.NewPCG(7, 9))
	for i := range 50 {
		s, err := fold.Note(shuffled(combined, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, s, want)
		}
	}
}

// TestFoldSeedSafeUsesSeed proves the fast path reads State, not the covered
// ops: the checkpoint's State carries a sentinel tag no op in the chain adds.
// A seeded fold surfaces it; a full replay of the covered ops would not.
func TestFoldSeedSafeUsesSeed(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Tags: []string{"keep"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.AddTag{Tag: "more"})
	base, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	seed := base
	seed.Tags = append(slices.Clone(seed.Tags), "sentinel")
	cK := cp("cK", "c1", "compactor", 250, 3, seed, 2, "c0", "c1")
	c2 := mk("c2", []string{"cK"}, "carol", 300, 4, model.AddTag{Tag: "late"})
	got, err := fold.Note([]model.PackCommit{c0, c1, cK, c2})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if !slices.Contains(got.Tags, "sentinel") {
		t.Fatalf("Tags = %v, want sentinel present (seed not used)", got.Tags)
	}
	if !slices.Contains(got.Tags, "late") {
		t.Fatalf("Tags = %v, want late present (suffix not replayed)", got.Tags)
	}
}

// TestFoldCompactionIdempotent compacts at the tip, then compacts the result.
// The folded snapshot is preserved across both — only Head advances to the
// newest checkpoint commit.
func TestFoldCompactionIdempotent(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Tags: []string{"x"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddTag{Tag: "y"})
	base, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, base, 3, "c0", "c1", "c2")
	once, err := fold.Note([]model.PackCommit{c0, c1, c2, cK})
	if err != nil {
		t.Fatalf("fold once: %v", err)
	}
	want := base
	want.Head = "cK"
	if !reflect.DeepEqual(once, want) {
		t.Fatalf("once = %+v, want %+v", once, want)
	}
	cK2 := cp("cK2", "cK", "compactor", 360, 5, once, 4, "c0", "c1", "c2", "cK")
	twice, err := fold.Note([]model.PackCommit{c0, c1, c2, cK, cK2})
	if err != nil {
		t.Fatalf("fold twice: %v", err)
	}
	want.Head = "cK2"
	if !reflect.DeepEqual(twice, want) {
		t.Fatalf("twice = %+v, want %+v", twice, want)
	}
}

// TestFoldCheckpointDoesNotMaskCorruptChain confirms a checkpoint never hides a
// shallow history: dropping a covered commit that a later commit still parents
// fails loudly with ErrMissingParent rather than being papered over by the seed.
func TestFoldCheckpointDoesNotMaskCorruptChain(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddTag{Tag: "y"})
	base, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, base, 3, "c0", "c1", "c2")
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.AddTag{Tag: "z"})
	// c1 is removed but c2 still parents it: the chain is shallow.
	corrupt := []model.PackCommit{c0, c2, cK, c3}
	if _, err := fold.Note(corrupt); !errors.Is(err, fold.ErrMissingParent) {
		t.Fatalf("fold corrupt err = %v, want ErrMissingParent", err)
	}
}
