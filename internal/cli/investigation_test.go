package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const (
	invTitle        = "daemonkit: TestPool deadlock on CI"
	invPremise      = "Hangs began after 3d55ae2e; suspect the pool rewrite."
	invFinding      = "commit 3d55ae2e (pool rewrite)"
	invEvidence     = "Bisect: hang reproduces at 3d55ae2e~4 too."
	invWhy          = "bisect reproduces 4 commits earlier"
	invRootCause    = "Unbuffered results chan + early return on ctx cancel leaks a blocked send."
	invConfirmation = "20 green CI runs since 5e3c9ce4; no recurrence."
)

func investigationEntryTexts(inv investigationDTO) []string {
	texts := make([]string, len(inv.Entries))
	for i, entry := range inv.Entries {
		texts[i] = entry.Text
	}
	return texts
}

type investigationStoryWant struct {
	status        string
	findingStatus string
	findingNote   string
	entries       []string
	rootCause     string
	fixCommits    []string
	attachments   int
	terminal      bool
}

func assertInvestigationStoryState(t *testing.T, inv investigationDTO, id string, want investigationStoryWant) {
	t.Helper()
	if inv.ID != id || inv.Title != invTitle || inv.Premise != invPremise {
		t.Fatalf("identity = %s/%q/%q, want %s/%q/%q", inv.ID, inv.Title, inv.Premise, id, invTitle, invPremise)
	}
	if inv.Status != want.status {
		t.Errorf("status = %q, want %q", inv.Status, want.status)
	}
	if inv.Body != "" {
		t.Errorf("resolution body = %q, want empty", inv.Body)
	}
	if inv.RootCause != want.rootCause {
		t.Errorf("root cause = %q, want %q", inv.RootCause, want.rootCause)
	}
	if inv.FollowUps != nil || inv.Commits != nil || inv.SupersededBy != nil {
		t.Errorf("never-set list fields must be absent, got follow_ups=%v commits=%v superseded_by=%v",
			inv.FollowUps, inv.Commits, inv.SupersededBy)
	}
	if strings.Join(inv.Labels, ",") != "ci" {
		t.Errorf("labels = %v, want [ci]", inv.Labels)
	}
	if len(inv.Anchors) != 1 || inv.Anchors[0].Kind != "path" || inv.Anchors[0].Value != "internal/pool" {
		t.Errorf("anchors = %+v, want one path internal/pool", inv.Anchors)
	}
	if len(inv.Findings) != 1 {
		t.Fatalf("findings = %+v, want one", inv.Findings)
	}
	finding := inv.Findings[0]
	if finding.Text != invFinding || finding.Status != want.findingStatus || finding.Note != want.findingNote {
		t.Errorf("finding = %+v, want text %q status %q note %q", finding, invFinding, want.findingStatus, want.findingNote)
	}
	if got := investigationEntryTexts(inv); !slices.Equal(got, want.entries) {
		t.Errorf("timeline = %v, want %v", got, want.entries)
	}
	gotCommits := slices.Clone(inv.FixCommits)
	wantCommits := slices.Clone(want.fixCommits)
	slices.Sort(gotCommits)
	slices.Sort(wantCommits)
	if !slices.Equal(gotCommits, wantCommits) {
		t.Errorf("fix commits = %v, want %v", gotCommits, wantCommits)
	}
	if len(inv.Attachments) != want.attachments {
		t.Fatalf("attachments = %+v, want %d", inv.Attachments, want.attachments)
	}
	if want.attachments == 1 && inv.Attachments[0].Name != "goroutine-stacks.txt" {
		t.Errorf("attachment = %+v, want goroutine-stacks.txt", inv.Attachments[0])
	}
	if want.terminal != (inv.ClosedAt != nil) {
		t.Errorf("closed_at = %v, terminal = %t", inv.ClosedAt, want.terminal)
	}
	if want.terminal != (inv.ClosedBy != nil) {
		t.Errorf("closed_by = %v, terminal = %t", inv.ClosedBy, want.terminal)
	}
}

// assertInvestigationStorySummary pins what a listing carries: the identity and
// status a reader triages on, the finding and timeline tallies, and nothing
// else — a summary has no premise, findings, or entries to inspect.
func assertInvestigationStorySummary(t *testing.T, inv investigationSummaryDTO, id string, want investigationStoryWant) {
	t.Helper()
	if inv.ID != id || inv.Title != invTitle || inv.Status != want.status {
		t.Errorf("summary = %s/%q/%q, want %s/%q/%q", inv.ID, inv.Title, inv.Status, id, invTitle, want.status)
	}
	if inv.FindingCount != 1 {
		t.Errorf("finding_count = %d, want 1", inv.FindingCount)
	}
	if inv.EntryCount != len(want.entries) {
		t.Errorf("entry_count = %d, want %d", inv.EntryCount, len(want.entries))
	}
}

