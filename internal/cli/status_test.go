package cli_test

import (
	"strings"
	"testing"
)

// statusJSONShape mirrors the status --json DTO for round-trip assertions.
type statusJSONShape struct {
	Branch     string     `json:"branch"`
	Backlog    []taskJSON `json:"backlog"`
	YourBranch []taskJSON `json:"your_branch"`
	InProgress []struct {
		Assignee string `json:"assignee"`
		Tasks    []struct {
			ID    string `json:"id"`
			Stale bool   `json:"stale"`
		} `json:"tasks"`
	} `json:"in_progress"`
	Notes struct {
		Total       int `json:"total"`
		NeedsReview int `json:"needs_review"`
	} `json:"notes"`
}

func hasTaskID(tasks []taskJSON, id string) bool {
	for _, t := range tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

func TestStatusJSON(t *testing.T) {
	dir := initRepo(t)
	back := addTask(t, dir, "Backlog item", "--backlog")
	open := addTask(t, dir, "Open on branch")
	claimed := addTask(t, dir, "Claimed work")
	mustRun(t, dir, "task", "claim", claimed.ID)
	mustRun(t, dir, "note", "add", "A note")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Branch != "main" {
		t.Fatalf("branch = %q, want main", st.Branch)
	}
	if len(st.Backlog) != 1 || st.Backlog[0].ID != back.ID {
		t.Fatalf("backlog = %+v, want only %s", st.Backlog, back.ID)
	}
	if len(st.YourBranch) != 2 || !hasTaskID(st.YourBranch, open.ID) || !hasTaskID(st.YourBranch, claimed.ID) {
		t.Fatalf("your_branch = %+v, want %s and %s", st.YourBranch, open.ID, claimed.ID)
	}
	if len(st.InProgress) != 1 || st.InProgress[0].Assignee != actorA {
		t.Fatalf("in_progress = %+v, want one group for %s", st.InProgress, actorA)
	}
	grp := st.InProgress[0].Tasks
	if len(grp) != 1 || grp[0].ID != claimed.ID {
		t.Fatalf("in_progress tasks = %+v, want only %s", grp, claimed.ID)
	}
	if grp[0].Stale {
		t.Fatalf("stale = true under 8760h ttl, want fresh")
	}
	if st.Notes.Total != 1 || st.Notes.NeedsReview != 0 {
		t.Fatalf("notes = %+v, want total 1 needs_review 0", st.Notes)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "1ns")
	st2 := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if !st2.InProgress[0].Tasks[0].Stale {
		t.Fatalf("stale = false under 1ns ttl, want STALE")
	}
}

func TestStatusText(t *testing.T) {
	dir := initRepo(t)
	back := addTask(t, dir, "Backlog item", "--backlog")
	claimed := addTask(t, dir, "Claimed work")
	mustRun(t, dir, "task", "claim", claimed.ID)
	mustRun(t, dir, "note", "add", "A note")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	out := mustRun(t, dir, "status")
	for _, want := range []string{
		"backlog\n",
		"  " + back.ID[:7] + "\t",
		"your branch (main)\n",
		"  " + claimed.ID[:7] + "\t",
		"in progress across branches\n",
		"  " + actorA + "\t" + claimed.ID[:7] + "\tfresh\n",
		"notes: 1 total, 0 need review\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status text %q missing %q", out, want)
		}
	}
}

func TestStatusDetachedHead(t *testing.T) {
	dir := initRepo(t)
	addTask(t, dir, "On main")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	mustGit(t, dir, "checkout", "-q", "--detach")

	out, _, err := runCLI(t, dir, "status")
	if err != nil {
		t.Fatalf("status on detached HEAD err = %v, want nil", err)
	}
	if strings.Contains(out, "your branch") {
		t.Fatalf("status text %q must omit the your-branch section on detached HEAD", out)
	}
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Branch != "" {
		t.Fatalf("branch = %q, want empty on detached HEAD", st.Branch)
	}
	if len(st.YourBranch) != 0 {
		t.Fatalf("your_branch = %+v, want empty on detached HEAD", st.YourBranch)
	}
}
