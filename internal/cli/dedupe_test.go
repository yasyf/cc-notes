package cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestNoteAddDedupe drives two identical `note add` runs in-process: the second
// warns on stderr, exits 0, echoes the first note's id, and roots nothing, while
// a near-duplicate title still creates a second note.
func TestNoteAddDedupe(t *testing.T) {
	dir := initRepo(t)

	firstOut, firstErr, err := runCLI(t, dir, "note", "add", "T", "--body", "B", "--json")
	if err != nil {
		t.Fatalf("first add: %v (stderr %q)", err, firstErr)
	}
	if firstErr != "" {
		t.Fatalf("first add stderr = %q, want empty", firstErr)
	}
	first := mustJSON[noteJSON](t, firstOut)

	dupOut, dupErr, err := runCLI(t, dir, "note", "add", "T", "--body", "B", "--json")
	if err != nil {
		t.Fatalf("dup add: %v (stderr %q)", err, dupErr)
	}
	wantErr := fmt.Sprintf("cc-notes: exact duplicate of note %s; reusing the existing note (nothing created)\n", first.ID[:7])
	if dupErr != wantErr {
		t.Fatalf("dup add stderr = %q, want %q", dupErr, wantErr)
	}
	dup := mustJSON[noteJSON](t, dupOut)
	if dup.ID != first.ID {
		t.Errorf("dup add id = %s, want existing %s", dup.ID, first.ID)
	}
	if got := len(mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))); got != 1 {
		t.Fatalf("note count after dup = %d, want 1", got)
	}

	nearOut, nearErr, err := runCLI(t, dir, "note", "add", "T2", "--body", "B", "--json")
	if err != nil {
		t.Fatalf("near-dup add: %v (stderr %q)", err, nearErr)
	}
	if nearErr != "" {
		t.Fatalf("near-dup add stderr = %q, want empty", nearErr)
	}
	near := mustJSON[noteJSON](t, nearOut)
	if near.ID == first.ID {
		t.Errorf("near-dup reused id %s", near.ID)
	}
	if got := len(mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))); got != 2 {
		t.Fatalf("note count after near-dup = %d, want 2", got)
	}
}

// TestTaskAddDedupe mirrors TestNoteAddDedupe for tasks.
func TestTaskAddDedupe(t *testing.T) {
	dir := initRepo(t)

	firstOut, firstErr, err := runCLI(t, dir, "task", "add", "T", "--no-validation-criteria", "--json")
	if err != nil {
		t.Fatalf("first add: %v (stderr %q)", err, firstErr)
	}
	if firstErr != "" {
		t.Fatalf("first add stderr = %q, want empty", firstErr)
	}
	first := mustJSON[taskJSON](t, firstOut)

	dupOut, dupErr, err := runCLI(t, dir, "task", "add", "T", "--no-validation-criteria", "--json")
	if err != nil {
		t.Fatalf("dup add: %v (stderr %q)", err, dupErr)
	}
	wantErr := fmt.Sprintf("cc-notes: exact duplicate of task %s; reusing the existing task (nothing created)\n", first.ID[:7])
	if dupErr != wantErr {
		t.Fatalf("dup add stderr = %q, want %q", dupErr, wantErr)
	}
	dup := mustJSON[taskJSON](t, dupOut)
	if dup.ID != first.ID {
		t.Errorf("dup add id = %s, want existing %s", dup.ID, first.ID)
	}
	if got := len(mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--json"))); got != 1 {
		t.Fatalf("task count after dup = %d, want 1", got)
	}

	_, nearErr, err := runCLI(t, dir, "task", "add", "T2", "--no-validation-criteria", "--json")
	if err != nil {
		t.Fatalf("near-dup add: %v (stderr %q)", err, nearErr)
	}
	if nearErr != "" {
		t.Fatalf("near-dup add stderr = %q, want empty", nearErr)
	}
	if got := len(mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--json"))); got != 2 {
		t.Fatalf("task count after near-dup = %d, want 2", got)
	}
}

