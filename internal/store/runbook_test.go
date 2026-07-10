package store

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func runbookOps(title string) []model.Op {
	return []model.Op{model.CreateRunbook{Nonce: model.NewNonce(), Title: title}}
}

func TestCreateRunbookRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{
		model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "ship it", Labels: []string{"b", "a"}},
		model.AddStep{ID: "s1", Text: "build", Command: "make", Position: "i"},
		model.AddStep{ID: "s2", Text: "test", Command: "", Position: "a"},
	}
	snapshot := create(t, s, ops)
	rb, ok := snapshot.(model.Runbook)
	if !ok {
		t.Fatalf("Create returned %T, want model.Runbook", snapshot)
	}

	if rb.Title != "Deploy" || rb.Description != "ship it" {
		t.Errorf("runbook = %q/%q, want Deploy/ship it", rb.Title, rb.Description)
	}
	if rb.Status != model.RunbookActive {
		t.Errorf("Status = %q, want %q", rb.Status, model.RunbookActive)
	}
	wantSteps := []model.RunbookStep{
		{ID: "s2", Text: "test", Command: "", Position: "a"},
		{ID: "s1", Text: "build", Command: "make", Position: "i"},
	}
	if !reflect.DeepEqual(rb.Steps, wantSteps) {
		t.Errorf("Steps = %+v, want %+v (sorted by position)", rb.Steps, wantSteps)
	}
	if rb.Runs == nil || len(rb.Runs) != 0 {
		t.Errorf("Runs = %+v, want empty non-nil", rb.Runs)
	}
	if rb.Comments == nil || len(rb.Comments) != 0 {
		t.Errorf("Comments = %+v, want empty non-nil", rb.Comments)
	}
	if want := []string{"a", "b"}; !slices.Equal(rb.Labels, want) {
		t.Errorf("Labels = %v, want %v", rb.Labels, want)
	}
	if rb.Author != testActor {
		t.Errorf("Author = %q, want %q", rb.Author, testActor)
	}
	if rb.Head != model.SHA(rb.ID) {
		t.Errorf("Head = %s, want root %s", rb.Head, rb.ID)
	}
	if rb.CreatedAt == 0 || rb.UpdatedAt != rb.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", rb.CreatedAt, rb.UpdatedAt)
	}

	ref := refs.Runbook(rb.ID)
	if got := mustGit(t, s.Git.Dir, "rev-parse", ref); got != string(rb.ID) {
		t.Errorf("ref %s -> %s, want %s", ref, got, rb.ID)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := mustGit(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: runbook create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: runbook create")
	}

	list, err := s.ListRunbooks(t.Context())
	if err != nil {
		t.Fatalf("ListRunbooks: %v", err)
	}
	if want := []model.Runbook{rb}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListRunbooks = %+v, want %+v", list, want)
	}
}

func TestDedupeRunbook(t *testing.T) {
	base := []model.Op{
		model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "D", Labels: []string{"a"}},
		model.AddStep{ID: "s1", Text: "build", Command: "make", Position: "a"},
		model.AddStep{ID: "s2", Text: "test", Command: "", Position: "i"},
	}
	for _, tc := range []struct {
		name       string
		ops        []model.Op
		wantDedupe bool
	}{
		{
			name: "exact",
			ops: []model.Op{
				model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "D", Labels: []string{"a"}},
				model.AddStep{ID: "x1", Text: "build", Command: "make", Position: "a"},
				model.AddStep{ID: "x2", Text: "test", Command: "", Position: "i"},
			},
			wantDedupe: true,
		},
		{
			name: "same steps different position encodings",
			ops: []model.Op{
				model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "D", Labels: []string{"a"}},
				model.AddStep{ID: "y1", Text: "build", Command: "make", Position: "5"},
				model.AddStep{ID: "y2", Text: "test", Command: "", Position: "z"},
			},
			wantDedupe: true,
		},
		{
			name: "diff title",
			ops: []model.Op{
				model.CreateRunbook{Nonce: model.NewNonce(), Title: "Release", Description: "D", Labels: []string{"a"}},
				model.AddStep{ID: "z1", Text: "build", Command: "make", Position: "a"},
				model.AddStep{ID: "z2", Text: "test", Command: "", Position: "i"},
			},
			wantDedupe: false,
		},
		{
			name: "diff step text",
			ops: []model.Op{
				model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "D", Labels: []string{"a"}},
				model.AddStep{ID: "w1", Text: "compile", Command: "make", Position: "a"},
				model.AddStep{ID: "w2", Text: "test", Command: "", Position: "i"},
			},
			wantDedupe: false,
		},
		{
			name: "extra step",
			ops: []model.Op{
				model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy", Description: "D", Labels: []string{"a"}},
				model.AddStep{ID: "v1", Text: "build", Command: "make", Position: "a"},
				model.AddStep{ID: "v2", Text: "test", Command: "", Position: "i"},
				model.AddStep{ID: "v3", Text: "ship", Command: "", Position: "t"},
			},
			wantDedupe: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, base)
			if tc.wantDedupe {
				got := mustDedupe(t, s, tc.ops)
				if got.EntityID() != first.EntityID() {
					t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.EntityID())
				}
				return
			}
			got := create(t, s, tc.ops)
			if got.EntityID() == first.EntityID() {
				t.Errorf("distinct content reused id %s", got.EntityID())
			}
		})
	}
}

