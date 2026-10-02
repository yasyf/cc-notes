package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// driftRepoInit scrubs the ambient git/cc-notes environment and creates a git
// repository on branch main in dir with a local identity, so the store's own
// commits — which inherit the test process env, not driftRepoGit's
// per-invocation GIT_AUTHOR_* — have a deterministic author.
func driftRepoInit(t *testing.T, dir string) {
	t.Helper()
	gittest.ScrubEnv(t)
	driftRepoGit(t, dir, "init", "-q", "-b", "main")
	driftRepoGit(t, dir, "config", "user.name", "Test User")
	driftRepoGit(t, dir, "config", "user.email", "test@example.com")
}

// driftRepoGit runs git in dir, failing the test on error.
func driftRepoGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
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

func showDrift(t *testing.T, dir string, id model.EntityID) notes.Verdict {
	t.Helper()
	t.Chdir(dir)
	root := NewRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"show", string(id), "--json"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("show %s: %v\n%s", id, err, stdout.String())
	}
	var shown struct {
		Drift notes.Verdict `json:"drift"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
		t.Fatalf("decode show %s: %v\n%s", id, err, stdout.String())
	}
	return shown.Drift
}

// TestShowUsesNotesVerdict anchors a note, a doc, and an answer to an old
// commit, grafts the history past it, and drifts a second note's path: the
// drift cc-notes show prints is notes.Client's verdict for every kind,
// HISTORY-UNAVAILABLE included.
func TestShowUsesNotesVerdict(t *testing.T) {
	dir := t.TempDir()
	driftRepoInit(t, dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(ccnhome.Env, t.TempDir())
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	commitDirFile(t, dir, "pkg/a.go", "v1\n")
	old := gittest.Git(t, dir, "rev-parse", "HEAD")
	commitDirFile(t, dir, "pkg/b.go", "v1\n")

	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("notes.Open: %v", err)
	}
	ctx := t.Context()
	onOld := notes.AnchorSpec{Commits: []string{old}}
	unknownNote, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "old commit", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	driftedNote, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "changed path", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"pkg/b.go"}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	freshNote, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "intact path", Body: "b", Anchors: notes.AnchorSpec{Paths: []string{"pkg/a.go"}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	unknownDoc, _, err := c.CreateDoc(ctx, notes.DocSpec{Title: "old commit", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	unknownAnswer, _, err := c.CreateAnswer(ctx, notes.NoteSpec{Title: "old commit?", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	commitDirFile(t, dir, "pkg/b.go", "v2\n")
	gittest.Shallow(t, dir, gittest.Git(t, dir, "rev-parse", "HEAD"))
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		t.Fatalf("NoteStaleAfter: %v", err)
	}

	noteVerdict := func(id model.EntityID) (notes.Verdict, error) {
		n, err := c.Note(ctx, id)
		if err != nil {
			return "", err
		}
		return c.NoteVerdict(ctx, n, staleAfter, false)
	}
	cases := []struct {
		name    string
		id      model.EntityID
		verdict func() (notes.Verdict, error)
		want    notes.Verdict
	}{
		{"note on an old commit", unknownNote.ID, func() (notes.Verdict, error) { return noteVerdict(unknownNote.ID) }, notes.VerdictHistoryUnavailable},
		{"note on a changed path", driftedNote.ID, func() (notes.Verdict, error) { return noteVerdict(driftedNote.ID) }, notes.VerdictDrifted},
		{"note on an intact path", freshNote.ID, func() (notes.Verdict, error) { return noteVerdict(freshNote.ID) }, ""},
		{"doc on an old commit", unknownDoc.ID, func() (notes.Verdict, error) {
			d, err := c.Doc(ctx, unknownDoc.ID)
			if err != nil {
				return "", err
			}
			return c.DocVerdict(ctx, d, staleAfter, false)
		}, notes.VerdictHistoryUnavailable},
		{"answer on an old commit", unknownAnswer.ID, func() (notes.Verdict, error) {
			a, err := c.Answer(ctx, unknownAnswer.ID)
			if err != nil {
				return "", err
			}
			return c.AnswerVerdict(ctx, a, staleAfter, false)
		}, notes.VerdictHistoryUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := tc.verdict()
			if err != nil {
				t.Fatalf("client verdict: %v", err)
			}
			if client != tc.want {
				t.Fatalf("fixture invalid: client verdict = %q, want %q", client, tc.want)
			}
			if shown := showDrift(t, dir, tc.id); shown != client {
				t.Errorf("show --json drift = %q, client verdict = %q", shown, client)
			}
		})
	}
}

func TestFilterVerdicts(t *testing.T) {
	rs := []reviewed[model.Note]{
		{entity: model.Note{ID: "a"}, verdict: verdictDrifted},
		{entity: model.Note{ID: "b"}, verdict: string(notes.VerdictStale)},
		{entity: model.Note{ID: "c"}, verdict: verdictUnverified},
		{entity: model.Note{ID: "d"}, verdict: verdictExpired},
	}
	if got := filterVerdicts(append([]reviewed[model.Note](nil), rs...), false, false, false); len(got) != 4 {
		t.Fatalf("no flags kept %d, want all 4", len(got))
	}
	drift := filterVerdicts(append([]reviewed[model.Note](nil), rs...), true, false, false)
	if len(drift) != 1 || drift[0].verdict != verdictDrifted {
		t.Fatalf("--drift = %+v, want only DRIFTED", drift)
	}
	unverified := filterVerdicts(append([]reviewed[model.Note](nil), rs...), false, true, false)
	if len(unverified) != 1 || unverified[0].verdict != verdictUnverified {
		t.Fatalf("--unverified = %+v, want only UNVERIFIED", unverified)
	}
	expired := filterVerdicts(append([]reviewed[model.Note](nil), rs...), false, false, true)
	if len(expired) != 1 || expired[0].verdict != verdictExpired {
		t.Fatalf("--expired = %+v, want only EXPIRED", expired)
	}
}

func ids(ns []model.Note) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
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
		ns := []model.Note{
			{ID: "body", Title: "Z", Body: "the widget breaks"},
			{ID: "title", Title: "Widget design"},
			{ID: "tag", Title: "Other", Tags: []string{"widget"}},
			{ID: "none", Title: "unrelated", Body: "nothing here"},
		}
		got := rankEntities(ns, "widget", nil, "", "", "", "", "", 20, noteRank)
		eqIDs(t, got, "title", "tag", "body")
	})

	t.Run("recency then id within tier", func(t *testing.T) {
		ns := []model.Note{
			{ID: "b", Title: "widget B", UpdatedAt: 100},
			{ID: "a", Title: "widget A", UpdatedAt: 200},
			{ID: "d", Title: "widget D", UpdatedAt: 100},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankEntities(ns, "widget", nil, "", "", "", "", "", 20, noteRank)
		eqIDs(t, got, "a", "b", "c", "d")
	})

	t.Run("limit truncation", func(t *testing.T) {
		ns := []model.Note{
			{ID: "a", Title: "widget A", UpdatedAt: 300},
			{ID: "b", Title: "widget B", UpdatedAt: 200},
			{ID: "c", Title: "widget C", UpdatedAt: 100},
		}
		got := rankEntities(ns, "widget", nil, "", "", "", "", "", 2, noteRank)
		eqIDs(t, got, "a", "b")
	})

	t.Run("tag filter narrows", func(t *testing.T) {
		ns := []model.Note{
			{ID: "yes", Title: "widget one", Tags: []string{"design"}},
			{ID: "no", Title: "widget two", Tags: []string{"misc"}},
		}
		got := rankEntities(ns, "widget", []string{"design"}, "", "", "", "", "", 20, noteRank)
		eqIDs(t, got, "yes")
	})

	t.Run("author filter narrows", func(t *testing.T) {
		ns := []model.Note{
			{ID: "yes", Title: "widget", Author: "ada <ada@example.com>"},
			{ID: "no", Title: "widget", Author: "ben <ben@example.com>"},
		}
		got := rankEntities(ns, "widget", nil, "ada <ada@example.com>", "", "", "", "", 20, noteRank)
		eqIDs(t, got, "yes")
	})

	t.Run("anchor filters narrow", func(t *testing.T) {
		ns := []model.Note{
			{ID: "yes", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}}},
			{ID: "no", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "b.go"}}},
		}
		got := rankEntities(ns, "widget", nil, "", "a.go", "", "", "", 20, noteRank)
		eqIDs(t, got, "yes")
	})

	t.Run("dir anchor filter narrows", func(t *testing.T) {
		ns := []model.Note{
			{ID: "yes", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/auth"}}},
			{ID: "no", Title: "widget", Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/sync"}}},
		}
		got := rankEntities(ns, "widget", nil, "", "", "internal/auth", "", "", 20, noteRank)
		eqIDs(t, got, "yes")
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		ns := []model.Note{{ID: "a", Title: "The Widget Factory"}}
		got := rankEntities(ns, "WIDGET", nil, "", "", "", "", "", 20, noteRank)
		eqIDs(t, got, "a")
	})
}

// TestWitnessReadsBoundContext opens a depth-1 clone bound to its full source
// after both moved apart: the HEAD and the path witness a born-verified note
// records come from the clone, never the records repository.
func TestWitnessReadsBoundContext(t *testing.T) {
	source := gittest.InitRepo(t)
	commitDirFile(t, source, "pkg/a.go", "v1\n")
	thin := gittest.ShallowClone(t, source, 1)
	commitDirFile(t, thin, "pkg/a.go", "thin\n")
	commitDirFile(t, source, "pkg/a.go", "source later\n")
	if _, err := store.Bind(t.Context(), thin, source); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	s, err := store.Open(thin)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := t.Context()

	head, err := resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if want := model.SHA(gittest.Git(t, thin, "rev-parse", "HEAD")); head != want {
		t.Fatalf("resolveHead = %s, want the clone's HEAD %s", head, want)
	}
	anchors := []model.Anchor{{Kind: model.AnchorPath, Value: "pkg/a.go"}, {Kind: model.AnchorDir, Value: "pkg"}}
	witness, err := buildWitness(ctx, s, head, anchors)
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	want := []model.AnchorWitness{
		{Anchor: anchors[0], OID: model.SHA(gittest.Git(t, thin, "rev-parse", "HEAD:pkg/a.go"))},
		{Anchor: anchors[1], OID: model.SHA(gittest.Git(t, thin, "rev-parse", "HEAD:pkg"))},
	}
	if !slices.Equal(witness, want) {
		t.Errorf("buildWitness = %+v, want the clone's oids %+v", witness, want)
	}
}
