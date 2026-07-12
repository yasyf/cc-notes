package cli_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// papercutEntryJSON mirrors one row of the papercut list DTO.
type papercutEntryJSON struct {
	LogID  string  `json:"log_id"`
	Model  *string `json:"model"`
	Author string  `json:"author"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
}

func papercutLogs(t *testing.T, dir string) []logJSON {
	t.Helper()
	return mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--label", "papercut", "--json"))
}

func TestPapercutFirstCreatesJournal(t *testing.T) {
	dir := initRepo(t)
	echo := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "unquoted globs broke rg", "--json"))
	if echo.Title != "papercuts" {
		t.Fatalf("journal title = %q, want papercuts", echo.Title)
	}
	if !slices.Contains(echo.Tags, "papercut") {
		t.Fatalf("journal tags = %v, want to include papercut", echo.Tags)
	}
	if len(echo.Entries) != 1 || echo.Entries[0].Text != "unquoted globs broke rg" {
		t.Fatalf("entries = %+v, want the one complaint", echo.Entries)
	}
	if logs := papercutLogs(t, dir); len(logs) != 1 {
		t.Fatalf("papercut-tagged logs = %d, want exactly 1", len(logs))
	}
}

func TestPapercutSecondAppendsSameJournal(t *testing.T) {
	dir := initRepo(t)
	first := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "first friction", "--json"))
	second := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "second friction", "--json"))
	if second.ID != first.ID {
		t.Fatalf("second journal id = %q, want %q (idempotent find-or-create)", second.ID, first.ID)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("entries = %+v, want two appended entries", second.Entries)
	}
	if logs := papercutLogs(t, dir); len(logs) != 1 {
		t.Fatalf("papercut-tagged logs = %d, want still exactly 1", len(logs))
	}
}

func TestPapercutModelInShowJSON(t *testing.T) {
	dir := initRepo(t)
	echo := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "friction", "--model", "claude-fable-5", "--json"))
	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", echo.ID, "--json"))
	if len(shown.Entries) != 1 {
		t.Fatalf("entries = %+v, want one", shown.Entries)
	}
	if got := shown.Entries[0].Model; got == nil || *got != "claude-fable-5" {
		t.Fatalf("entry model = %v, want claude-fable-5 in log show --json", got)
	}
}

func TestPapercutModelEnvAndFlagPrecedence(t *testing.T) {
	dir := initRepo(t)
	// The env value carries surrounding whitespace to prove it is trimmed.
	t.Setenv("CC_NOTES_MODEL", "  claude-opus-4-8  ")

	env := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "via env", "--json"))
	if got := env.Entries[0].Model; got == nil || *got != "claude-opus-4-8" {
		t.Fatalf("env entry model = %v, want the trimmed CC_NOTES_MODEL claude-opus-4-8", got)
	}

	flagged := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "via flag", "--model", "claude-fable-5", "--json"))
	last := flagged.Entries[len(flagged.Entries)-1]
	if got := last.Model; got == nil || *got != "claude-fable-5" {
		t.Fatalf("flagged entry model = %v, want claude-fable-5 (flag beats env)", got)
	}
}

func TestPapercutListLeanBlocks(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "papercut", "no model here")
	mustRun(t, dir, "papercut", "with a model", "--model", "claude-fable-5")

	out := mustRun(t, dir, "papercut", "list")
	// Model-less block: "-- <author> <ts>".
	if !strings.Contains(out, fmt.Sprintf("-- %s ", actorA)) {
		t.Fatalf("list = %q, want a model-less -- author block", out)
	}
	// Model-bearing block: "-- <model> — <author> <ts>".
	if !strings.Contains(out, fmt.Sprintf("-- claude-fable-5 — %s ", actorA)) {
		t.Fatalf("list = %q, want a model-bearing -- model — author block", out)
	}
	// The model-less complaint sorts first (appended first) and is separated from
	// the next block by a blank line.
	if !strings.Contains(out, "no model here\n\n-- claude-fable-5 — ") {
		t.Fatalf("list = %q, want a blank line between chronologically ordered blocks", out)
	}
}

func TestPapercutListJSONShape(t *testing.T) {
	dir := initRepo(t)
	j := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "no model", "--json"))
	mustRun(t, dir, "papercut", "with model", "--model", "claude-fable-5")

	raw := mustRun(t, dir, "papercut", "list", "--json")
	if !strings.Contains(raw, `"model":null`) {
		t.Fatalf("list --json = %q, want an explicit \"model\":null for the model-less entry", raw)
	}
	rows := mustJSON[[]papercutEntryJSON](t, raw)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Model != nil {
		t.Fatalf("row0 model = %v, want null", rows[0].Model)
	}
	if got := rows[1].Model; got == nil || *got != "claude-fable-5" {
		t.Fatalf("row1 model = %v, want claude-fable-5", got)
	}
	if rows[0].LogID != j.ID || rows[0].Text != "no model" || rows[0].Author != actorA {
		t.Fatalf("row0 = %+v, want log_id %s, text 'no model', author %s", rows[0], j.ID, actorA)
	}
	if _, err := time.Parse(time.RFC3339, rows[0].TS); err != nil {
		t.Fatalf("row0 ts = %q, not RFC3339: %v", rows[0].TS, err)
	}
}

func TestPapercutTwinConvergence(t *testing.T) {
	dir := initRepo(t)
	first := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "original complaint", "--json"))

	// Mint a deliberate same-content twin. A create bundled with an append_entry
	// is not dedupe-covered (dedupeCovered excludes append_entry), so the store
	// roots a second papercut-tagged journal rather than reusing the first — the
	// cross-clone twin the tag-scan must converge. No CLI path bundles this way
	// (log add --entry creates then separately appends), so it goes through the
	// store directly.
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	twinSnap, err := s.Create(t.Context(), []model.Op{
		model.CreateLog{Nonce: model.NewNonce(), Title: papercutTitleForTest, Tags: []string{"papercut"}},
		model.AppendEntry{Text: "twin complaint"},
	})
	if err != nil {
		t.Fatalf("create twin: %v", err)
	}
	twin := twinSnap.(model.Log)
	if string(twin.ID) == first.ID {
		t.Fatalf("twin id = %q, want a distinct second journal", twin.ID)
	}
	if logs := papercutLogs(t, dir); len(logs) != 2 {
		t.Fatalf("papercut-tagged logs = %d, want 2 (a deliberate twin)", len(logs))
	}

	// The canonical pick is the first papercut-tagged log in ListLogs order
	// (created_at asc, id asc) — the same order findOrCreatePapercutLog scans.
	live, err := s.ListLogs(t.Context(), false)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	var canonicalID string
	for _, l := range live {
		if slices.Contains(l.Tags, "papercut") {
			canonicalID = string(l.ID)
			break
		}
	}

	appended := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "converged complaint", "--json"))
	if appended.ID != canonicalID {
		t.Fatalf("converged onto %q, want the canonical (oldest) journal %q", appended.ID, canonicalID)
	}
	if last := appended.Entries[len(appended.Entries)-1]; last.Text != "converged complaint" {
		t.Fatalf("canonical journal's last entry = %q, want 'converged complaint'", last.Text)
	}
	if logs := papercutLogs(t, dir); len(logs) != 2 {
		t.Fatalf("papercut-tagged logs after convergence = %d, want still 2 (no third journal)", len(logs))
	}

	rows := mustJSON[[]papercutEntryJSON](t, mustRun(t, dir, "papercut", "list", "--json"))
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 unioned across both journals", len(rows))
	}
	texts := map[string]bool{}
	for _, r := range rows {
		texts[r.Text] = true
	}
	for _, want := range []string{"original complaint", "twin complaint", "converged complaint"} {
		if !texts[want] {
			t.Fatalf("list rows = %+v, want the union to include %q", rows, want)
		}
	}
}

// papercutTitleForTest mirrors the papercut journal's display title; a same-title
// twin proves the tag (not the title) is the journal's identity.
const papercutTitleForTest = "papercuts"

func TestPapercutStdin(t *testing.T) {
	dir := initRepo(t)
	stdout, stderr, err := runCLIIn(t, dir, "friction from stdin\n", "papercut", "-", "--json")
	if err != nil {
		t.Fatalf("papercut -: %v (stderr %q)", err, stderr)
	}
	echo := mustJSON[logJSON](t, stdout)
	if len(echo.Entries) != 1 || echo.Entries[0].Text != "friction from stdin" {
		t.Fatalf("entries = %+v, want the stdin complaint with the trailing newline trimmed", echo.Entries)
	}
}

func TestPapercutBareUsageError(t *testing.T) {
	dir := initRepo(t)
	var usage *cli.UsageError

	_, _, err := runCLI(t, dir, "papercut")
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("bare papercut err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	_, _, err = runCLI(t, dir, "papercut", "   ")
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("whitespace-only papercut err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	// A rejected papercut mints no journal.
	if logs := papercutLogs(t, dir); len(logs) != 0 {
		t.Fatalf("papercut-tagged logs = %d, want 0 after rejected filings", len(logs))
	}
}

func TestPapercutDashDashFilesLiteralList(t *testing.T) {
	dir := initRepo(t)
	// "papercut list" reads the journal, so filing a complaint whose text is
	// "list" needs the -- escape (--json sits before -- so it stays a flag).
	echo := mustJSON[logJSON](t, mustRun(t, dir, "papercut", "--json", "--", "list"))
	if len(echo.Entries) != 1 || echo.Entries[0].Text != "list" {
		t.Fatalf("entries = %+v, want a complaint whose text is literally 'list'", echo.Entries)
	}
	listed := mustRun(t, dir, "papercut", "list")
	if !strings.Contains(listed, "\nlist\n") {
		t.Fatalf("papercut list = %q, want it to render the filed 'list' complaint", listed)
	}
}