func TestDedupeRunbookSkipsArchived(t *testing.T) {
	s := initStore(t)
	first := create(t, s, runbookOps("Deploy")).(model.Runbook)
	if _, err := s.Append(t.Context(), refs.Runbook(first.ID), []model.Op{model.SetRunbookStatus{Status: model.RunbookArchived}}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got := create(t, s, runbookOps("Deploy"))
	if got.EntityID() == first.ID {
		t.Errorf("archived twin suppressed re-create: reused id %s", got.EntityID())
	}
}

func TestDedupeRunbookSkipsRunPack(t *testing.T) {
	s := initStore(t)
	first := create(t, s, runbookOps("Deploy"))
	pack := []model.Op{
		model.CreateRunbook{Nonce: model.NewNonce(), Title: "Deploy"},
		model.StartRun{ID: "r1", Task: "task0"},
	}
	got := create(t, s, pack)
	if got.EntityID() == first.EntityID() {
		t.Errorf("run-bundling pack was deduped; reused id %s", got.EntityID())
	}
}

func TestResolveRunbook(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	titles := map[model.EntityID]string{}
	buckets := map[byte][]model.EntityID{}
	var shared []model.EntityID
	for i := 0; len(shared) == 0; i++ {
		if i > 17 {
			t.Fatal("no shared 1-char prefix after 17 creates")
		}
		title := fmt.Sprintf("runbook-%d", i)
		rb := create(t, s, runbookOps(title)).(model.Runbook)
		titles[rb.ID] = title
		first := rb.ID[0]
		buckets[first] = append(buckets[first], rb.ID)
		if len(buckets[first]) == 2 {
			shared = buckets[first]
		}
	}

	full := shared[0]
	got, err := s.Resolve(ctx, refs.KindRunbook, string(full))
	if err != nil {
		t.Fatalf("Resolve(%q): %v", full, err)
	}
	if want := refs.Runbook(full); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", full, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindRunbook, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	prefix := string(shared[0])[:1]
	_, err = s.Resolve(ctx, refs.KindRunbook, prefix)
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", prefix, err)
	}
	if ambiguous.Kind != refs.KindRunbook || ambiguous.Prefix != prefix {
		t.Errorf("AmbiguousError = %+v, want kind runbook prefix %q", ambiguous, prefix)
	}
	slices.Sort(shared)
	want := []Candidate{
		{ID: shared[0], Title: titles[shared[0]]},
		{ID: shared[1], Title: titles[shared[1]]},
	}
	if !reflect.DeepEqual(ambiguous.Candidates, want) {
		t.Errorf("Candidates = %+v, want %+v", ambiguous.Candidates, want)
	}
}