// TestNoteAddAfterExpireCreatesFresh proves an expired note never blocks
// re-asserting its fact: `note add` of the same content after `note expire`
// roots a fresh note (no dedupe warning), leaving the stale twin flagged.
func TestNoteAddAfterExpireCreatesFresh(t *testing.T) {
	dir := initRepo(t)

	first := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "T", "--body", "B", "--json"))
	mustRun(t, dir, "note", "expire", first.ID, "--reason", "outdated", "--json")

	out, stderr, err := runCLI(t, dir, "note", "add", "T", "--body", "B", "--json")
	if err != nil {
		t.Fatalf("re-add: %v (stderr %q)", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("re-add stderr = %q, want empty (fresh create, no dedupe)", stderr)
	}
	fresh := mustJSON[noteJSON](t, out)
	if fresh.ID == first.ID {
		t.Fatalf("re-add reused stale id %s, want a fresh note", fresh.ID)
	}
	if fresh.StaleAt != nil {
		t.Fatalf("fresh note stale_at = %q, want null", *fresh.StaleAt)
	}
	if n := len(mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))); n != 2 {
		t.Fatalf("note count = %d, want 2 (stale twin + fresh re-add)", n)
	}
	twin := mustJSON[noteJSON](t, mustRun(t, dir, "note", "show", first.ID, "--json"))
	if twin.StaleAt == nil {
		t.Fatalf("stale twin %s no longer flagged expired after re-add", first.ID[:7])
	}
}

// TestNoteAddDedupeRefreshesWitness proves a dedupe hit re-verifies the reused
// note instead of skipping verification: a second identical `note add` after the
// anchored file's content changed reuses the note but refreshes its path-anchor
// witness to the new blob and keeps verified_at set.
func TestNoteAddDedupeRefreshesWitness(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.go", "one")

	first := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "T", "--body", "B", "--path", "f.go", "--json"))
	if len(first.Anchors) != 1 || first.Anchors[0].Witness == nil {
		t.Fatalf("first add anchors = %+v, want one witnessed path anchor", first.Anchors)
	}
	w1 := *first.Anchors[0].Witness

	commitFile(t, dir, "f.go", "two")

	out, stderr, err := runCLI(t, dir, "note", "add", "T", "--body", "B", "--path", "f.go", "--json")
	if err != nil {
		t.Fatalf("dedupe re-add: %v (stderr %q)", err, stderr)
	}
	wantWarn := fmt.Sprintf("cc-notes: exact duplicate of note %s; reusing the existing note (nothing created)\n", first.ID[:7])
	if stderr != wantWarn {
		t.Fatalf("dedupe re-add stderr = %q, want %q", stderr, wantWarn)
	}
	dup := mustJSON[noteJSON](t, out)
	if dup.ID != first.ID {
		t.Fatalf("dedupe re-add id = %s, want existing %s", dup.ID, first.ID)
	}
	if len(dup.Anchors) != 1 || dup.Anchors[0].Witness == nil {
		t.Fatalf("dedupe re-add anchors = %+v, want one witnessed path anchor", dup.Anchors)
	}
	w2 := *dup.Anchors[0].Witness
	if w2 == w1 {
		t.Fatalf("witness unchanged after dedupe re-add (%s); want refreshed to the new blob", w2)
	}
	if wantOID := mustGit(t, dir, "rev-parse", "HEAD:f.go"); w2 != wantOID {
		t.Fatalf("refreshed witness = %s, want new blob %s", w2, wantOID)
	}
	if dup.VerifiedAt == nil {
		t.Fatal("dedupe re-add verified_at = null, want set (re-verified)")
	}
	if n := len(mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))); n != 1 {
		t.Fatalf("note count after dedupe re-add = %d, want 1 (reused, no twin)", n)
	}
}

