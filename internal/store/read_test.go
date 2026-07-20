package store

import (
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// asSnapshots widens a typed ListX result to []model.Snapshot so it can be
// compared against ListSnapshots.
func asSnapshots[T model.Snapshot](xs []T, err error) ([]model.Snapshot, error) {
	if err != nil {
		return nil, err
	}
	out := make([]model.Snapshot, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out, nil
}

// TestListSnapshotsMatchesTyped proves the kind-generic ListSnapshots returns
// exactly what each typed ListX returns — same entities, same order — for every
// kind, honoring the inclusion filter (a deleted note and a superseded doc are
// hidden by default and shown when the matching opt is set).
func TestListSnapshotsMatchesTyped(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()

	// Two of each lifecycle kind so ordering matters; a deleted note and a
	// superseded doc so the filter matters.
	create(t, s, noteOps("n-keep"))
	nDel := create(t, s, noteOps("n-del")).(model.Note)
	if _, err := s.Append(ctx, refs.For(model.KindNote, nDel.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	dKeep := create(t, s, docOps("d-keep")).(model.Doc)
	dSup := create(t, s, docOps("d-sup")).(model.Doc)
	if _, err := s.Append(ctx, refs.For(model.KindDoc, dSup.ID), []model.Op{model.AddSupersededBy{ID: dKeep.ID}}); err != nil {
		t.Fatalf("supersede doc: %v", err)
	}
	create(t, s, logOps("l1"))
	create(t, s, logOps("l2"))
	create(t, s, taskOps("t1", "main"))
	create(t, s, taskOps("t2", "main"))
	create(t, s, sprintOps("sp1"))
	create(t, s, projectOps("pr1"))
	create(t, s, runbookOps("rb1"))
	create(t, s, investigationOps("iv1"))

	cases := []struct {
		kind  model.Kind
		opts  ListOpts
		typed func() ([]model.Snapshot, error)
	}{
		{model.KindNote, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListNotes(ctx, false, false)) }},
		{model.KindNote, ListOpts{IncludeDeleted: true}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListNotes(ctx, true, false)) }},
		{model.KindDoc, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListDocs(ctx, false, false)) }},
		{model.KindDoc, ListOpts{IncludeSuperseded: true}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListDocs(ctx, false, true)) }},
		{model.KindLog, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListLogs(ctx, false)) }},
		{model.KindTask, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListTasks(ctx)) }},
		{model.KindSprint, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListSprints(ctx)) }},
		{model.KindProject, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListProjects(ctx)) }},
		{model.KindRunbook, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListRunbooks(ctx)) }},
		{model.KindInvestigation, ListOpts{}, func() ([]model.Snapshot, error) { return asSnapshots(s.ListInvestigations(ctx)) }},
	}
	seen := map[model.Kind]bool{}
	for _, tc := range cases {
		seen[tc.kind] = true
		want, err := tc.typed()
		if err != nil {
			t.Fatalf("%s typed list: %v", tc.kind, err)
		}
		got, err := s.ListSnapshots(ctx, tc.kind, tc.opts)
		if err != nil {
			t.Fatalf("ListSnapshots(%s): %v", tc.kind, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ListSnapshots(%s, %+v) =\n%#v\nwant\n%#v", tc.kind, tc.opts, got, want)
		}
	}
	for _, k := range model.Kinds() {
		if !seen[k] {
			t.Errorf("kind %q not covered by the ListSnapshots differential", k)
		}
	}
}

func TestLoadRootedAtPinsImmutableTip(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	created := create(t, s, noteOps("before")).(model.Note)
	ref := refs.For(model.KindNote, created.ID)
	before, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip before append: %v", err)
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "after"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rooted, err := s.LoadRootedAt(ctx, before)
	if err != nil {
		t.Fatalf("LoadRootedAt: %v", err)
	}
	if rooted.Snapshot.(model.Note).Title != "before" {
		t.Fatalf("pinned title = %q, want before", rooted.Snapshot.(model.Note).Title)
	}
	if rooted.Root.SHA != before {
		t.Fatalf("root sha = %s, want %s", rooted.Root.SHA, before)
	}
}
