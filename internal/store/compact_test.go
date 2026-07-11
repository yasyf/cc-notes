package store

import (
	"reflect"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// sameExceptHead asserts two snapshots are equal in every field but Head, which
// compaction legitimately advances.
func sameExceptHead(t *testing.T, got, want model.Snapshot) {
	t.Helper()
	switch g := got.(type) {
	case model.Note:
		w := want.(model.Note)
		g.Head, w.Head = "", ""
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("note (ignoring Head) = %+v, want %+v", g, w)
		}
	case model.Task:
		w := want.(model.Task)
		g.Head, w.Head = "", ""
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("task (ignoring Head) = %+v, want %+v", g, w)
		}
	default:
		t.Fatalf("unexpected snapshot %T", got)
	}
}

func TestCompactAdvancesRef(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "v2"}, model.AddTag{Tag: "x"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	pre, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load pre: %v", err)
	}
	preTip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip pre: %v", err)
	}

	post, err := s.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	newTip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip post: %v", err)
	}
	if newTip == preTip {
		t.Fatalf("ref did not advance: still %s", preTip)
	}
	if post.EntityID() != pre.EntityID() {
		t.Fatalf("id changed across compaction: %s -> %s", pre.EntityID(), post.EntityID())
	}
	if post.(model.Note).Head != newTip {
		t.Fatalf("Head = %s, want new tip %s", post.(model.Note).Head, newTip)
	}
	sameExceptHead(t, post, pre)

	chain, err := s.Repo.ReadChain(ctx, newTip)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	var tip model.PackCommit
	for _, c := range chain {
		if c.SHA == newTip {
			tip = c
		}
	}
	if want := []model.SHA{preTip}; !reflect.DeepEqual(tip.Parents, want) {
		t.Fatalf("checkpoint parents = %v, want %v", tip.Parents, want)
	}
	if len(tip.Pack.Ops) != 1 {
		t.Fatalf("checkpoint carries %d ops, want 1", len(tip.Pack.Ops))
	}
	checkpoint, ok := tip.Pack.Ops[0].(model.Checkpoint)
	if !ok {
		t.Fatalf("tip op = %T, want model.Checkpoint", tip.Pack.Ops[0])
	}
	if checkpoint.EntityID != note.ID {
		t.Fatalf("checkpoint entity = %s, want %s", checkpoint.EntityID, note.ID)
	}
	if checkpoint.CoversLamport != 2 {
		t.Fatalf("CoversLamport = %d, want 2", checkpoint.CoversLamport)
	}
	if len(checkpoint.CoversShas) != 2 {
		t.Fatalf("CoversShas = %v, want 2 covered commits", checkpoint.CoversShas)
	}

	reloaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load post: %v", err)
	}
	if !reflect.DeepEqual(reloaded, post) {
		t.Fatalf("Load = %+v, want %+v", reloaded, post)
	}
}

func TestCompactFreshCloneFoldsIdentically(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	task := create(t, s, taskOps("ship it", "main")).(model.Task)
	ref := refs.For(model.KindTask, task.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.Claim{Assignee: testActor}, model.AddComment{Body: "mine"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	post, err := s.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	clone := t.TempDir()
	gittest.Git(t, clone, "init", "-q", "-b", "main")
	gittest.Git(t, clone, "config", "user.name", testName)
	gittest.Git(t, clone, "config", "user.email", testEmail)
	gittest.Git(t, clone, "fetch", s.Git.Dir, ref+":"+ref)

	cs, err := Open(clone)
	if err != nil {
		t.Fatalf("Open clone: %v", err)
	}
	got, err := cs.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load clone: %v", err)
	}
	if !reflect.DeepEqual(got, post) {
		t.Fatalf("clone fold = %+v, want %+v", got, post)
	}
}

func TestCompactIdempotent(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "x"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	first, err := s.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact first: %v", err)
	}
	second, err := s.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact second: %v", err)
	}
	sameExceptHead(t, second, first)
	if second.(model.Note).Head == first.(model.Note).Head {
		t.Fatalf("second compaction did not advance Head: %s", second.(model.Note).Head)
	}
}

func TestCompactConcurrentCAS(t *testing.T) {
	s := initStore(t)
	gittest.Git(t, s.Git.Dir, "config", "core.filesRefLockTimeout", "3000")
	ctx := t.Context()
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "v2"}, model.AddTag{Tag: "x"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	pre, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load pre: %v", err)
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Compact(ctx, ref)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("compact %d: %v", i, err)
		}
	}

	final, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load final: %v", err)
	}
	sameExceptHead(t, final, pre)
}