func assertInvestigationStorySurfaces(t *testing.T, dir, id string, want investigationStoryWant) {
	t.Helper()
	shown := spJSON[investigationDTO](t, spMust(t, dir, "investigation", "show", id, "--json"))
	assertInvestigationStoryState(t, shown, id, want)

	plain := spMust(t, dir, "investigation", "show", id)
	for _, fragment := range []string{
		"id: " + id,
		"title: " + invTitle,
		"status: " + want.status,
		invPremise,
		invFinding,
		want.findingStatus,
	} {
		if !strings.Contains(plain, fragment) {
			t.Errorf("show at %s omits %q:\n%s", want.status, fragment, plain)
		}
	}
	lower := strings.ToLower(plain)
	sections := []string{"\npremise:\n", "\nfindings:\n", "\ntimeline:\n", "\nroot cause:\n", "\nresolution:\n"}
	lastSection := -1
	for _, section := range sections {
		at := strings.Index(lower, section)
		if at < 0 || at <= lastSection {
			t.Errorf("show at %s omits or misorders %q section:\n%s", want.status, section, plain)
		}
		lastSection = at
	}
	if want.findingNote != "" && !strings.Contains(plain, want.findingNote) {
		t.Errorf("show at %s omits finding why %q:\n%s", want.status, want.findingNote, plain)
	}
	if want.rootCause != "" && !strings.Contains(plain, want.rootCause) {
		t.Errorf("show at %s omits root cause %q:\n%s", want.status, want.rootCause, plain)
	}
	timelineStart := strings.Index(lower, "\ntimeline:\n")
	timelineEnd := strings.Index(lower[timelineStart+1:], "\nroot cause:\n") + timelineStart + 1
	timeline := plain[timelineStart:timelineEnd]
	last := -1
	for _, entry := range want.entries {
		at := strings.Index(timeline, entry)
		if at < 0 || at <= last {
			t.Errorf("show timeline is not chronological at %q:\n%s", entry, timeline)
		}
		last = at
	}
	if want.attachments == 1 && !strings.Contains(plain, "attachment: goroutine-stacks.txt") {
		t.Errorf("show at %s omits attachment header:\n%s", want.status, plain)
	}
	if want.attachments == 1 && !strings.Contains(timeline, "attachment: goroutine-stacks.txt") {
		t.Errorf("show at %s does not associate the attachment with its timeline entry:\n%s", want.status, timeline)
	}
	if want.findingStatus == "cleared" && !strings.Contains(timeline, "→ cleared: "+invWhy) {
		t.Errorf("show at %s omits the finding transition:\n%s", want.status, timeline)
	}
	if want.rootCause != "" && !strings.Contains(timeline, "status: open → root_caused") {
		t.Errorf("show at %s omits the root-caused transition:\n%s", want.status, timeline)
	}
	if want.status == "fixed" || want.status == "confirmed" {
		if !strings.Contains(timeline, "status: root_caused → fixed") {
			t.Errorf("show at %s omits the fixed transition:\n%s", want.status, timeline)
		}
	}
	if want.status == "confirmed" {
		if !strings.Contains(timeline, "status: fixed → confirmed") {
			t.Errorf("show at %s omits the confirmed transition:\n%s", want.status, timeline)
		}
		headerBlock := plain[:strings.Index(lower, "\npremise:\n")]
		if !strings.Contains(headerBlock, "resolution: "+invConfirmation) {
			t.Errorf("closed show omits its one-line resolution summary:\n%s", headerBlock)
		}
	}
	wantResolutionLines := 1
	if want.terminal {
		wantResolutionLines = 2
	}
	if got := strings.Count(lower, "\nresolution:"); got != wantResolutionLines {
		t.Errorf("show at %s has %d resolution lines, want %d (closed records add the header summary):\n%s", want.status, got, wantResolutionLines, plain)
	}

	all := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "list", "--all", "--json"))
	if len(all) != 1 {
		t.Fatalf("list --all at %s = %d entries, want 1", want.status, len(all))
	}
	assertInvestigationStorySummary(t, all[0], id, want)
	filtered := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "list", "--status", want.status, "--json"))
	if len(filtered) != 1 || filtered[0].ID != id {
		t.Fatalf("list --status %s = %+v, want %s", want.status, filtered, id)
	}
	listed := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "list", "--json"))
	if want.terminal {
		if len(listed) != 0 {
			t.Errorf("default list at terminal %s = %+v, want empty", want.status, listed)
		}
	} else if len(listed) != 1 || listed[0].ID != id {
		t.Errorf("default list at %s = %+v, want %s", want.status, listed, id)
	}

	found := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "search", "daemonkit", "--json"))
	if len(found) != 1 {
		t.Fatalf("search at %s = %d entries, want 1", want.status, len(found))
	}
	assertInvestigationStorySummary(t, found[0], id, want)
	leanWant := id[:7] + "\t" + want.status + "\t" + invTitle + "\n"
	if got := spMust(t, dir, "investigation", "list", "--all"); got != leanWant {
		t.Errorf("lean list at %s = %q, want %q", want.status, got, leanWant)
	}
	if got := spMust(t, dir, "investigation", "search", "daemonkit"); got != leanWant {
		t.Errorf("lean search at %s = %q, want %q", want.status, got, leanWant)
	}
}

