package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// driftRepoGit runs git in dir, failing the test on error.
func driftRepoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitDirFile writes path (under a directory) with content in dir and commits
// it, giving a dir anchor real subtree content to witness and drift.
func commitDirFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	driftRepoGit(t, dir, "add", "-A")
	driftRepoGit(t, dir, "commit", "-q", "-m", "commit "+path)
}

func TestNoteDirAnchorDrift(t *testing.T) {
	dir := t.TempDir()
	driftRepoGit(t, dir, "init", "-q", "-b", "main")
	commitDirFile(t, dir, "internal/auth/login.go", "v1\n")

	t.Chdir(dir)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := t.Context()
	head, err := resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if head == "" {
		t.Fatal("HEAD is unborn after a commit")
	}

	anchors := []model.Anchor{{Kind: model.AnchorDir, Value: "internal/auth"}}
	witness, err := buildWitness(ctx, s, head, anchors)
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	if len(witness) != 1 || witness[0].Anchor != anchors[0] || witness[0].OID == "" {
		t.Fatalf("witness = %+v, want one dir-anchor witness with a tree oid", witness)
	}
	note := model.Note{Anchors: anchors, Witness: witness}

	drifted, err := noteDrifted(ctx, s, head, note, false)
	if err != nil {
		t.Fatalf("noteDrifted (unchanged): %v", err)
	}
	if drifted {
		t.Fatal("dir anchor drifted with no change to the subtree")
	}

	commitDirFile(t, dir, "internal/auth/login.go", "v2\n")
	head, err = resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead after edit: %v", err)
	}
	drifted, err = noteDrifted(ctx, s, head, note, false)
	if err != nil {
		t.Fatalf("noteDrifted (changed): %v", err)
	}
	if !drifted {
		t.Fatal("dir anchor did not drift after a file under it changed")
	}

	driftRepoGit(t, dir, "rm", "-q", "-r", "internal/auth")
	driftRepoGit(t, dir, "commit", "-q", "-m", "remove internal/auth")
	head, err = resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead after delete: %v", err)
	}
	drifted, err = noteDrifted(ctx, s, head, note, false)
	if err != nil {
		t.Fatalf("noteDrifted (deleted): %v", err)
	}
	if !drifted {
		t.Fatal("dir anchor did not drift after the directory was deleted")
	}
}

func TestNoteVerdict(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	t.Run("never verified is UNVERIFIED before any git read", func(t *testing.T) {
		// A zero VerifiedAt short-circuits, so the nil store is never touched.
		got, err := noteVerdict(t.Context(), nil, "", model.Note{}, now, time.Hour, false)
		if err != nil {
			t.Fatalf("noteVerdict: %v", err)
		}
		if got != verdictUnverified {
			t.Fatalf("verdict = %q, want %q", got, verdictUnverified)
		}
	})
	t.Run("verified within threshold against unborn HEAD is fresh", func(t *testing.T) {
		n := model.Note{VerifiedAt: now.Add(-time.Minute).Unix()}
		got, err := noteVerdict(t.Context(), nil, "", n, now, time.Hour, false)
		if err != nil {
			t.Fatalf("noteVerdict: %v", err)
		}
		if got != "" {
			t.Fatalf("verdict = %q, want fresh", got)
		}
	})
	t.Run("verified past threshold is STALE", func(t *testing.T) {
		n := model.Note{VerifiedAt: now.Add(-2 * time.Hour).Unix()}
		got, err := noteVerdict(t.Context(), nil, "", n, now, time.Hour, false)
		if err != nil {
			t.Fatalf("noteVerdict: %v", err)
		}
		if got != verdictStale {
			t.Fatalf("verdict = %q, want %q", got, verdictStale)
		}
	})
}

func TestFilterVerdicts(t *testing.T) {
	reviewed := []reviewedNote{
		{note: model.Note{ID: "a"}, verdict: verdictDrifted},
		{note: model.Note{ID: "b"}, verdict: verdictStale},
		{note: model.Note{ID: "c"}, verdict: verdictUnverified},
		{note: model.Note{ID: "d"}, verdict: verdictExpired},
	}
	if got := filterVerdicts(append([]reviewedNote(nil), reviewed...), false, false, false); len(got) != 4 {
		t.Fatalf("no flags kept %d, want all 4", len(got))
	}
	drift := filterVerdicts(append([]reviewedNote(nil), reviewed...), true, false, false)
	if len(drift) != 1 || drift[0].verdict != verdictDrifted {
		t.Fatalf("--drift = %+v, want only DRIFTED", drift)
	}
	unverified := filterVerdicts(append([]reviewedNote(nil), reviewed...), false, true, false)
	if len(unverified) != 1 || unverified[0].verdict != verdictUnverified {
		t.Fatalf("--unverified = %+v, want only UNVERIFIED", unverified)
	}
	expired := filterVerdicts(append([]reviewedNote(nil), reviewed...), false, false, true)
	if len(expired) != 1 || expired[0].verdict != verdictExpired {
		t.Fatalf("--expired = %+v, want only EXPIRED", expired)
	}
}

func ids(notes []model.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = string(n.ID)
	}
	return out
}

func eqIDs(t *testing.T, got []model.Note, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Fatalf("ids = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("ids = %v, want %v", g, want)
		}
	}
}

func TestRankNotes(t *testing.T) {
	t.Run("tier order title>tag>body", func(t *testing.T) {
		notes := []model.Note{
			{ID: "body", Title: "Z", Body: "the widget breaks"},
			{ID: "title", Title: "Widget design"},
			{ID: "tag", Title: "Other", Tags: []string{"widget"}},
			{ID: "none", Title: "unrelated", Body: "nothing here"},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", "", 20)
		eqIDs(t, got, "title", "tag", "body")
	})

	t.Run("recency then id within tier", func(t *testing.T) {
		notes := []model.Note{
			{ID: "b", Title: "widget B", UpdatedAt: 100},
			{ID: "a", Title: "widget A", UpdatedAt: 200},
			{ID: "d", Title: "widget D", UpdatedAt: 100},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", "", 20)
		eqIDs(t, got, "a", "b", "c", "d")
	})

	t.Run("limit truncation", func(t *testing.T) {
		notes := []model.Note{
			{ID: "a", Title: "widget A", UpdatedAt: 300},
			{ID: "b", Title: "widget B", UpdatedAt: 200},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankNotes(notes, "widget", nil, "", "", "", "", "", 2)
		eqIDs(t, got, "a", "b")
	})

	t.Run("tag filter narrows", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget one", Tags: []string{"design"}},
			{ID: "no", Title: "widget two", Tags: []string{"misc"}},
		}
		got := rankNotes(notes, "widget", []string{"design"}, "", "", "", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("author filter narrows", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget", Author: "ada <ada@example.com>"},
			{ID: "no", Title: "widget", Author: "ben <ben@example.com>"},
		}
		got := rankNotes(notes, "widget", nil, "ada <ada@example.com>", "", "", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("anchor filters narrow", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "no", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "b.go"}}},
		}
		got := rankNotes(notes, "widget", nil, "", "a.go", "", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("dir anchor filter narrows", func(t *testing.T) {
		notes := []model.Note{
			{ID: "yes", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/auth"}}},
			{ID: "no", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/sync"}}},
		}
		got := rankNotes(notes, "widget", nil, "", "", "internal/auth", "", "", 20)
		eqIDs(t, got, "yes")
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		notes := []model.Note{{ID: "a", Title: "The Widget Factory"}}
		got := rankNotes(notes, "WIDGET", nil, "", "", "", "", "", 20)
		eqIDs(t, got, "a")
	})
}
