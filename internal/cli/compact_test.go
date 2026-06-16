package cli_test

import (
	"strings"
	"testing"
)

func TestCompactNoteJSONAndLean(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Runbook", "--tag", "ops", "--json"))
	mustRun(t, dir, "note", "edit", added.ID, "--title", "Runbook v2", "--json")

	got := mustJSON[noteJSON](t, mustRun(t, dir, "compact", added.ID, "--json"))
	if got.ID != added.ID {
		t.Fatalf("compact id = %s, want %s (id is immutable)", got.ID, added.ID)
	}
	if got.Title != "Runbook v2" {
		t.Fatalf("compact title = %q, want Runbook v2 (state preserved)", got.Title)
	}
	if want := []string{"ops"}; len(got.Tags) != 1 || got.Tags[0] != want[0] {
		t.Fatalf("compact tags = %v, want %v", got.Tags, want)
	}

	lean := mustRun(t, dir, "compact", added.ID)
	if !strings.HasPrefix(lean, added.ID[:7]) {
		t.Fatalf("compact lean = %q, want prefix %s", lean, added.ID[:7])
	}
	if !strings.Contains(lean, "Runbook v2") {
		t.Fatalf("compact lean = %q, want it to carry the title", lean)
	}
}

func TestCompactTaskAndUnknownID(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")
	mustRun(t, dir, "task", "start", task.ID, "--json")

	got := mustJSON[taskJSON](t, mustRun(t, dir, "compact", task.ID, "--json"))
	if got.ID != task.ID {
		t.Fatalf("compact id = %s, want %s", got.ID, task.ID)
	}
	if got.Status != "in_progress" {
		t.Fatalf("compact status = %q, want in_progress (state preserved)", got.Status)
	}

	if _, _, err := runCLI(t, dir, "compact", "ffffffff"); err == nil {
		t.Fatal("compact of an unknown id returned nil error")
	}
}