// invShow reads an investigation back in full. A mutation acknowledges with a
// summary, so the premise and the findings live only here.
func invShow(t *testing.T, dir, id string) investigationDTO {
	t.Helper()
	return spJSON[investigationDTO](t, spMust(t, dir, "investigation", "show", id, "--json"))
}

func TestInvestigationOpenForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stdin   string
		args    []string
		title   string
		premise string
	}{
		{name: "positional", args: []string{"investigation", "open", "Positional", "positional premise", "--json"}, title: "Positional", premise: "positional premise"},
		{name: "body flag", args: []string{"investigation", "open", "Flag", "--body", "flag premise", "--json"}, title: "Flag", premise: "flag premise"},
		{name: "stdin", stdin: "piped premise\n", args: []string{"investigation", "open", "Stdin", "-", "--json"}, title: "Stdin", premise: "piped premise"},
		{name: "add alias", args: []string{"investigation", "add", "Alias", "alias premise", "--json"}, title: "Alias", premise: "alias premise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := spInitRepo(t)
			stdout, stderr, err := spRun(t, dir, tc.stdin, tc.args...)
			if err != nil {
				t.Fatalf("cc-notes %s: %v (stderr %q)", strings.Join(tc.args, " "), err, stderr)
			}
			ack := spJSON[investigationSummaryDTO](t, stdout)
			if ack.Title != tc.title || ack.Status != "open" {
				t.Errorf("open ack = %q/%q, want %q/open", ack.Title, ack.Status, tc.title)
			}
			if got := invShow(t, dir, ack.ID).Premise; got != tc.premise {
				t.Errorf("premise = %q, want %q", got, tc.premise)
			}
		})
	}
}

func TestInvestigationRequiredTextRejectsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
		args  func(*testing.T, string) []string
	}{
		{
			name: "open positional",
			args: func(*testing.T, string) []string {
				return []string{"investigation", "open", "Empty premise", ""}
			},
		},
		{
			name: "open flag",
			args: func(*testing.T, string) []string {
				return []string{"investigation", "open", "Empty premise", "--body", ""}
			},
		},
		{
			name: "open stdin",
			args: func(*testing.T, string) []string {
				return []string{"investigation", "open", "Empty premise", "-"}
			},
		},
		{
			name: "finding add positional",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--json"))
				return []string{"investigation", "finding", "add", id, ""}
			},
		},
		{
			name: "finding add flag",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--json"))
				return []string{"investigation", "finding", "add", id, "--body", ""}
			},
		},
		{
			name: "finding add stdin",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--json"))
				return []string{"investigation", "finding", "add", id, "-"}
			},
		},
		{
			name: "finding edit positional",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--finding", "original", "--json"))
				return []string{"investigation", "finding", "edit", id, invShow(t, dir, id).Findings[0].ID, ""}
			},
		},
		{
			name: "finding edit flag",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--finding", "original", "--json"))
				return []string{"investigation", "finding", "edit", id, invShow(t, dir, id).Findings[0].ID, "--body", ""}
			},
		},
		{
			name: "finding edit stdin",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Finding", "premise", "--finding", "original", "--json"))
				return []string{"investigation", "finding", "edit", id, invShow(t, dir, id).Findings[0].ID, "-"}
			},
		},
		{
			name: "required transition positional",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Transition", "premise", "--json"))
				return []string{"investigation", "root-cause", id, ""}
			},
		},
		{
			name: "required transition stdin",
			args: func(t *testing.T, dir string) []string {
				id := spID(t, spMust(t, dir, "investigation", "open", "Transition", "premise", "--json"))
				return []string{"investigation", "root-cause", id, "-"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := spInitRepo(t)
			args := tc.args(t, dir)
			_, _, err := spRun(t, dir, tc.stdin, args...)
			if !isUsage(err) {
				t.Fatalf("cc-notes %s error = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, ExitCode(err))
			}
		})
	}
}

