package notes_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func TestBlame(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	commit := func(messages ...string) model.SHA {
		t.Helper()
		args := make([]string, 0, 3+2*len(messages))
		args = append(args, "commit", "--allow-empty", "-q")
		for _, m := range messages {
			args = append(args, "-m", m)
		}
		gittest.Git(t, dir, args...)
		return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	}

	// A task whose linked commits include the sha (DoneTask links current HEAD).
	linked := mustTask(t, c, notes.TaskSpec{Title: "linked", Branch: "main"})
	shaCommits := commit("implement linked")
	if _, err := c.DoneTask(ctx, linked.ID, true); err != nil {
		t.Fatalf("DoneTask(linked): %v", err)
	}

	// A task attributed only through a cc-task trailer, never linked.
	trailed := mustTask(t, c, notes.TaskSpec{Title: "trailed", Branch: "main"})
	shaTrailer := commit("work", "cc-task: "+trailed.ID.Short())

	// A task both linked and trailered on one commit: it must appear once.
	dup := mustTask(t, c, notes.TaskSpec{Title: "dup", Branch: "main"})
	shaDup := commit("dup work", "cc-task: "+dup.ID.Short())
	if _, err := c.DoneTask(ctx, dup.ID, true); err != nil {
		t.Fatalf("DoneTask(dup): %v", err)
	}

	// Two tasks linked to one commit exercise the priority-ascending sort.
	lowPri := mustTask(t, c, notes.TaskSpec{Title: "low", Branch: "main", Priority: 3})
	highPri := mustTask(t, c, notes.TaskSpec{Title: "high", Branch: "main", Priority: 1})
	shaSort := commit("sort work")
	if _, err := c.DoneTask(ctx, lowPri.ID, true); err != nil {
		t.Fatalf("DoneTask(lowPri): %v", err)
	}
	if _, err := c.DoneTask(ctx, highPri.ID, true); err != nil {
		t.Fatalf("DoneTask(highPri): %v", err)
	}

	tests := []struct {
		name    string
		rev     string
		wantSHA model.SHA
		wantIDs []model.EntityID
	}{
		{"commit link", string(shaCommits), shaCommits, []model.EntityID{linked.ID}},
		{"trailer", string(shaTrailer), shaTrailer, []model.EntityID{trailed.ID}},
		{"dedup", string(shaDup), shaDup, []model.EntityID{dup.ID}},
		{"sorted by priority", string(shaSort), shaSort, []model.EntityID{highPri.ID, lowPri.ID}},
		{"short rev resolves to full sha", string(shaCommits)[:12], shaCommits, []model.EntityID{linked.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSHA, gotTasks, err := c.Blame(ctx, tc.rev)
			if err != nil {
				t.Fatalf("Blame(%s): %v", tc.rev, err)
			}
			if gotSHA != tc.wantSHA {
				t.Errorf("sha = %q, want %q", gotSHA, tc.wantSHA)
			}
			gotIDs := make([]model.EntityID, len(gotTasks))
			for i, task := range gotTasks {
				gotIDs[i] = task.ID
			}
			if !slices.Equal(gotIDs, tc.wantIDs) {
				t.Errorf("task ids = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}

	// An unknown rev fails with ErrNotFound naming the rev, matching the CLI text.
	unknown := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if _, _, err := c.Blame(ctx, unknown); !errors.Is(err, notes.ErrNotFound) {
		t.Fatalf("Blame(unknown) = %v, want ErrNotFound", err)
	} else if want := "no commit " + unknown; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want to contain %q", err.Error(), want)
	}
}

func TestBlameInvestigations(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	commit := func(messages ...string) model.SHA {
		t.Helper()
		args := make([]string, 0, 3+2*len(messages))
		args = append(args, "commit", "--allow-empty", "-q")
		for _, m := range messages {
			args = append(args, "-m", m)
		}
		gittest.Git(t, dir, args...)
		return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	}

	// An investigation whose fix commit is the sha.
	fixed := mustInvestigation(t, c, notes.InvestigationSpec{Title: "fix-linked", Premise: "p1"})
	if _, err := c.RootCause(ctx, fixed.ID, "cause"); err != nil {
		t.Fatalf("RootCause fixed: %v", err)
	}
	shaFix := commit("implement fix")
	if _, err := c.Fix(ctx, fixed.ID, "", []string{string(shaFix)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// An investigation attributed only through a cc-investigation trailer.
	trailed := mustInvestigation(t, c, notes.InvestigationSpec{Title: "trailed", Premise: "p2"})
	shaTrailer := commit("work", "cc-investigation: "+trailed.ID.Short())

	// An investigation both fix-linked and trailered on one commit: it must appear
	// once, not twice.
	dup := mustInvestigation(t, c, notes.InvestigationSpec{Title: "dup", Premise: "p3"})
	if _, err := c.RootCause(ctx, dup.ID, "cause"); err != nil {
		t.Fatalf("RootCause dup: %v", err)
	}
	shaDup := commit("dup work", "cc-investigation: "+dup.ID.Short())
	if _, err := c.Fix(ctx, dup.ID, "", []string{string(shaDup)}); err != nil {
		t.Fatalf("Fix dup: %v", err)
	}

	tests := []struct {
		name    string
		rev     string
		wantSHA model.SHA
		wantIDs []model.EntityID
	}{
		{"fix commit", string(shaFix), shaFix, []model.EntityID{fixed.ID}},
		{"trailer", string(shaTrailer), shaTrailer, []model.EntityID{trailed.ID}},
		{"dedup", string(shaDup), shaDup, []model.EntityID{dup.ID}},
		{"short rev resolves to full sha", string(shaFix)[:12], shaFix, []model.EntityID{fixed.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSHA, gotInvs, err := c.BlameInvestigations(ctx, tc.rev)
			if err != nil {
				t.Fatalf("BlameInvestigations(%s): %v", tc.rev, err)
			}
			if gotSHA != tc.wantSHA {
				t.Errorf("sha = %q, want %q", gotSHA, tc.wantSHA)
			}
			gotIDs := make([]model.EntityID, len(gotInvs))
			for i, inv := range gotInvs {
				gotIDs[i] = inv.ID
			}
			if !slices.Equal(gotIDs, tc.wantIDs) {
				t.Errorf("investigation ids = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}

	unknown := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if _, _, err := c.BlameInvestigations(ctx, unknown); !errors.Is(err, notes.ErrNotFound) {
		t.Fatalf("BlameInvestigations(unknown) = %v, want ErrNotFound", err)
	}
}
