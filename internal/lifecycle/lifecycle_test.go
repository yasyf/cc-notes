package lifecycle_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/lifecycle"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

func scalar(field string, from, to any) trail.Change {
	return trail.Change{Field: field, Scalar: true, From: from, To: to}
}

func set(field string, added ...any) trail.Change {
	return trail.Change{Field: field, Added: added}
}

func types(events []lifecycle.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func TestClassifyEntryKinds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry trail.Entry
		want  []string
	}{
		{
			"task create",
			trail.Entry{Kind: "create", Snapshot: model.Task{}},
			[]string{lifecycle.TypeCreated},
		},
		{
			"task claim",
			trail.Entry{Kind: "edit", Snapshot: model.Task{Status: model.StatusInProgress}, Changes: []trail.Change{
				scalar("status", "open", string(model.StatusInProgress)),
				scalar("assignee", nil, "alice <alice@example.com>"),
			}},
			[]string{lifecycle.TypeClaimed},
		},
		{
			"task reclaim keeps in-progress without a status delta",
			trail.Entry{Kind: "edit", Snapshot: model.Task{Status: model.StatusInProgress}, Changes: []trail.Change{
				scalar("assignee", "alice <alice@example.com>", "bob <bob@example.com>"),
			}},
			[]string{lifecycle.TypeReclaimed},
		},
		{
			"task close",
			trail.Entry{Kind: "edit", Snapshot: model.Task{}, Changes: []trail.Change{
				scalar("status", string(model.StatusInProgress), string(model.StatusDone)),
			}},
			[]string{lifecycle.TypeClosed},
		},
		{
			"task branch move and commit link accumulate",
			trail.Entry{Kind: "edit", Snapshot: model.Task{}, Changes: []trail.Change{
				scalar("branch", "wip", "main"),
				set("commits", "deadbeef"),
			}},
			[]string{lifecycle.TypeBranchMoved, lifecycle.TypeCommitLinked},
		},
		{
			"note verify wins over the stale it clears",
			trail.Entry{Kind: "edit", Snapshot: model.Note{}, Changes: []trail.Change{
				scalar("verified_at", nil, float64(1)),
				scalar("stale_at", float64(1), nil),
			}},
			[]string{lifecycle.TypeVerified},
		},
		{
			"note supersede",
			trail.Entry{Kind: "edit", Snapshot: model.Note{}, Changes: []trail.Change{
				set("superseded_by", "abc"),
			}},
			[]string{lifecycle.TypeSuperseded},
		},
		{
			"doc edit falls through",
			trail.Entry{Kind: "edit", Snapshot: model.Doc{}, Changes: []trail.Change{
				scalar("body", "a", "b"),
			}},
			[]string{lifecycle.TypeEdited},
		},
		{
			"sprint status",
			trail.Entry{Kind: "edit", Snapshot: model.Sprint{}, Changes: []trail.Change{
				scalar("status", "planned", "active"),
			}},
			[]string{lifecycle.TypeStatus},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := types(lifecycle.Classify(tc.entry)); !slices.Equal(got, tc.want) {
				t.Fatalf("Classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyLogAppendFansOutPerEntry(t *testing.T) {
	log := model.Log{Entries: []model.LogEntry{{Text: "first"}, {Text: "second"}, {Text: "third"}}}
	entry := trail.Entry{Kind: "edit", Snapshot: log, Changes: []trail.Change{set("entries", "b", "c")}}

	events := lifecycle.Classify(entry)
	if got := types(events); !slices.Equal(got, []string{lifecycle.TypeEntry, lifecycle.TypeEntry}) {
		t.Fatalf("Classify = %v, want two entry events", got)
	}
	if events[0].Detail["text"] != "second" || events[1].Detail["text"] != "third" {
		t.Fatalf("entry texts = %q, %q; want the two appended", events[0].Detail["text"], events[1].Detail["text"])
	}
}

func TestBranchAttribution(t *testing.T) {
	branchAnchor := []model.Anchor{{Kind: model.AnchorBranch, Value: "feature/x"}}
	for _, tc := range []struct {
		name string
		snap model.Snapshot
		want string
	}{
		{"task reads its branch scalar", model.Task{Branch: "main"}, "main"},
		{"note reads its first branch anchor", model.Note{Anchors: branchAnchor}, "feature/x"},
		{"note with only a path anchor has none", model.Note{Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}}, ""},
		{"a runbook carries no branch", model.Runbook{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lifecycle.Branch(tc.snap); got != tc.want {
				t.Fatalf("Branch = %q, want %q", got, tc.want)
			}
		})
	}
}