func TestInvestigationOpenRejectsEmptyFinding(t *testing.T) {
	dir := spInitRepo(t)
	_, _, err := spRun(t, dir, "", "investigation", "open", "Empty finding", "premise", "--finding", "")
	if !errors.Is(err, notes.ErrEmptyFinding) {
		t.Fatalf("open with empty finding error = %v, want ErrEmptyFinding", err)
	}
	got := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "list", "--all", "--json"))
	if len(got) != 0 {
		t.Fatalf("investigations after rejected open = %+v, want none", got)
	}
}

func TestRenderInvestigationRootPackStatus(t *testing.T) {
	inv := model.Investigation{
		ID:        model.EntityID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Title:     "Root pack",
		Premise:   "same-pack transition",
		Status:    model.InvestigationRootCaused,
		Author:    model.Actor(spActor),
		CreatedAt: 1,
		UpdatedAt: 1,
	}
	steps := []fold.Step{{
		Commit: model.PackCommit{
			Author:     model.Actor(spActor),
			AuthorTime: 1,
			Pack: model.Pack{Ops: []model.Op{
				model.CreateInvestigation{Title: inv.Title, Premise: inv.Premise},
				model.SetInvestigationStatus{Status: inv.Status},
			}},
		},
		Snapshot: inv,
	}}

	got := renderInvestigationShow(inv, nil, steps)
	if !strings.Contains(got, "status: open → root_caused") {
		t.Fatalf("root-pack show omitted the status transition:\n%s", got)
	}
}

func TestInvestigationTitleStatusHint(t *testing.T) {
	for _, word := range []string{"RESOLVED", "FIXED", "FALSIFIED", "CONFIRMED"} {
		t.Run(strings.ToLower(word), func(t *testing.T) {
			dir := spInitRepo(t)
			stdout, stderr, err := spRun(t, dir, "", "investigation", "open", word+" pool deadlock", "premise", "--json")
			if err != nil {
				t.Fatalf("open title containing %s: %v", word, err)
			}
			if ack := spJSON[investigationSummaryDTO](t, stdout); ack.Status != "open" {
				t.Errorf("status = %q, want open", ack.Status)
			}
			if !strings.Contains(stderr, word) || !strings.Contains(stderr, "status is structural") {
				t.Errorf("stderr = %q, want structural-status hint for %s", stderr, word)
			}
		})
	}
}

func TestInvestigationCommandCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, string)
	}{
		{
			name: "finding CRUD",
			run: func(t *testing.T, dir string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "Findings", "test suspects", "--json"))
				ack := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "finding", "add", id, "suspect A", "--json"))
				if ack.FindingCount != 1 {
					t.Fatalf("finding add ack = %+v, want finding_count 1", ack)
				}
				added := invShow(t, dir, id).Findings
				if len(added) != 1 || added[0].Status != "open" {
					t.Fatalf("finding add = %+v, want one open finding", added)
				}
				findingID := added[0].ID
				spMust(t, dir, "investigation", "finding", "edit", id, findingID[:8], "suspect B", "--json")
				if got := invShow(t, dir, id).Findings[0].Text; got != "suspect B" {
					t.Errorf("finding edit text = %q, want suspect B", got)
				}
				listed := spJSON[[]findingDTO](t, spMust(t, dir, "investigation", "finding", "list", id, "--json"))
				if len(listed) != 1 || listed[0].ID != findingID || listed[0].Text != "suspect B" {
					t.Fatalf("finding list = %+v, want edited finding %s", listed, findingID)
				}
				spMust(t, dir, "investigation", "finding", "confirm", id, findingID[:8], "--why", "stack points here", "--json")
				if got := invShow(t, dir, id).Findings[0]; got.Status != "confirmed" || got.Note != "stack points here" {
					t.Errorf("finding confirm = %+v, want confirmed with why", got)
				}
				removed := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "finding", "rm", id, findingID[:8], "--json"))
				if removed.FindingCount != 0 {
					t.Errorf("finding rm ack = %+v, want finding_count dropped", removed)
				}
				if got := invShow(t, dir, id).Findings; len(got) != 0 {
					t.Errorf("finding rm = %+v, want empty", got)
				}
			},
		},
		{
			name: "edit",
			run: func(t *testing.T, dir string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "Old title", "immutable premise", "--label", "old", "--path", "old.go", "--json"))
				ack := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "edit", id,
					"--title", "New title", "--body", "resolution summary",
					"--add-label", "new", "--rm-label", "old",
					"--add-path", "new.go", "--rm-path", "old.go", "--json"))
				if ack.Title != "New title" {
					t.Errorf("edit ack title = %q, want New title", ack.Title)
				}
				edited := invShow(t, dir, id)
				if edited.Title != "New title" || edited.Premise != "immutable premise" || edited.Body != "resolution summary" {
					t.Errorf("edited text = %q/%q/%q", edited.Title, edited.Premise, edited.Body)
				}
				if strings.Join(edited.Labels, ",") != "new" {
					t.Errorf("edited labels = %v, want [new]", edited.Labels)
				}
				if len(edited.Anchors) != 1 || edited.Anchors[0].Kind != "path" || edited.Anchors[0].Value != "new.go" {
					t.Errorf("edited anchors = %+v, want path new.go", edited.Anchors)
				}
			},
		},
		{
			name: "exonerate",
			run: func(t *testing.T, dir string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "False lead", "suspect cache", "--json"))
				ack := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "exonerate", id, "cache disabled and failure persists", "--json"))
				if ack.Status != "exonerated" || ack.EntryCount != 1 {
					t.Errorf("exonerate ack = %+v, want exonerated with entry_count 1", ack)
				}
				done := invShow(t, dir, id)
				if done.ClosedAt == nil || !slices.Equal(investigationEntryTexts(done), []string{"cache disabled and failure persists"}) {
					t.Errorf("exonerate = closed %v entries %v", done.ClosedAt, investigationEntryTexts(done))
				}
			},
		},
		{
			name: "abandon and reopen",
			run: func(t *testing.T, dir string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "Dormant", "needs hardware", "--json"))
				ack := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "abandon", id, "--json"))
				if ack.Status != "abandoned" || ack.EntryCount != 0 {
					t.Errorf("abandon ack = %+v, want abandoned with no entries", ack)
				}
				abandoned := invShow(t, dir, id)
				if abandoned.ClosedAt == nil || len(abandoned.Entries) != 0 {
					t.Errorf("abandon = closed %v entries %v", abandoned.ClosedAt, abandoned.Entries)
				}
				if got := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "reopen", id, "hardware arrived", "--json")).Status; got != "open" {
					t.Errorf("reopen ack status = %q, want open", got)
				}
				reopened := invShow(t, dir, id)
				if reopened.ClosedAt != nil || !slices.Equal(investigationEntryTexts(reopened), []string{"hardware arrived"}) {
					t.Errorf("reopen = closed %v entries %v", reopened.ClosedAt, investigationEntryTexts(reopened))
				}
			},
		},
		{
			name: "history and remove",
			run: func(t *testing.T, dir string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "Disposable", "history premise", "--json"))
				spMust(t, dir, "investigation", "append", id, "evidence")
				history := spJSON[[]historyEntryDTO](t, spMust(t, dir, "investigation", "history", id, "--json"))
				if len(history) != 2 || history[1].Kind != "create" {
					t.Errorf("history = %+v, want edit then create", history)
				}
				spMust(t, dir, "investigation", "rm", id, "--json")
				if !invShow(t, dir, id).Deleted {
					t.Errorf("rm deleted = false")
				}
				if listed := spJSON[[]investigationSummaryDTO](t, spMust(t, dir, "investigation", "list", "--all", "--json")); len(listed) != 0 {
					t.Errorf("list --all after rm = %+v, want empty", listed)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, spInitRepo(t))
		})
	}
}

func TestInvestigationCrossKindShowHistorySearch(t *testing.T) {
	dir := spInitRepo(t)
	invID := spID(t, spMust(t, dir, "investigation", "open", "crosskind investigation token", "generic surfaces", "--json"))

	if got, want := spMust(t, dir, "show", invID), spMust(t, dir, "investigation", "show", invID); got != want {
		t.Errorf("top-level show differs from investigation show:\n top:\n%s\n scoped:\n%s", got, want)
	}
	if got, want := spMust(t, dir, "show", invID, "--json"), spMust(t, dir, "investigation", "show", invID, "--json"); got != want {
		t.Errorf("top-level show --json differs from investigation show --json:\n top: %s\n scoped: %s", got, want)
	}

	hits := spJSON[[]searchDTO](t, spMust(t, dir, "search", "crosskind", "--json"))
	if len(hits) != 1 {
		t.Fatalf("top-level search = %+v, want one investigation", hits)
	}
	hit := hits[0]
	if hit.Kind != "investigation" || hit.Investigation == nil || hit.Investigation.ID != invID || hit.Investigation.Status != "open" {
		t.Fatalf("top-level search hit = %+v, want open investigation %s", hit, invID)
	}
	if hit.Note != nil || hit.Doc != nil || hit.Log != nil || hit.Runbook != nil {
		t.Errorf("top-level search investigation hit populates a foreign entity field: %+v", hit)
	}
	if got, want := spMust(t, dir, "search", "crosskind"), "investigation\t"+invID[:7]+"\topen\tcrosskind investigation token\n"; got != want {
		t.Errorf("top-level lean search = %q, want %q", got, want)
	}

	spMust(t, dir, "investigation", "append", invID, "history evidence")
	if got, want := spMust(t, dir, "history", invID), spMust(t, dir, "investigation", "history", invID); got != want {
		t.Errorf("top-level history differs from investigation history:\n top:\n%s\n scoped:\n%s", got, want)
	} else if !strings.Contains(got, "created investigation") || !strings.Contains(got, "history evidence") {
		t.Errorf("investigation history omits create or append change:\n%s", got)
	}
	if got, want := spMust(t, dir, "history", invID, "--json"), spMust(t, dir, "investigation", "history", invID, "--json"); got != want {
		t.Errorf("top-level history --json differs from investigation history --json:\n top: %s\n scoped: %s", got, want)
	}
}

