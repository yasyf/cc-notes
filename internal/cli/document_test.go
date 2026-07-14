package cli

import (
	"testing"
)

// TestDocumentAddBodyForms proves the shared note/doc "add" resolves the body
// from exactly one of a positional BODY, --body, or - (stdin), rejects two
// sources as a usage error, and rejects a stray third positional.
func TestDocumentAddBodyForms(t *testing.T) {
	dir := spInitRepo(t)

	pos := spJSON[noteDTO](t, spMust(t, dir, "note", "add", "T1", "positional body", "--json"))
	if pos.Body != "positional body" {
		t.Errorf("note positional body = %q, want %q", pos.Body, "positional body")
	}

	flag := spJSON[noteDTO](t, spMust(t, dir, "note", "add", "T2", "--body", "flag body", "--json"))
	if flag.Body != "flag body" {
		t.Errorf("note --body = %q, want %q", flag.Body, "flag body")
	}

	out, _, err := spRun(t, dir, "stdin body\n\n", "note", "add", "T3", "-", "--json")
	if err != nil {
		t.Fatalf("note add - : %v", err)
	}
	if got := spJSON[noteDTO](t, out).Body; got != "stdin body" {
		t.Errorf("note stdin body = %q, want %q (trailing newlines trimmed)", got, "stdin body")
	}

	// A doc's body is required; the positional satisfies it.
	doc := spJSON[docDTO](t, spMust(t, dir, "doc", "add", "D1", "positional doc body", "--json"))
	if doc.Body != "positional doc body" {
		t.Errorf("doc positional body = %q, want %q", doc.Body, "positional doc body")
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
	n := spJSON[noteDTO](t, spMust(t, dir, "note", "add", "T", "b", "--json"))

	if _, _, err := spRun(t, dir, "", "note", "expire", n.ID, "--reason", "x", "--clear"); ExitCode(err) != 2 || !isFlagGroupError(err) {
		t.Errorf("note expire --reason --clear err = %v (exit %d), want flag-group usage error exit 2", err, ExitCode(err))
	}

	spMust(t, dir, "note", "expire", n.ID, "--reason", "outdated")
	spMust(t, dir, "note", "expire", n.ID, "--clear")
}
