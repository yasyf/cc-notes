package cli_test

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// papercutEntryJSON mirrors one row of the papercut list DTO.
type papercutEntryJSON struct {
	LogID  string  `json:"log_id"`
	Index  int     `json:"index"`
	Model  *string `json:"model"`
	Author string  `json:"author"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
}

// papercutClipChars mirrors the internal maxHistoryValueChars preview cap
// (unexported) the papercut listing clips at.
const papercutClipChars = 300

func papercutLogs(t *testing.T, dir string) []logJSON {
	t.Helper()
	return mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--label", "papercut", "--json"))
}

// TestPapercutAckIsASummary pins the acknowledgement itself, which every other
// test here reads past via showJSON: filing into a journal that already holds
// many complaints must not echo them back. This is the trigger case for the
// whole write-ack change — revert print.go's summary DTO and it fails.
func TestPapercutAckIsASummary(t *testing.T) {
	dir := initRepo(t)
	for i := range 25 {
		mustRun(t, dir, "papercut", fmt.Sprintf("friction %d", i))
	}

	ack := mustRun(t, dir, "papercut", "the complaint under test", "--json")
	for _, frag := range []string{`"entries"`, `"friction 0"`, `"the complaint under test"`} {
		if strings.Contains(ack, frag) {
			t.Errorf("papercut ack %q carries %q; a write acknowledgement is a summary", ack, frag)
		}
	}
	if !strings.Contains(ack, `"entry_count":26`) {
		t.Errorf("papercut ack %q lost the entry_count that replaces the entries", ack)
	}
	if len(ack) > 300 {
		t.Errorf("papercut ack is %d bytes; a summary stays small: %q", len(ack), ack)
	}
}

func TestPapercutFirstCreatesJournal(t *testing.T) {
	dir := initRepo(t)
	echo := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "unquoted globs broke rg", "--json"))
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
	first := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "first friction", "--json"))
	second := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "second friction", "--json"))
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
	echo := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "friction", "--model", "claude-fable-5", "--json"))
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

	env := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "via env", "--json"))
	if got := env.Entries[0].Model; got == nil || *got != "claude-opus-4-8" {
		t.Fatalf("env entry model = %v, want the trimmed CC_NOTES_MODEL claude-opus-4-8", got)
	}

	flagged := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "via flag", "--model", "claude-fable-5", "--json"))
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
	// The model-bearing complaint was appended last, so it leads the newest-first
	// listing and is separated from the older block by a blank line.
	if !strings.Contains(out, "with a model\n\n-- "+actorA+" ") {
		t.Fatalf("list = %q, want a blank line between newest-first blocks", out)
	}
}

func TestPapercutListJSONShape(t *testing.T) {
	dir := initRepo(t)
	j := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "no model", "--json"))
	mustRun(t, dir, "papercut", "with model", "--model", "claude-fable-5")

	raw := mustRun(t, dir, "papercut", "list", "--json")
	if !strings.Contains(raw, `"model":null`) {
		t.Fatalf("list --json = %q, want an explicit \"model\":null for the model-less entry", raw)
	}
	rows := mustJSON[[]papercutEntryJSON](t, raw)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if got := rows[0].Model; got == nil || *got != "claude-fable-5" {
		t.Fatalf("row0 model = %v, want claude-fable-5 (newest first)", got)
	}
	if rows[1].Model != nil {
		t.Fatalf("row1 model = %v, want null", rows[1].Model)
	}
	if rows[1].LogID != j.ID || rows[1].Index != 0 || rows[1].Text != "no model" || rows[1].Author != actorA {
		t.Fatalf("row1 = %+v, want log_id %s, index 0, text 'no model', author %s", rows[1], j.ID, actorA)
	}
	if rows[0].Index != 1 {
		t.Fatalf("row0 index = %d, want 1 (the entry's position within its journal)", rows[0].Index)
	}
	if _, err := time.Parse(time.RFC3339, rows[0].TS); err != nil {
		t.Fatalf("row0 ts = %q, not RFC3339: %v", rows[0].TS, err)
	}
}

// TestPapercutListNewestFirstCapped files more complaints than the default cap
// and pins the listing contract: the 20 newest, newest first, each addressed by
// its own within-journal index whatever the cap; --limit lifts or tightens it.
func TestPapercutListNewestFirstCapped(t *testing.T) {
	dir := initRepo(t)
	const filed = 25
	for i := 0; i < filed; i++ {
		mustRun(t, dir, "papercut", fmt.Sprintf("friction %02d", i))
	}

	rows := mustJSON[[]papercutEntryJSON](t, mustRun(t, dir, "papercut", "list", "--json"))
	if len(rows) != 20 {
		t.Fatalf("default rows = %d, want the 20 newest of %d", len(rows), filed)
	}
	for i, r := range rows {
		want := filed - 1 - i
		if r.Index != want {
			t.Fatalf("row %d index = %d, want %d — the index addresses the journal, not the listing", i, r.Index, want)
		}
		if text := fmt.Sprintf("friction %02d", want); r.Text != text {
			t.Fatalf("row %d text = %q, want %q", i, r.Text, text)
		}
	}

	all := mustJSON[[]papercutEntryJSON](t, mustRun(t, dir, "papercut", "list", "--limit", "0", "--json"))
	if len(all) != filed {
		t.Fatalf("--limit 0 rows = %d, want all %d", len(all), filed)
	}
	if all[0].Text != "friction 24" || all[filed-1].Text != "friction 00" {
		t.Fatalf("--limit 0 ends = %q … %q, want the newest first and the oldest last", all[0].Text, all[filed-1].Text)
	}

	three := mustJSON[[]papercutEntryJSON](t, mustRun(t, dir, "papercut", "list", "--limit", "3", "--json"))
	if len(three) != 3 || three[0].Index != 24 || three[1].Index != 23 || three[2].Index != 22 {
		t.Fatalf("--limit 3 = %+v, want the three newest at indexes 24, 23, 22", three)
	}
}

// TestPapercutListClipsWithShowRecovery pins the summarization contract: a long
// complaint's list row is a clipped preview whose in-band marker names the exact
// "papercut show" call, and that call returns the original text byte-identically.
func TestPapercutListClipsWithShowRecovery(t *testing.T) {
	dir := initRepo(t)
	// A multi-byte filler proves the clip cuts on runes, not bytes.
	long := strings.Repeat("é", papercutClipChars) + " and the rest of the paragraph"
	mustRun(t, dir, "papercut", "short one")
	journal := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", long, "--json"))

	rows := mustJSON[[]papercutEntryJSON](t, mustRun(t, dir, "papercut", "list", "--json"))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].LogID != journal.ID || rows[0].Index != 1 {
		t.Fatalf("newest row = %+v, want index 1 of journal %s", rows[0], journal.ID)
	}
	marker := fmt.Sprintf("…(%d of %d chars; papercut show %s 1)", papercutClipChars, utf8.RuneCountInString(long), journal.ID[:7])
	if want := strings.Repeat("é", papercutClipChars) + marker; rows[0].Text != want {
		t.Fatalf("clipped row text = %q, want %q", rows[0].Text, want)
	}
	if rows[1].Text != "short one" {
		t.Fatalf("row1 text = %q, want the under-cap complaint verbatim", rows[1].Text)
	}
	if out := mustRun(t, dir, "papercut", "list"); !strings.Contains(out, marker) {
		t.Fatalf("papercut list = %q, want the text path to carry the same marker %q", out, marker)
	}

	shown := mustJSON[papercutEntryJSON](t, mustRun(t, dir, "papercut", "show", rows[0].LogID, strconv.Itoa(rows[0].Index), "--json"))
	if shown.Text != long {
		t.Fatalf("papercut show text = %q, want the byte-identical original", shown.Text)
	}
	if shown.LogID != journal.ID || shown.Index != 1 || shown.Author != actorA {
		t.Fatalf("shown row = %+v, want index 1 of journal %s by %s", shown, journal.ID, actorA)
	}
	if out := mustRun(t, dir, "papercut", "show", journal.ID, "1"); !strings.Contains(out, long) {
		t.Fatalf("papercut show = %q, want the text path untruncated too", out)
	}
}

// TestPapercutShowRejectsBadAddress covers every way an address can miss: a
// non-numeric index, an index past either end, and a log that is not a papercut
// journal all exit 2 with a usage error instead of panicking.
func TestPapercutShowRejectsBadAddress(t *testing.T) {
	dir := initRepo(t)
	journal := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "the only complaint", "--json"))
	other := jsonID(t, mustRun(t, dir, "log", "add", "unrelated journal", "--json"))

	cases := []struct {
		name string
		args []string
	}{
		{name: "index not a number", args: []string{journal.ID, "two"}},
		{name: "index past the end", args: []string{journal.ID, "1"}},
		{name: "negative index", args: []string{"--", journal.ID, "-1"}},
		{name: "not a papercut journal", args: []string{other, "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var usage *cli.UsageError
			_, _, err := runCLI(t, dir, append([]string{"papercut", "show"}, tc.args...)...)
			if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
				t.Fatalf("papercut show %v err = %v (exit %d), want UsageError exit 2", tc.args, err, cli.ExitCode(err))
			}
		})
	}
}

func TestPapercutTwinConvergence(t *testing.T) {
	dir := initRepo(t)
	first := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "original complaint", "--json"))

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

	appended := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "converged complaint", "--json"))
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
	echo := showJSON[logJSON](t, dir, stdout)
	if len(echo.Entries) != 1 || echo.Entries[0].Text != "friction from stdin" {
		t.Fatalf("entries = %+v, want the stdin complaint with the trailing newline trimmed", echo.Entries)
	}
}

func TestPapercutBodyFlagAndConflict(t *testing.T) {
	dir := initRepo(t)

	flagged := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "--body", "flag friction", "--json"))
	if len(flagged.Entries) != 1 || flagged.Entries[0].Text != "flag friction" {
		t.Fatalf("entries = %+v, want the single --body complaint", flagged.Entries)
	}

	var usage *cli.UsageError
	if _, _, err := runCLI(t, dir, "papercut", "positional", "--body", "flag"); !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("papercut positional+--body err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
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
	echo := showJSON[logJSON](t, dir, mustRun(t, dir, "papercut", "--json", "--", "list"))
	if len(echo.Entries) != 1 || echo.Entries[0].Text != "list" {
		t.Fatalf("entries = %+v, want a complaint whose text is literally 'list'", echo.Entries)
	}
	listed := mustRun(t, dir, "papercut", "list")
	if !strings.Contains(listed, "\nlist\n") {
		t.Fatalf("papercut list = %q, want it to render the filed 'list' complaint", listed)
	}
}