func TestInvestigationRelevantJSONAndLean(t *testing.T) {
	dir := spInitRepo(t)
	path := filepath.Join(dir, "internal", "pool", "pool.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	if err := os.WriteFile(path, []byte("package pool\n"), 0o600); err != nil {
		t.Fatalf("write pool.go: %v", err)
	}
	gittest.Git(t, dir, "add", "internal/pool/pool.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "add pool")

	invID := spID(t, spMust(t, dir, "investigation", "open", "active pool deadlock", "pool stalls", "--path", "internal/pool/pool.go", "--json"))
	dtos := spJSON[[]relevantDTO](t, spMust(t, dir, "relevant", "internal/pool/pool.go", "--json"))
	if len(dtos) != 1 {
		t.Fatalf("relevant --json = %+v, want one investigation", dtos)
	}
	dto := dtos[0]
	if dto.Kind != "investigation" || dto.Investigation == nil || dto.Investigation.ID != invID || dto.Investigation.Status != "open" {
		t.Fatalf("relevant entry = %+v, want open investigation %s", dto, invID)
	}
	if dto.Note != nil || dto.Doc != nil || dto.Log != nil || dto.Runbook != nil || dto.Plan != nil {
		t.Errorf("relevant investigation entry populates a foreign entity field: %+v", dto)
	}
	if dto.Score != 150 || !slices.Equal(dto.Reasons, []string{"investigation-open", "path"}) {
		t.Errorf("relevant score/reasons = %d/%v, want 150/[investigation-open path]", dto.Score, dto.Reasons)
	}
	leanWant := invID[:7] + "\topen\tactive pool deadlock\tinvestigation-open,path\tinvestigation show " + invID[:7] + "\n"
	if got := spMust(t, dir, "relevant", "internal/pool/pool.go"); got != leanWant {
		t.Errorf("relevant lean = %q, want %q", got, leanWant)
	}
}

func TestInvestigationBlameAndTaskCompatibility(t *testing.T) {
	dir := spInitRepo(t)
	inv := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "open", "Blame investigation", "pool stalls", "--json"))
	spMust(t, dir, "investigation", "root-cause", inv.ID, "blocked sender")
	task := spJSON[taskSummaryDTO](t, spMust(t, dir, "task", "add", "Blame task", "--no-validation-criteria", "--json"))
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "fix pool\n\ncc-task: "+task.ID)
	fixSHA := gittest.Git(t, dir, "rev-parse", "HEAD")
	spMust(t, dir, "investigation", "fix", inv.ID, "--commit", fixSHA)

	taskLine := task.ID[:7] + "\t" + task.Status + "\tP" + strconv.Itoa(task.Priority) + "\t-\t" + task.Title
	invLine := inv.ID[:7] + "\tfixed\t" + inv.Title
	if got, want := spMust(t, dir, "blame", fixSHA), taskLine+"\n"+invLine+"\n"; got != want {
		t.Errorf("combined blame = %q, want task line unchanged then investigation %q", got, want)
	}
	dtos := spJSON[[]blameDTO](t, spMust(t, dir, "blame", fixSHA, "--json"))
	if len(dtos) != 2 {
		t.Fatalf("combined blame --json = %+v, want task and investigation", dtos)
	}
	if dtos[0].Kind != "task" || dtos[0].Task == nil || dtos[0].Task.ID != task.ID || dtos[0].Investigation != nil {
		t.Errorf("combined blame task DTO = %+v", dtos[0])
	}
	if dtos[1].Kind != "investigation" || dtos[1].Investigation == nil || dtos[1].Investigation.ID != inv.ID || dtos[1].Investigation.Status != "fixed" || dtos[1].Task != nil {
		t.Errorf("combined blame investigation DTO = %+v", dtos[1])
	}

	taskOnly := spJSON[taskSummaryDTO](t, spMust(t, dir, "task", "add", "Task only", "--no-validation-criteria", "--json"))
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "task only\n\ncc-task: "+taskOnly.ID)
	taskOnlySHA := gittest.Git(t, dir, "rev-parse", "HEAD")
	taskOnlyLine := taskOnly.ID[:7] + "\t" + taskOnly.Status + "\tP" + strconv.Itoa(taskOnly.Priority) + "\t-\t" + taskOnly.Title + "\n"
	if got := spMust(t, dir, "blame", taskOnlySHA); got != taskOnlyLine {
		t.Errorf("task-only blame = %q, want unchanged %q", got, taskOnlyLine)
	}
	taskOnlyDTOs := spJSON[[]blameDTO](t, spMust(t, dir, "blame", taskOnlySHA, "--json"))
	if len(taskOnlyDTOs) != 1 || taskOnlyDTOs[0].Kind != "task" || taskOnlyDTOs[0].Task == nil || taskOnlyDTOs[0].Task.ID != taskOnly.ID || taskOnlyDTOs[0].Investigation != nil {
		t.Errorf("task-only blame --json = %+v, want one task DTO", taskOnlyDTOs)
	}
}

