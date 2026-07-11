package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

// logEntryJSON mirrors one log entry in the output DTO.
type logEntryJSON struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

// logJSON mirrors the log output DTO for round-trip assertions: a lean
// append-only journal with no freshness lifecycle.
type logJSON struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Entries []logEntryJSON `json:"entries"`
	Tags    []string       `json:"tags"`
	Anchors []struct {
		Kind    string  `json:"kind"`
		Value   string  `json:"value"`
		Witness *string `json:"witness"`
	} `json:"anchors"`
	Author      string           `json:"author"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	Deleted     bool             `json:"deleted"`
	Attachments []attachmentJSON `json:"attachments"`
}

func logIDs(logs []logJSON) []string {
	out := make([]string, len(logs))
	for i, l := range logs {
		out[i] = l.ID
	}
	return out
}

func TestLogAddRoundTrip(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "auth.go", "v1\n")
	out := mustRun(t, dir, "log", "add", "Auth rollout",
		"--label", "ops", "--path", "auth.go", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("log JSON does not lead with id: %q", out)
	}
	added := mustJSON[logJSON](t, out)
	if added.Title != "Auth rollout" {
		t.Fatalf("title = %q, want %q", added.Title, "Auth rollout")
	}
	if len(added.ID) != 40 {
		t.Errorf("id length = %d, want 40", len(added.ID))
	}
	// A bare log add (no --entry) is a single create commit, no born-verified
	// double-append: logs have no freshness lifecycle.
	if len(added.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty on a bare add", added.Entries)
	}
	ref := "refs/cc-notes/logs/" + added.ID
	if got := gittest.Git(t, dir, "rev-list", "--count", ref); got != "1" {
		t.Errorf("log chain has %s commits, want 1 (create only)", got)
	}

	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	if shown.ID != added.ID || shown.Title != added.Title {
		t.Fatalf("show id/title = %q/%q, want %q/%q", shown.ID, shown.Title, added.ID, added.Title)
	}
	lean := mustRun(t, dir, "log", "show", added.ID)
	if !strings.Contains(lean, "title: Auth rollout\ntags: ops\n") {
		t.Fatalf("lean show = %q, want title then tags header lines", lean)
	}
	if strings.Contains(lean, "when:") || strings.Contains(lean, "verified") || strings.Contains(lean, "drift") {
		t.Fatalf("lean show = %q, want no when/verify/drift headers", lean)
	}
}

func TestLogAddWithFirstEntry(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Rollout", "--entry", "flipped to 5%", "--json"))
	if len(added.Entries) != 1 {
		t.Fatalf("entries = %+v, want one first entry", added.Entries)
	}
	if added.Entries[0].Text != "flipped to 5%" {
		t.Fatalf("entry text = %q, want %q", added.Entries[0].Text, "flipped to 5%")
	}
	if added.Entries[0].Author != actorA {
		t.Fatalf("entry author = %q, want %q", added.Entries[0].Author, actorA)
	}
	// The first entry is a separate AppendEntry commit, so the chain is create
	// + append (two commits), and the entry's author/ts come from its own commit.
	ref := "refs/cc-notes/logs/" + added.ID
	if got := gittest.Git(t, dir, "rev-list", "--count", ref); got != "2" {
		t.Errorf("log chain has %s commits, want 2 (create + first entry)", got)
	}
}

func TestLogAppendSources(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Incident", "--json"))

	// positional TEXT
	mustRun(t, dir, "log", "append", added.ID, "first via positional")
	// --entry flag
	mustRun(t, dir, "log", "append", added.ID, "--entry", "second via message")
	// - reads stdin
	if _, stderr, err := runCLIIn(t, dir, "third via stdin\n", "log", "append", added.ID, "-"); err != nil {
		t.Fatalf("log append -: %v (stderr %q)", err, stderr)
	}

	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	wantTexts := []string{"first via positional", "second via message", "third via stdin"}
	if len(shown.Entries) != 3 {
		t.Fatalf("entries = %+v, want 3 in chronological order", shown.Entries)
	}
	for i, want := range wantTexts {
		if shown.Entries[i].Text != want {
			t.Fatalf("entry[%d] = %q, want %q", i, shown.Entries[i].Text, want)
		}
		if shown.Entries[i].Author != actorA {
			t.Errorf("entry[%d] author = %q, want %q", i, shown.Entries[i].Author, actorA)
		}
	}

	// Lean show renders each entry as a "-- <author> <RFC3339>" block, the same
	// block style task comments use.
	lean := mustRun(t, dir, "log", "show", added.ID)
	if !strings.Contains(lean, fmt.Sprintf("\n-- %s ", actorA)) {
		t.Fatalf("lean show = %q, want a -- author block per entry", lean)
	}
	if !strings.HasSuffix(lean, "\nthird via stdin\n") {
		t.Fatalf("lean show = %q, want the last entry text at the end", lean)
	}
}

func TestLogAppendZeroSources(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "L", "--json"))

	_, _, err := runCLI(t, dir, "log", "append", added.ID)
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("append with no text err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	// Nothing was appended.
	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	if len(shown.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty after rejected append", shown.Entries)
	}
}

func TestLogAppendMultipleSources(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "L", "--json"))

	// positional TEXT and --entry together is ambiguous.
	_, _, err := runCLI(t, dir, "log", "append", added.ID, "positional", "--entry", "flagged")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("append with two sources err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	// - stdin and --entry together is ambiguous.
	_, _, err = runCLIIn(t, dir, "from stdin\n", "log", "append", added.ID, "-", "--entry", "flagged")
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("append with stdin+message err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	if len(shown.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty after rejected appends", shown.Entries)
	}
}

func TestLogAddLeanLine(t *testing.T) {
	dir := initRepo(t)
	added := mustRun(t, dir, "log", "add", "Timeline", "--label", "b", "--label", "a")
	listed := mustRun(t, dir, "log", "list")
	if listed != added {
		t.Fatalf("log list = %q, want the line log add printed %q", listed, added)
	}
	dto := mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--json"))[0]
	want := fmt.Sprintf("%s\t%s\ta,b\tTimeline\n", dto.ID[:7], dateOf(t, dto.UpdatedAt))
	if added != want {
		t.Fatalf("log add output = %q, want %q (no when field)", added, want)
	}
}

func TestLogEditMetadataOnly(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "First title", "--json"))

	// edit with no flags is a usage error, exactly like doc edit.
	_, _, err := runCLI(t, dir, "log", "edit", added.ID)
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("log edit with no flags err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	edited := mustJSON[logJSON](t, mustRun(t, dir, "log", "edit", added.ID, "--title", "Second title", "--add-label", "ops", "--json"))
	if edited.ID != added.ID {
		t.Fatalf("edit id = %q, want %q (stable)", edited.ID, added.ID)
	}
	if edited.Title != "Second title" || strings.Join(edited.Tags, ",") != "ops" {
		t.Fatalf("edited title/tags = %q/%v, want Second title/[ops]", edited.Title, edited.Tags)
	}
	// edit never touches entries: there is no flag to do so, and existing
	// entries survive metadata edits untouched.
	mustRun(t, dir, "log", "append", added.ID, "an entry")
	editedAgain := mustJSON[logJSON](t, mustRun(t, dir, "log", "edit", added.ID, "--add-label", "rollout", "--json"))
	if len(editedAgain.Entries) != 1 || editedAgain.Entries[0].Text != "an entry" {
		t.Fatalf("entries after metadata edit = %+v, want the one append preserved", editedAgain.Entries)
	}
}

func TestLogListFiltersAndRm(t *testing.T) {
	dir := initRepo(t)
	keep := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Kept", "--label", "keep", "--dir", "internal/api", "--json"))
	mustRun(t, dir, "log", "add", "Dropped", "--label", "skip", "--dir", "internal/sync")

	byTag := mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--label", "keep", "--json"))
	if len(byTag) != 1 || byTag[0].ID != keep.ID {
		t.Fatalf("list --label keep = %v, want only %s", logIDs(byTag), keep.ID)
	}
	byDir := mustRun(t, dir, "log", "list", "--dir", "internal/api")
	if !strings.HasPrefix(byDir, keep.ID[:7]+"\t") || strings.Count(byDir, "\n") != 1 {
		t.Fatalf("list --dir internal/api = %q, want only %s", byDir, keep.ID[:7])
	}

	rm := mustRun(t, dir, "log", "rm", keep.ID)
	if !strings.HasPrefix(rm, keep.ID[:7]+"\t") {
		t.Fatalf("rm echo = %q, want the tombstoned lean line", rm)
	}
	if out := mustRun(t, dir, "log", "list"); strings.Contains(out, keep.ID[:7]) {
		t.Fatalf("list after rm = %q, want the tombstoned log dropped", out)
	}
	if out := mustRun(t, dir, "log", "list", "--all"); !strings.Contains(out, keep.ID[:7]) {
		t.Fatalf("list --all = %q, want the tombstoned log present", out)
	}
}

func TestLogSearch(t *testing.T) {
	dir := initRepo(t)
	rollout := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Rollout log", "--label", "ops", "--json"))
	mustRun(t, dir, "log", "append", rollout.ID, "the Tokenizer panicked at noon")
	mustRun(t, dir, "log", "add", "Other", "--label", "misc")

	// title, tag, and entry-text matches all surface the rollout log.
	for query, wantTitle := range map[string]string{"ROLLOUT": "Rollout log", "ops": "Rollout log", "tokenizer": "Rollout log", "misc": "Other"} {
		out := mustRun(t, dir, "log", "search", query)
		if !strings.Contains(out, wantTitle) || strings.Count(out, "\n") != 1 {
			t.Errorf("search %q = %q, want one line containing %q", query, out, wantTitle)
		}
	}

	rm := mustRun(t, dir, "log", "rm", rollout.ID)
	if !strings.HasPrefix(rm, rollout.ID[:7]+"\t") {
		t.Fatalf("rm echo = %q, want the tombstoned lean line", rm)
	}
	if out := mustRun(t, dir, "log", "search", "tokenizer"); out != "" {
		t.Fatalf("search after rm = %q, want empty (tombstoned log excluded)", out)
	}
}

func TestLogDeletedShowAndAppend(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Doomed", "--json"))
	mustRun(t, dir, "log", "rm", added.ID)

	// show on a deleted log still resolves and renders it flagged deleted,
	// exactly like doc show on a tombstoned doc.
	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	if !shown.Deleted {
		t.Fatalf("show after rm = %+v, want deleted", shown)
	}
	lean := mustRun(t, dir, "log", "show", added.ID)
	if !strings.Contains(lean, "deleted: true\n") {
		t.Fatalf("lean show after rm = %q, want a deleted header", lean)
	}

	// append to a tombstoned log still resolves the ref and appends — DeleteNote
	// is a soft tombstone, not a hard delete, just like doc.
	appended := mustJSON[logJSON](t, mustRun(t, dir, "log", "append", added.ID, "post-mortem note", "--json"))
	if len(appended.Entries) != 1 || appended.Entries[0].Text != "post-mortem note" {
		t.Fatalf("entries after append to deleted = %+v, want the appended entry", appended.Entries)
	}
}

func TestLogNotFound(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "log", "show", "deadbeef")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("log show err = %v (exit %d), want ErrNotFound exit 3", err, cli.ExitCode(err))
	}
}
