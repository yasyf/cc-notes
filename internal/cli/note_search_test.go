package cli_test

import (
	"testing"
)

func noteIDs(notes []noteJSON) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	return out
}

func TestNoteSearchWiring(t *testing.T) {
	dir := initRepo(t)
	titleHit := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Token expiry", "--label", "design", "--path", "auth.go", "--json"))
	bodyHit := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Other", "--body", "token rotation", "--label", "misc", "--json"))

	got := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "search", "token", "--json"))
	if len(got) != 2 || got[0].ID != titleHit.ID || got[1].ID != bodyHit.ID {
		t.Fatalf("search token = %v, want title hit %s before body hit %s", noteIDs(got), titleHit.ID, bodyHit.ID)
	}

	tagged := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "search", "token", "--label", "design", "--json"))
	if len(tagged) != 1 || tagged[0].ID != titleHit.ID {
		t.Fatalf("search --label design = %v, want only %s", noteIDs(tagged), titleHit.ID)
	}

	byAuthor := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "search", "token", "--author", actorA, "--json"))
	if len(byAuthor) != 2 {
		t.Fatalf("search --author %s = %v, want both notes", actorA, noteIDs(byAuthor))
	}
	if out := mustRun(t, dir, "note", "search", "token", "--author", "nobody <x@y>"); out != "" {
		t.Fatalf("search --author nobody = %q, want empty", out)
	}

	anchored := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "search", "token", "--path", "auth.go", "--json"))
	if len(anchored) != 1 || anchored[0].ID != titleHit.ID {
		t.Fatalf("search --path auth.go = %v, want only %s", noteIDs(anchored), titleHit.ID)
	}

	limited := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "search", "token", "--limit", "1", "--json"))
	if len(limited) != 1 || limited[0].ID != titleHit.ID {
		t.Fatalf("search --limit 1 = %v, want only %s", noteIDs(limited), titleHit.ID)
	}
}
