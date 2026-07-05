package trail_test

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

func mk(sha string, parents []string, author string, at int64, lamport uint64, ops ...model.Op) model.PackCommit {
	ps := make([]model.SHA, len(parents))
	for i, p := range parents {
		ps[i] = model.SHA(p)
	}
	return model.PackCommit{
		SHA:        model.SHA(sha),
		Parents:    ps,
		Author:     model.Actor(author),
		AuthorTime: at,
		Pack:       model.Pack{Lamport: model.Lamport(lamport), Ops: ops},
	}
}

func cp(sha, parent, author string, at int64, lamport uint64, state model.Snapshot, coversLamport uint64, covers ...string) model.PackCommit {
	shas := make([]model.SHA, len(covers))
	for i, c := range covers {
		shas[i] = model.SHA(c)
	}
	op := model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: model.Lamport(coversLamport), CoversShas: shas}
	return mk(sha, []string{parent}, author, at, lamport, op)
}

func entriesOf(t *testing.T, chain []model.PackCommit) []trail.Entry {
	t.Helper()
	steps, err := fold.History(chain)
	if err != nil {
		t.Fatalf("fold.History: %v", err)
	}
	entries, err := trail.Entries(steps)
	if err != nil {
		t.Fatalf("trail.Entries: %v", err)
	}
	return entries
}

func changeByField(e trail.Entry, field string) (trail.Change, bool) {
	for _, c := range e.Changes {
		if c.Field == field {
			return c, true
		}
	}
	return trail.Change{}, false
}

// TestEntriesNoteTrail pins the trail of a linear note chain: the create renders
// every initial scalar "from nothing" (From cleared) and the initial tags as a
// set, the title edit reports a scalar From→To, the tag edit reports sorted
// Added/Removed, the anchor edit summarizes the object element, the hidden Head
// field never surfaces, and each entry carries its folded post-state Snapshot.
func TestEntriesNoteTrail(t *testing.T) {
	chain := []model.PackCommit{
		mk("n0", nil, "alice", 100, 1, model.CreateNote{Nonce: "x", Title: "T", Body: "B", Tags: []string{"a", "b"}}),
		mk("n1", []string{"n0"}, "bob", 200, 2, model.SetTitle{Title: "T2"}),
		mk("n2", []string{"n1"}, "carol", 250, 3, model.SetTitle{Title: "T2"}),
		mk("n3", []string{"n2"}, "dave", 300, 4, model.AddTag{Tag: "c"}, model.RemoveTag{Tag: "a"}),
		mk("n4", []string{"n3"}, "erin", 350, 5, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}}),
	}
	entries := entriesOf(t, chain)

	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4 (n2 idempotent title set suppressed)", len(entries))
	}

	create := entries[0]
	if create.Kind != "create" || create.Commit.SHA != "n0" {
		t.Fatalf("entries[0] = {kind %q sha %q}, want {create n0}", create.Kind, create.Commit.SHA)
	}
	if c, ok := changeByField(create, "title"); !ok || !c.Scalar || c.From != "" || c.To != "T" {
		t.Errorf("create title change = %+v, want scalar from \"\" to \"T\"", c)
	}
	if c, ok := changeByField(create, "body"); !ok || !c.Scalar || c.From != "" || c.To != "B" {
		t.Errorf("create body change = %+v, want scalar from \"\" to \"B\"", c)
	}
	if c, ok := changeByField(create, "tags"); !ok || c.Scalar || len(c.Added) != 2 || c.Added[0] != "a" || c.Added[1] != "b" || len(c.Removed) != 0 {
		t.Errorf("create tags change = %+v, want set added [a b]", c)
	}
	if _, ok := changeByField(create, "head"); ok {
		t.Errorf("create entry surfaced the hidden head field: %+v", create.Changes)
	}
	if n, ok := create.Snapshot.(model.Note); !ok || n.Title != "T" {
		t.Errorf("create snapshot = %#v, want model.Note titled T", create.Snapshot)
	}

	edit := entries[1]
	if edit.Kind != "edit" || edit.Commit.SHA != "n1" {
		t.Fatalf("entries[1] = {kind %q sha %q}, want {edit n1}", edit.Kind, edit.Commit.SHA)
	}
	if c, ok := changeByField(edit, "title"); !ok || !c.Scalar || c.From != "T" || c.To != "T2" {
		t.Errorf("title edit change = %+v, want scalar T → T2", c)
	}

	tagEdit := entries[2]
	if tagEdit.Commit.SHA != "n3" {
		t.Fatalf("entries[2] sha = %q, want n3 (idempotent n2 suppressed)", tagEdit.Commit.SHA)
	}
	if c, ok := changeByField(tagEdit, "tags"); !ok || c.Scalar || len(c.Added) != 1 || c.Added[0] != "c" || len(c.Removed) != 1 || c.Removed[0] != "a" {
		t.Errorf("tag edit change = %+v, want added [c] removed [a]", c)
	}

	anchorEdit := entries[3]
	if anchorEdit.Commit.SHA != "n4" {
		t.Fatalf("entries[3] sha = %q, want n4", anchorEdit.Commit.SHA)
	}
	if c, ok := changeByField(anchorEdit, "anchors"); !ok || c.Scalar || len(c.Added) != 1 || c.Added[0] != "path:x.go" {
		t.Errorf("anchor edit change = %+v, want added [path:x.go]", c)
	}
}