func TestInvestigationDaemonkitStory(t *testing.T) {
	dir := spInitRepo(t)
	attachment := filepath.Join(t.TempDir(), "goroutine-stacks.txt")
	if err := os.WriteFile(attachment, []byte("goroutine 1 [chan send]\n"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	id := spID(t, spMust(t, dir,
		"investigation", "open", invTitle, invPremise,
		"--finding", invFinding, "--path", "internal/pool", "--label", "ci", "--json"))
	opened := invShow(t, dir, id)
	if len(opened.Findings) != 1 {
		t.Fatalf("open findings = %+v, want one", opened.Findings)
	}
	findingID := opened.Findings[0].ID
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{status: "open", findingStatus: "open"})

	spMust(t, dir, "investigation", "append", id, invEvidence, "--attach", attachment, "--json")
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{
		status: "open", findingStatus: "open", entries: []string{invEvidence}, attachments: 1,
	})

	spMust(t, dir, "investigation", "finding", "clear", id, findingID[:8], "--why", invWhy, "--json")
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{
		status: "open", findingStatus: "cleared", findingNote: invWhy,
		entries: []string{invEvidence}, attachments: 1,
	})

	spMust(t, dir, "investigation", "root-cause", id, invRootCause, "--json")
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{
		status: "root_caused", findingStatus: "cleared", findingNote: invWhy,
		entries: []string{invEvidence, invRootCause}, rootCause: invRootCause, attachments: 1,
	})

	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "first half of fix")
	firstFix := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "finish fix")
	secondFix := gittest.Git(t, dir, "rev-parse", "HEAD")
	spMust(t, dir, "investigation", "fix", id, "--commit", firstFix, "--commit", secondFix, "--json")
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{
		status: "fixed", findingStatus: "cleared", findingNote: invWhy,
		entries: []string{invEvidence, invRootCause}, rootCause: invRootCause,
		fixCommits: []string{firstFix, secondFix}, attachments: 1,
	})

	spMust(t, dir, "investigation", "confirm", id, invConfirmation, "--json")
	assertInvestigationStorySurfaces(t, dir, id, investigationStoryWant{
		status: "confirmed", findingStatus: "cleared", findingNote: invWhy,
		entries: []string{invEvidence, invRootCause, invConfirmation}, rootCause: invRootCause,
		fixCommits: []string{firstFix, secondFix}, attachments: 1, terminal: true,
	})
}

func TestInvestigationFixRequiresCommit(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "investigation", "open", "No commit", "premise", "--json"))
	spMust(t, dir, "investigation", "root-cause", id, "cause")
	_, _, err := spRun(t, dir, "", "investigation", "fix", id)
	if !isUsage(err) || !strings.Contains(err.Error(), "at least one --commit") {
		t.Fatalf("fix without --commit error = %v, want usage error requiring at least one --commit", err)
	}
	shown := invShow(t, dir, id)
	if shown.Status != "root_caused" || len(shown.FixCommits) != 0 {
		t.Errorf("after rejected fix = status %q commits %v, want root_caused with no commits", shown.Status, shown.FixCommits)
	}
}