// TestNoteAddDedupeBinary is the end-to-end contract: a second identical
// `note add` through the real binary exits 0, emits exactly the warning on
// stderr, and leaves a single note ref. execBin is used because mustBin asserts
// empty stderr, which the dedupe path deliberately violates.
func TestNoteAddDedupeBinary(t *testing.T) {
	dir := initRepo(t)

	first := mustBin(t, dir, actorA, "note", "add", "dup", "--json")
	firstID := mustJSON[noteJSON](t, first).ID

	res, err := execBin(dir, actorA, "note", "add", "dup", "--json")
	if err != nil {
		t.Fatalf("dup add: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("dup add exit = %d, want 0 (stderr %q)", res.Code, res.Stderr)
	}
	wantErr := fmt.Sprintf("cc-notes: exact duplicate of note %s; reusing the existing note (nothing created)\n", firstID[:7])
	if res.Stderr != wantErr {
		t.Fatalf("dup add stderr = %q, want %q", res.Stderr, wantErr)
	}
	if dupID := mustJSON[noteJSON](t, res.Stdout).ID; dupID != firstID {
		t.Errorf("dup add id = %s, want existing %s", dupID, firstID)
	}
	refsOut := mustGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes/notes/")
	if refCount := len(strings.Fields(refsOut)); refCount != 1 {
		t.Fatalf("note refs after dup = %d, want 1 (%q)", refCount, refsOut)
	}
}

// TestLogAddDedupeAppendsEntry proves `log add --entry` never loses the entry
// when Create dedupes. First: adding an entry to an existing entry-less log
// warns, reuses the log, and appends the entry. Second: two identical
// `log add T --entry Y` runs converge on one log carrying both invocations'
// entries — the log comparator ignores Entries, so the repeat reuses instead of
// minting a twin, and the append still lands.
func TestLogAddDedupeAppendsEntry(t *testing.T) {
	dir := initRepo(t)

	base := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Incident", "--json"))
	if len(base.Entries) != 0 {
		t.Fatalf("seed log entries = %d, want 0 on a bare add", len(base.Entries))
	}

	wantWarn := fmt.Sprintf("cc-notes: exact duplicate of log %s; reusing the existing log (nothing created)\n", base.ID[:7])
	out, stderr, err := runCLI(t, dir, "log", "add", "Incident", "--entry", "started", "--json")
	if err != nil {
		t.Fatalf("dedupe add --entry: %v (stderr %q)", err, stderr)
	}
	if stderr != wantWarn {
		t.Fatalf("dedupe add stderr = %q, want %q", stderr, wantWarn)
	}
	got := mustJSON[logJSON](t, out)
	if got.ID != base.ID {
		t.Fatalf("dedupe add id = %s, want existing %s", got.ID, base.ID)
	}
	if len(got.Entries) != 1 || got.Entries[0].Text != "started" {
		t.Fatalf("entries = %+v, want one 'started' entry appended on dedupe", got.Entries)
	}
	if n := len(mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--json"))); n != 1 {
		t.Fatalf("log count after dedupe add = %d, want 1 (reused, no twin)", n)
	}

	firstOut, firstErr, err := runCLI(t, dir, "log", "add", "Rollout", "--entry", "flip", "--json")
	if err != nil {
		t.Fatalf("first rollout add: %v (stderr %q)", err, firstErr)
	}
	if firstErr != "" {
		t.Fatalf("first rollout add stderr = %q, want empty", firstErr)
	}
	first := mustJSON[logJSON](t, firstOut)

	secondOut, secondErr, err := runCLI(t, dir, "log", "add", "Rollout", "--entry", "flip", "--json")
	if err != nil {
		t.Fatalf("second rollout add: %v (stderr %q)", err, secondErr)
	}
	wantSecondWarn := fmt.Sprintf("cc-notes: exact duplicate of log %s; reusing the existing log (nothing created)\n", first.ID[:7])
	if secondErr != wantSecondWarn {
		t.Fatalf("second rollout add stderr = %q, want %q", secondErr, wantSecondWarn)
	}
	second := mustJSON[logJSON](t, secondOut)
	if second.ID != first.ID {
		t.Fatalf("second rollout id = %s, want existing %s", second.ID, first.ID)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2 (both invocations appended)", second.Entries)
	}
	if n := len(mustJSON[[]logJSON](t, mustRun(t, dir, "log", "list", "--json"))); n != 2 {
		t.Fatalf("log count = %d, want 2 (Incident + Rollout, no twins)", n)
	}
}
