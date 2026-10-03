package cli_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

func TestAddTitleFlagAliasesPositionalTitle(t *testing.T) {
	dir := initRepo(t)
	verbs := [][]string{
		{"answer", "add", "--body", "y"},
		{"note", "add", "--body", "y"},
		{"doc", "add", "--body", "y"},
		{"log", "add", "--entry", "y"},
		{"plan", "add", "--body", "y"},
		{"project", "add", "--body", "y"},
		{"runbook", "add", "--body", "y"},
		{"task", "add", "--body", "y", "--no-validation-criteria"},
		{"sprint", "add", "--body", "y"},
		{"ledger", "add", "--body", "y"},
		{"investigation", "open", "--body", "y"},
	}
	for _, verb := range verbs {
		t.Run(verb[0], func(t *testing.T) {
			title := "flagged " + verb[0]
			got := mustJSON[struct {
				Title string `json:"title"`
			}](t, mustRun(t, dir, append(verb, "--json", "--title", title)...))
			if got.Title != title {
				t.Fatalf("%s --title %q created title %q", strings.Join(verb[:2], " "), title, got.Title)
			}
		})
	}
}

func TestAddTitleFlagMatchingPositionalIsAccepted(t *testing.T) {
	dir := initRepo(t)
	got := mustJSON[struct {
		Title string `json:"title"`
	}](t, mustRun(t, dir, "note", "add", "--json", "--title", "same", "same", "body"))
	if got.Title != "same" {
		t.Fatalf("title = %q, want %q", got.Title, "same")
	}
}

func TestAddTitleFlagConflictingPositionalIsUsageError(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "answer", "add", "--title", "from flag", "--body", "y", "from positional")
	var usage *cli.UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a UsageError", err)
	}
	for _, want := range []string{`--title "from flag"`, `positional TITLE "from positional"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %s", err, want)
		}
	}
	if out := mustRun(t, dir, "answer", "list", "--json"); strings.Contains(out, "from") {
		t.Fatalf("conflicting add created an answer: %s", out)
	}
}