func TestMergeRunbook(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	rb := create(t, s, runbookOps("Deploy")).(model.Runbook)
	ref := refs.Runbook(rb.ID)

	snapshot, err := s.Append(ctx, ref, []model.Op{
		model.StartRun{ID: "r1", Task: "taskA"},
		model.FinishRun{ID: "r1", Status: model.RunSucceeded},
	})
	if err != nil {
		t.Fatalf("Append ours: %v", err)
	}
	ours := snapshot.(model.Runbook).Head

	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	theirs, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{model.SHA(rb.ID)}, sig, "cc-notes: run",
		model.Pack{Lamport: 2, Ops: []model.Op{
			model.StartRun{ID: "r2", Task: "taskB"},
			model.FinishRun{ID: "r2", Status: model.RunFailed},
		}})
	if err != nil {
		t.Fatalf("WriteOpsCommit theirs: %v", err)
	}

	if _, err := s.Merge(ctx, ref, ours, theirs); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	merged := loaded.(model.Runbook)
	if len(merged.Runs) != 2 {
		t.Fatalf("Runs = %+v, want 2 surviving runs", merged.Runs)
	}
	byID := map[string]model.RunStatus{}
	for _, r := range merged.Runs {
		byID[r.ID] = r.Status
	}
	if byID["r1"] != model.RunSucceeded {
		t.Errorf("r1 status = %q, want succeeded", byID["r1"])
	}
	if byID["r2"] != model.RunFailed {
		t.Errorf("r2 status = %q, want failed", byID["r2"])
	}
}

func TestFoldCacheRoundTripStructuralKinds(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	sprintTip := model.SHA("aaaa333333333333333333333333333333333333")
	sprint := model.Sprint{
		ID: "sprintid", Project: "proj0", Title: "Q3", Description: "d",
		Status: model.SprintActive, StartDate: 1000, EndDate: 2000,
		Labels: []string{"a"}, Commits: []model.SHA{"sha1"},
		Comments:  []model.Comment{{Author: testActor, TS: 5, Body: "kickoff"}},
		Author:    testActor,
		CreatedAt: 1, UpdatedAt: 2, StartedAt: 3, Head: sprintTip,
	}
	c.put(sprintTip, sprint)
	if got, ok := c.get(sprintTip); !ok || !reflect.DeepEqual(got, sprint) {
		t.Fatalf("sprint round-trip: ok=%v got=%#v want=%#v", ok, got, sprint)
	}

	projectTip := model.SHA("bbbb333333333333333333333333333333333333")
	project := model.Project{
		ID: "projectid", Title: "Platform", Description: "infra",
		Status: model.ProjectActive, Labels: []string{"x"}, Commits: []model.SHA{"sha2"},
		Comments:  []model.Comment{{Author: testActor, TS: 6, Body: "start"}},
		Author:    testActor,
		CreatedAt: 1, UpdatedAt: 2, Head: projectTip,
	}
	c.put(projectTip, project)
	if got, ok := c.get(projectTip); !ok || !reflect.DeepEqual(got, project) {
		t.Fatalf("project round-trip: ok=%v got=%#v want=%#v", ok, got, project)
	}

	runbookTip := model.SHA("cccc333333333333333333333333333333333333")
	runbook := model.Runbook{
		ID: "runbookid", Title: "Deploy", Description: "ship", Status: model.RunbookArchived,
		Steps: []model.RunbookStep{{ID: "s1", Text: "build", Command: "make", Position: "a"}},
		Runs: []model.RunbookRun{{
			ID: "r1", Task: "task0", Status: model.RunSucceeded,
			Runner: testActor, StartedAt: 10, FinishedAt: 20,
			Results: []model.RunbookStepResult{{StepID: "s1", Status: model.StepDone, Note: "ok", Actor: testActor, TS: 15}},
		}},
		Labels: []string{"ops"}, Comments: []model.Comment{{Author: testActor, TS: 7, Body: "note"}},
		Author: testActor, CreatedAt: 1, UpdatedAt: 20, ArchivedAt: 25, Head: runbookTip,
	}
	c.put(runbookTip, runbook)
	if got, ok := c.get(runbookTip); !ok || !reflect.DeepEqual(got, runbook) {
		t.Fatalf("runbook round-trip: ok=%v got=%#v want=%#v", ok, got, runbook)
	}
}
