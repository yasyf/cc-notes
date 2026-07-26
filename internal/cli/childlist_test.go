package cli_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// childCommentJSON mirrors one task comment in the output DTO — the row shape
// task show and task comment list both hand back.
type childCommentJSON struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// childListRecords is how many records each case appends: past the show cap, so
// a show elides the oldest and only the child list command reaches them.
const childListRecords = 25

// childListShown is the show cap every capped collection shares.
const childListShown = 20

func childRecordText(i int) string {
	return fmt.Sprintf("record %02d — the whole text, kept intact", i)
}

// childRow is the normalized view of one record, decoded out of the element
// type the kind's show and list rows share.
type childRow struct {
	author string
	ts     string
	text   string
}

// childListCase is one capped show collection and its uncapped list reader. The
// same element type decodes the show rows and the list rows, which is the point
// of the list command: an agent reaches record 21 without a second row shape.
type childListCase struct {
	name   string
	seed   func(t *testing.T, dir string) string
	show   []string
	list   []string
	shown  func(t *testing.T, raw string) ([]childRow, int)
	listed func(t *testing.T, raw string) []childRow
}

func childListCases() []childListCase {
	return []childListCase{
		{
			name: "log entries",
			seed: func(t *testing.T, dir string) string {
				t.Helper()
				id := jsonID(t, mustRun(t, dir, "log", "add", "Rollout", "--json"))
				for i := range childListRecords {
					mustRun(t, dir, "log", "append", id, childRecordText(i))
				}
				return id
			},
			show:   []string{"log", "show"},
			list:   []string{"log", "entry", "list"},
			shown:  shownEntries,
			listed: listedEntries,
		},
		{
			name: "investigation timeline entries",
			seed: func(t *testing.T, dir string) string {
				t.Helper()
				id := jsonID(t, mustRun(t, dir, "investigation", "open", "Flaky auth", "the refresh races the read", "--json"))
				for i := range childListRecords {
					mustRun(t, dir, "investigation", "append", id, childRecordText(i))
				}
				return id
			},
			show:   []string{"investigation", "show"},
			list:   []string{"investigation", "entry", "list"},
			shown:  shownEntries,
			listed: listedEntries,
		},
		{
			name: "task comments",
			seed: func(t *testing.T, dir string) string {
				t.Helper()
				id := jsonID(t, mustRun(t, dir, "task", "add", "Ship it", "--no-validation-criteria", "--json"))
				for i := range childListRecords {
					mustRun(t, dir, "task", "comment", id, childRecordText(i))
				}
				return id
			},
			show:   []string{"task", "show"},
			list:   []string{"task", "comment", "list"},
			shown:  shownComments,
			listed: listedComments,
		},
	}
}

func shownEntries(t *testing.T, raw string) ([]childRow, int) {
	t.Helper()
	dto := mustJSON[struct {
		Entries []logEntryJSON `json:"entries"`
		Omitted int            `json:"entries_omitted"`
	}](t, raw)
	return entryRows(dto.Entries), dto.Omitted
}

func listedEntries(t *testing.T, raw string) []childRow {
	t.Helper()
	return entryRows(mustJSON[[]logEntryJSON](t, raw))
}

func entryRows(entries []logEntryJSON) []childRow {
	out := make([]childRow, len(entries))
	for i, e := range entries {
		out[i] = childRow{author: e.Author, ts: e.TS, text: e.Text}
	}
	return out
}

func shownComments(t *testing.T, raw string) ([]childRow, int) {
	t.Helper()
	dto := mustJSON[struct {
		Comments []childCommentJSON `json:"comments"`
		Omitted  int                `json:"comments_omitted"`
	}](t, raw)
	return commentRows(dto.Comments), dto.Omitted
}

func listedComments(t *testing.T, raw string) []childRow {
	t.Helper()
	return commentRows(mustJSON[[]childCommentJSON](t, raw))
}

func commentRows(comments []childCommentJSON) []childRow {
	out := make([]childRow, len(comments))
	for i, c := range comments {
		out[i] = childRow{author: c.Author, ts: c.TS, text: c.Body}
	}
	return out
}

func childTexts(rows []childRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.text
	}
	return out
}