// TestInvestigationListFlagsMutuallyExclusive pins that --all and --status
// cannot be combined: the cobra group violation maps to a usage error (exit 2).
func TestInvestigationListFlagsMutuallyExclusive(t *testing.T) {
	dir := spInitRepo(t)
	_, _, err := spRun(t, dir, "", "investigation", "list", "--all", "--status", "open")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("list --all --status = %v (exit %d), want a mutual-exclusion usage error (exit 2)", err, ExitCode(err))
	}
}

// TestInvestigationVerdictForceGate pins the CLI face of the verdict gate: the
// refusal is a usage error (exit 2) listing every open finding with the --force
// remediation, --force records the verdict, and abandon needs no escape.
func TestInvestigationVerdictForceGate(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "investigation", "open", "Gated", "premise", "--finding", invFinding, "--json"))
	finding := invShow(t, dir, id).Findings[0]

	_, _, err := spRun(t, dir, "", "investigation", "exonerate", id, "not the pool after all")
	if !isUsage(err) {
		t.Fatalf("exonerate with an open finding = %v (exit %d), want a usage error (exit 2)", err, ExitCode(err))
	}
	for _, fragment := range []string{
		id[:7] + " has 1 open finding/findings blocking exonerated",
		"pass --force to record the verdict anyway",
		finding.ID[:7] + " " + invFinding,
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("refusal omits %q:\n%s", fragment, err.Error())
		}
	}
	if got := invShow(t, dir, id); got.Status != "open" || len(got.Entries) != 0 {
		t.Fatalf("after refused exonerate = status %q entries %+v, want an untouched open record", got.Status, got.Entries)
	}

	ack := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "exonerate", id, "not the pool after all", "--force", "--json"))
	if ack.Status != "exonerated" {
		t.Fatalf("forced exonerate ack = %+v, want exonerated", ack)
	}
	if got := invShow(t, dir, id).Findings[0].Status; got != "open" {
		t.Errorf("finding after a forced verdict = %q, want it left open", got)
	}

	walked := spID(t, spMust(t, dir, "investigation", "open", "Walked", "premise", "--finding", invFinding, "--json"))
	if got := spJSON[investigationSummaryDTO](t, spMust(t, dir, "investigation", "abandon", walked, "reprioritized", "--json")); got.Status != "abandoned" {
		t.Fatalf("abandon with an open finding = %+v, want abandoned without --force", got)
	}
}

func TestInvestigationCommandErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string) (string, string)
		args  func(string, string) []string
		want  error  // matched with errors.Is when usage is empty
		usage string // when set, a usage error (exit 2) whose message contains this
	}{
		{
			name: "illegal transition",
			setup: func(t *testing.T, dir string) (string, string) {
				return spID(t, spMust(t, dir, "investigation", "open", "Illegal", "still open", "--json")), ""
			},
			args: func(id, _ string) []string { return []string{"investigation", "confirm", id, "not fixed"} },
			want: notes.ErrIllegalTransition,
		},
		{
			// reopen's TEXT is a required positional enforced at the cobra arg level.
			name: "reopen without reason",
			setup: func(t *testing.T, dir string) (string, string) {
				return spID(t, spMust(t, dir, "investigation", "open", "No reason", "open", "--json")), ""
			},
			args:  func(id, _ string) []string { return []string{"investigation", "reopen", id} },
			usage: "(ID TEXT)",
		},
		{
			// --why is required at the CLI layer, before the store opens.
			name: "finding confirm without why",
			setup: func(t *testing.T, dir string) (string, string) {
				id := spID(t, spMust(t, dir, "investigation", "open", "Missing why", "premise", "--finding", "suspect", "--json"))
				return id, invShow(t, dir, id).Findings[0].ID
			},
			args: func(id, finding string) []string {
				return []string{"investigation", "finding", "confirm", id, finding[:8]}
			},
			usage: "requires --why",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := spInitRepo(t)
			id, finding := tc.setup(t, dir)
			args := tc.args(id, finding)
			_, _, err := spRun(t, dir, "", args...)
			if tc.usage != "" {
				if !isUsage(err) || !strings.Contains(err.Error(), tc.usage) {
					t.Fatalf("cc-notes %s error = %v, want usage error containing %q", strings.Join(args, " "), err, tc.usage)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("cc-notes %s error = %v, want errors.Is(%v)", strings.Join(args, " "), err, tc.want)
			}
			shown := invShow(t, dir, id)
			if shown.Status != "open" {
				t.Errorf("status after rejected command = %q, want open", shown.Status)
			}
			if tc.name == "illegal transition" && len(shown.Entries) != 0 {
				t.Errorf("timeline after illegal transition = %+v, want empty", shown.Entries)
			}
			if finding != "" && shown.Findings[0].Status != "open" {
				t.Errorf("finding after missing --why = %+v, want open", shown.Findings[0])
			}
		})
	}
}