// TestEntriesTaskLifecycleSkipsBookkeeping drives a task through claim, a lease
// heartbeat, and an idempotent priority set, then two real edits: the heartbeat
// (a bookkeeping-only commit) and the idempotent set change no visible field and
// stay out of the trail, while the create renders the numeric default priority
// as a plain set rather than "0 → 2".
func TestEntriesTaskLifecycleSkipsBookkeeping(t *testing.T) {
	chain := []model.PackCommit{
		mk("t0", nil, "alice", 100, 1, model.CreateTask{Nonce: "x", Title: "ship", Type: model.TypeTask, Priority: 2, Branch: "main", Labels: []string{"backend"}}),
		mk("t1", []string{"t0"}, "agent-b", 200, 2, model.Claim{Assignee: "agent-b"}),
		mk("t2", []string{"t1"}, "agent-b", 250, 3, model.Renew{}),
		mk("t3", []string{"t2"}, "alice", 300, 4, model.SetPriority{Priority: 2}),
		mk("t4", []string{"t3"}, "alice", 350, 5, model.SetPriority{Priority: 3}),
		mk("t5", []string{"t4"}, "alice", 400, 6, model.SetStatus{Status: model.StatusDone}),
	}
	entries := entriesOf(t, chain)

	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4 (heartbeat t2 and idempotent t3 suppressed)", len(entries))
	}
	wantSHA := []model.SHA{"t0", "t1", "t4", "t5"}
	wantKind := []string{"create", "edit", "edit", "edit"}
	for i, e := range entries {
		if e.Commit.SHA != wantSHA[i] {
			t.Errorf("entries[%d] sha = %q, want %q", i, e.Commit.SHA, wantSHA[i])
		}
		if e.Kind != wantKind[i] {
			t.Errorf("entries[%d] kind = %q, want %q", i, e.Kind, wantKind[i])
		}
	}

	create := entries[0]
	if c, ok := changeByField(create, "priority"); !ok || !c.Scalar || c.From != "" || c.To != "2" {
		t.Errorf("create priority change = %+v, want scalar from \"\" to \"2\" (not \"0 → 2\")", c)
	}
	if c, ok := changeByField(create, "status"); !ok || !c.Scalar || c.From != "" || c.To != "open" {
		t.Errorf("create status change = %+v, want scalar from \"\" to \"open\"", c)
	}
	if c, ok := changeByField(create, "labels"); !ok || c.Scalar || len(c.Added) != 1 || c.Added[0] != "backend" {
		t.Errorf("create labels change = %+v, want set added [backend]", c)
	}

	claim := entries[1]
	if c, ok := changeByField(claim, "status"); !ok || !c.Scalar || c.From != "open" || c.To != "in_progress" {
		t.Errorf("claim status change = %+v, want open → in_progress", c)
	}
	if c, ok := changeByField(claim, "assignee"); !ok || !c.Scalar || c.From != "" || c.To != "agent-b" {
		t.Errorf("claim assignee change = %+v, want \"\" → agent-b", c)
	}

	if c, ok := changeByField(entries[2], "priority"); !ok || c.From != "2" || c.To != "3" {
		t.Errorf("priority edit change = %+v, want 2 → 3", c)
	}
	if c, ok := changeByField(entries[3], "status"); !ok || c.From != "in_progress" || c.To != "done" {
		t.Errorf("done edit change = %+v, want in_progress → done", c)
	}
	if task, ok := entries[3].Snapshot.(model.Task); !ok || task.Status != model.StatusDone {
		t.Errorf("done entry snapshot = %#v, want model.Task status done", entries[3].Snapshot)
	}
}