// childBlocks splits a list command's text output into its
// "-- <author> <RFC3339>" blocks, the block idiom a show prints.
func childBlocks(out string) []string {
	trimmed := strings.TrimSuffix(out, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n\n")
}

// TestChildListReachesEveryRecord is the escape-hatch litmus for every capped
// show collection: the show hands back the 20 most recent records and reports
// the rest as elided, while the child list command hands back every record, in
// order, with its text intact — as JSON rows the show's own element type
// decodes, and as the same text blocks the show prints.
func TestChildListReachesEveryRecord(t *testing.T) {
	want := make([]string, childListRecords)
	for i := range want {
		want[i] = childRecordText(i)
	}

	for _, tc := range childListCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			id := tc.seed(t, dir)

			shown, omitted := tc.shown(t, mustRun(t, dir, append(slices.Clone(tc.show), id, "--json")...))
			if len(shown) != childListShown || omitted != childListRecords-childListShown {
				t.Fatalf("show = %d records with %d omitted, want %d with %d elided",
					len(shown), omitted, childListShown, childListRecords-childListShown)
			}
			if newest := want[childListRecords-childListShown:]; !slices.Equal(childTexts(shown), newest) {
				t.Errorf("show records = %v, want the newest %d %v", childTexts(shown), childListShown, newest)
			}

			listed := tc.listed(t, mustRun(t, dir, append(slices.Clone(tc.list), id, "--json")...))
			if got := childTexts(listed); !slices.Equal(got, want) {
				t.Errorf("list records = %v, want all %d in order %v", got, childListRecords, want)
			}
			for i, row := range listed {
				if row.author != actorA || row.ts == "" {
					t.Errorf("list row %d = %+v, want author %q and a timestamp", i, row, actorA)
				}
			}

			text := mustRun(t, dir, append(slices.Clone(tc.list), id)...)
			blocks := childBlocks(text)
			if len(blocks) != childListRecords {
				t.Fatalf("list text = %d blocks, want %d:\n%s", len(blocks), childListRecords, text)
			}
			shownText := mustRun(t, dir, append(slices.Clone(tc.show), id)...)
			for i, block := range blocks {
				if !strings.Contains(block, want[i]) {
					t.Errorf("list text block %d = %q, want record %d", i, block, i)
				}
				if !strings.Contains(shownText, block) {
					t.Errorf("list text block %d is not the block the show prints:\n%q\n--- show ---\n%s", i, block, shownText)
				}
			}
		})
	}
}

// TestChildListEmptyCollection pins the empty case: no records means no rows and
// no text output, and an empty JSON array — never null, which a client
// iterating the whole response cannot consume.
func TestChildListEmptyCollection(t *testing.T) {
	dir := initRepo(t)
	logID := jsonID(t, mustRun(t, dir, "log", "add", "Empty", "--json"))
	taskID := jsonID(t, mustRun(t, dir, "task", "add", "Uncommented", "--no-validation-criteria", "--json"))
	invID := jsonID(t, mustRun(t, dir, "investigation", "open", "Unexplored", "nothing observed yet", "--json"))
	runbookID := jsonID(t, mustRun(t, dir, "runbook", "add", "Stepless", "--json"))

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{name: "log entries", argv: []string{"log", "entry", "list", logID}},
		{name: "task comments", argv: []string{"task", "comment", "list", taskID}},
		{name: "investigation entries", argv: []string{"investigation", "entry", "list", invID}},
		{name: "investigation findings", argv: []string{"investigation", "finding", "list", invID}},
		{name: "task criteria", argv: []string{"task", "criterion", "list", taskID}},
		{name: "runbook steps", argv: []string{"runbook", "step", "list", runbookID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustRun(t, dir, tc.argv...); got != "" {
				t.Errorf("text output = %q, want empty", got)
			}
			if got := mustRun(t, dir, append(slices.Clone(tc.argv), "--json")...); got != "[]\n" {
				t.Errorf("JSON output = %q, want []", got)
			}
		})
	}
}

// TestChildListTaskCommentDoesNotShadow pins the claim that lets "list" sit
// under "task comment": the comment verb's first positional is always a task id,
// so a comment whose body is literally "list" still appends.
func TestChildListTaskCommentDoesNotShadow(t *testing.T) {
	dir := initRepo(t)
	id := jsonID(t, mustRun(t, dir, "task", "add", "Ship it", "--no-validation-criteria", "--json"))
	mustRun(t, dir, "task", "comment", id, "list")

	listed := childTexts(listedComments(t, mustRun(t, dir, "task", "comment", "list", id, "--json")))
	if !slices.Equal(listed, []string{"list"}) {
		t.Fatalf("comments = %v, want the one comment bodied \"list\"", listed)
	}
}
