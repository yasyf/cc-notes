package cli

import (
	"testing"
)

// spShowBody reads back the stored body of the entity an add acknowledgement
// identifies. The ack is a summary, so the body lives with "show".
func spShowBody(t *testing.T, dir, ack string) string {
	t.Helper()
	return spJSON[noteDTO](t, spMust(t, dir, "show", spID(t, ack), "--json")).Body
}

// TestDocumentAddBodyForms proves the shared note/doc "add" resolves the body
// from exactly one of a positional BODY, --body, or - (stdin), rejects two
// sources as a usage error, and rejects a stray third positional.
func TestDocumentAddBodyForms(t *testing.T) {
	dir := spInitRepo(t)

	pos := spShowBody(t, dir, spMust(t, dir, "note", "add", "T1", "positional body", "--json"))
	if pos != "positional body" {
		t.Errorf("note positional body = %q, want %q", pos, "positional body")
	}

	flag := spShowBody(t, dir, spMust(t, dir, "note", "add", "T2", "--body", "flag body", "--json"))
	if flag != "flag body" {
		t.Errorf("note --body = %q, want %q", flag, "flag body")
	}

	out, _, err := spRun(t, dir, "stdin body\n\n", "note", "add", "T3", "-", "--json")
	if err != nil {
		t.Fatalf("note add - : %v", err)
	}
	if got := spShowBody(t, dir, out); got != "stdin body" {
		t.Errorf("note stdin body = %q, want %q (trailing newlines trimmed)", got, "stdin body")
	}

	// A doc's body is required; the positional satisfies it.
	doc := spShowBody(t, dir, spMust(t, dir, "doc", "add", "D1", "positional doc body", "--json"))
	if doc != "positional doc body" {
		t.Errorf("doc positional body = %q, want %q", doc, "positional doc body")
	}

	if _, _, err := spRun(t, dir, "", "note", "add", "T4", "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("note add positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}

	if _, _, err := spRun(t, dir, "", "note", "add", "T5", "a", "b"); !isUsage(err) {
		t.Errorf("note add with three positionals err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
}

// TestDocumentExpireExclusion proves note/doc "expire" rejects --reason with
// --clear through cobra's mutually-exclusive flag group (exit 2, not the old
// hand-rolled RunE check), while each flag alone is still accepted.
func TestDocumentExpireExclusion(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "note", "add", "T", "b", "--json"))

	if _, _, err := spRun(t, dir, "", "note", "expire", id, "--reason", "x", "--clear"); ExitCode(err) != 2 || !isFlagGroupError(err) {
		t.Errorf("note expire --reason --clear err = %v (exit %d), want flag-group usage error exit 2", err, ExitCode(err))
	}

	spMust(t, dir, "note", "expire", id, "--reason", "outdated")
	spMust(t, dir, "note", "expire", id, "--clear")
}
