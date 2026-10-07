package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTextFileFlagsAcrossVerbs(t *testing.T) {
	dir := spInitRepo(t)
	note := spID(t, spMust(t, dir, "note", "add", "seed note", "--body", "seed", "--json"))
	task := spID(t, spMust(t, dir, "task", "add", "seed task", "--no-validation-criteria", "--json"))
	log := spID(t, spMust(t, dir, "log", "add", "seed log", "--json"))
	inv := spID(t, spMust(t, dir, "investigation", "open", "seed investigation", "--body", "the premise", "--json"))
	runbook := spID(t, spMust(t, dir, "runbook", "add", "seed runbook", "--json"))

	cases := []struct {
		name  string
		args  []string
		flag  string
		shows string
	}{
		{"note add", []string{"note", "add", "a note"}, "--body-file", ""},
		{"note edit", []string{"note", "edit", note}, "--body-file", note},
		{"doc add", []string{"doc", "add", "a doc", "--when", "always"}, "--body-file", ""},
		{"answer add", []string{"answer", "add", "which way?"}, "--body-file", ""},
		{"task add", []string{"task", "add", "a task", "--no-validation-criteria"}, "--body-file", ""},
		{"task comment", []string{"task", "comment", task}, "--body-file", task},
		{"papercut", []string{"papercut"}, "--body-file", ""},
		{"plan add", []string{"plan", "add", "a plan"}, "--body-file", ""},
		{"investigation append", []string{"investigation", "append", inv}, "--body-file", inv},
		{"log add", []string{"log", "add", "a log"}, "--entry-file", ""},
		{"log append", []string{"log", "append", log}, "--entry-file", log},
		{"runbook step add", []string{"runbook", "step", "add", runbook}, "--text-file", runbook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := "recorded by " + strings.ReplaceAll(tc.name, " ", "-")
			file := filepath.Join(t.TempDir(), "text.md")
			if err := os.WriteFile(file, []byte(marker+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := spMust(t, dir, append(slices.Clone(tc.args), tc.flag, file, "--json")...)
			id := tc.shows
			if id == "" {
				id = spID(t, out)
			}
			if shown := spMust(t, dir, "show", id); !strings.Contains(shown, marker) {
				t.Errorf("show %s lacks %q:\n%s", id, marker, shown)
			}
		})
	}
}

func TestBodyFileStdinAndConflicts(t *testing.T) {
	dir := spInitRepo(t)
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte("from the file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := spRun(t, dir, "from stdin\n", "note", "add", "piped", "--body-file", "-", "--json")
	if err != nil {
		t.Fatalf("note add --body-file -: %v", err)
	}
	if shown := spMust(t, dir, "show", spID(t, stdout)); !strings.Contains(shown, "from stdin") {
		t.Errorf("show lacks the piped body:\n%s", shown)
	}

	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"body and body-file", []string{"note", "add", "both", "--body", "inline", "--body-file", file}, 2},
		{"positional and body-file", []string{"task", "add", "both", "inline", "--no-validation-criteria", "--body-file", file}, 2},
		{"missing file", []string{"note", "add", "absent", "--body-file", filepath.Join(dir, "absent.md")}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := spRun(t, dir, "", tc.args...); ExitCode(err) != tc.wantCode {
				t.Fatalf("exit = %d (%v), want %d", ExitCode(err), err, tc.wantCode)
			}
		})
	}
}