// TestEntriesCheckpoint compacts a note chain and checks that the checkpoint
// commit lands as a state-neutral marker — kind "checkpoint", the covered-commit
// count, and no field changes — while the real edits on either side survive.
func TestEntriesCheckpoint(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "x", Title: "T0", Body: "B0"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"})
	state, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold.Note: %v", err)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
	c2 := mk("c2", []string{"cK"}, "carol", 300, 4, model.SetBody{Body: "B2"})
	entries := entriesOf(t, []model.PackCommit{c0, c1, cK, c2})

	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4", len(entries))
	}
	ck := entries[2]
	if ck.Commit.SHA != "cK" || ck.Kind != "checkpoint" {
		t.Fatalf("entries[2] = {kind %q sha %q}, want {checkpoint cK}", ck.Kind, ck.Commit.SHA)
	}
	if ck.Covers != 2 {
		t.Errorf("checkpoint covers = %d, want 2", ck.Covers)
	}
	if len(ck.Changes) != 0 {
		t.Errorf("checkpoint changes = %+v, want none", ck.Changes)
	}
	if c, ok := changeByField(entries[1], "title"); !ok || c.From != "T0" || c.To != "T1" {
		t.Errorf("edit before checkpoint = %+v, want T0 → T1", c)
	}
	if c, ok := changeByField(entries[3], "body"); !ok || c.From != "B0" || c.To != "B2" {
		t.Errorf("edit after checkpoint = %+v, want B0 → B2", c)
	}
}

// TestEntriesEmpty checks the boundary: no steps yields no entries and no error.
func TestEntriesEmpty(t *testing.T) {
	entries, err := trail.Entries(nil)
	if err != nil {
		t.Fatalf("Entries(nil) error = %v, want nil", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Entries(nil) = %d entries, want 0", len(entries))
	}
}

// TestIsCheckpoint pins the classifier: only an all-Checkpoint commit is a
// marker; an empty pack, a plain edit, and a mixed pack are not.
func TestIsCheckpoint(t *testing.T) {
	note := model.Note{ID: "n0"}
	cases := []struct {
		name string
		ops  []model.Op
		want bool
	}{
		{"only checkpoint", []model.Op{model.Checkpoint{EntityID: "n0", State: note}}, true},
		{"empty pack", nil, false},
		{"plain edit", []model.Op{model.SetTitle{Title: "T"}}, false},
		{"mixed", []model.Op{model.Checkpoint{EntityID: "n0", State: note}, model.SetTitle{Title: "T"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := model.PackCommit{Pack: model.Pack{Ops: tc.ops}}
			if got := trail.IsCheckpoint(c); got != tc.want {
				t.Errorf("IsCheckpoint(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestEntityKind pins the snapshot→kind name mapping for every entity kind.
func TestEntityKind(t *testing.T) {
	cases := []struct {
		name string
		snap model.Snapshot
		want string
	}{
		{"note", model.Note{}, "note"},
		{"doc", model.Doc{}, "doc"},
		{"log", model.Log{}, "log"},
		{"task", model.Task{}, "task"},
		{"sprint", model.Sprint{}, "sprint"},
		{"project", model.Project{}, "project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trail.EntityKind(tc.snap); got != tc.want {
				t.Errorf("EntityKind(%T) = %q, want %q", tc.snap, got, tc.want)
			}
		})
	}
}
