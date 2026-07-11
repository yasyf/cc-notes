package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	relevantMe    = "me@example.com"
	relevantOther = "other@example.com"
)

// relevantRepo initializes a repo on main with a deterministic local identity
// (me@example.com) so AuthorIdent resolves the same regardless of the test
// process environment, and chdirs into it.
func relevantRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	relevantGit(t, dir, relevantMe, "init", "-q", "-b", "main")
	relevantGit(t, dir, relevantMe, "config", "user.name", "Me")
	relevantGit(t, dir, relevantMe, "config", "user.email", relevantMe)
	t.Chdir(dir)
	return dir
}

// relevantGit runs git in dir authored by email, failing the test on error.
func relevantGit(t *testing.T, dir, email string, args ...string) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME=Author", "GIT_COMMITTER_EMAIL="+email,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitFileAs writes path with content in dir and commits it authored by
// email, returning the resulting commit sha.
func commitFileAs(t *testing.T, dir, email, path, content string) model.SHA {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	relevantGit(t, dir, email, "add", "-A")
	relevantGit(t, dir, email, "commit", "-q", "-m", "commit "+path)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	head, err := resolveHead(t.Context(), s)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	return head
}

// makeNote creates a note with id-stable nonce, the given anchors, and no
// verify (unverified) through the store, returning its entity id.
func makeNote(t *testing.T, dir, title string, anchors ...model.Anchor) model.EntityID {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	snap, err := s.Create(t.Context(), []model.Op{model.CreateNote{
		Nonce:   model.NewNonce(),
		Title:   title,
		Anchors: anchors,
	}})
	if err != nil {
		t.Fatalf("create note %q: %v", title, err)
	}
	return snap.(model.Note).ID
}

// makeDoc creates a doc with id-stable nonce, the given when trigger and
// anchors, and no verify (unverified) through the store, returning its entity
// id.
func makeDoc(t *testing.T, dir, title, when string, anchors ...model.Anchor) model.EntityID {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	snap, err := s.Create(t.Context(), []model.Op{model.CreateDoc{
		Nonce:   model.NewNonce(),
		Title:   title,
		When:    when,
		Anchors: anchors,
	}})
	if err != nil {
		t.Fatalf("create doc %q: %v", title, err)
	}
	return snap.(model.Doc).ID
}

// makeLog creates a log with an id-stable nonce, the given anchors, and one
// appended entry through the store, returning its entity id.
func makeLog(t *testing.T, dir, title, entry string, anchors ...model.Anchor) model.EntityID {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := t.Context()
	snap, err := s.Create(ctx, []model.Op{model.CreateLog{
		Nonce:   model.NewNonce(),
		Title:   title,
		Anchors: anchors,
	}})
	if err != nil {
		t.Fatalf("create log %q: %v", title, err)
	}
	id := snap.(model.Log).ID
	if entry != "" {
		if _, err := s.Append(ctx, refs.For(model.KindLog, id), []model.Op{model.AppendEntry{Text: entry}}); err != nil {
			t.Fatalf("append entry to log %s: %v", id, err)
		}
	}
	return id
}

// verifyNote stamps a real witness on a note against current HEAD, so the note
// reads as fresh until its anchored content drifts.
func verifyNote(t *testing.T, dir string, id model.EntityID) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := t.Context()
	_, note, err := noteSpec.load(ctx, s, string(id))
	if err != nil {
		t.Fatalf("load note %s: %v", id, err)
	}
	head, err := resolveHead(ctx, s)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	witness, err := buildWitness(ctx, s, head, note.Anchors)
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	if _, err := s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}}); err != nil {
		t.Fatalf("verify note %s: %v", id, err)
	}
}

// runRelevant scores notes and docs for target through the engine, failing on
// error.
func runRelevant(t *testing.T, dir, target string) []scoredEntity {
	t.Helper()
	scored, _, err := relevantForTest(t, dir, target, "", "", false, false)
	if err != nil {
		t.Fatalf("relevantNotes %q: %v", target, err)
	}
	return scored
}

// relevantForTest opens the store in dir and runs the relevance engine.
func relevantForTest(t *testing.T, dir, target, branchFlag, baseFlag string, attached, worktree bool) ([]scoredEntity, map[model.EntityID]string, error) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return relevantNotes(t.Context(), s, target, branchFlag, baseFlag, attached, worktree)
}

// scoredIDs returns the ordered entity ids of a scored slice.
func scoredIDs(scored []scoredEntity) []model.EntityID {
	out := make([]model.EntityID, len(scored))
	for i, m := range scored {
		out[i] = m.id()
	}
	return out
}

// findScored returns the scored entry for id, failing if absent.
func findScored(t *testing.T, scored []scoredEntity, id model.EntityID) scoredEntity {
	t.Helper()
	for _, m := range scored {
		if m.id() == id {
			return m
		}
	}
	t.Fatalf("entity %s not in results %v", id, scoredIDs(scored))
	return scoredEntity{}
}

func TestRelevantOrdering(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/login.go", "v1\n")

	pathNote := makeNote(t, dir, "exact path", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/login.go"})
	dirNote := makeNote(t, dir, "dir", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})
	sibNote := makeNote(t, dir, "sibling", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/logout.go"})
	branchNote := makeNote(t, dir, "branch only", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	scored := runRelevant(t, dir, "internal/auth/login.go")
	gotIDs := scoredIDs(scored)
	wantIDs := []model.EntityID{pathNote, dirNote, branchNote, sibNote}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("ids = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("order = %v, want %v", gotIDs, wantIDs)
		}
	}

	wantScore := map[model.EntityID]int{
		pathNote:   scorePath,
		dirNote:    scoreDir,
		branchNote: scoreBranch,
		sibNote:    scoreSibling,
	}
	wantReason := map[model.EntityID]string{
		pathNote:   reasonPath,
		dirNote:    reasonDir,
		branchNote: reasonBranch,
		sibNote:    reasonSibling,
	}
	for _, m := range scored {
		if m.score != wantScore[m.note.ID] {
			t.Errorf("note %s score = %d, want %d", m.note.ID, m.score, wantScore[m.note.ID])
		}
		if len(m.reasons) != 1 || m.reasons[0] != wantReason[m.note.ID] {
			t.Errorf("note %s reasons = %v, want [%s]", m.note.ID, m.reasons, wantReason[m.note.ID])
		}
	}
}

func TestRelevantDirAncestorMatch(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/oauth/token.go", "v1\n")

	// A dir anchor two levels up still matches a nested path.
	ancestor := makeNote(t, dir, "ancestor dir", model.Anchor{Kind: model.AnchorDir, Value: "internal"})
	// Overlapping dir anchors do not stack: the deepest wins, scored once.
	stacked := makeNote(
		t, dir, "stacked dirs",
		model.Anchor{Kind: model.AnchorDir, Value: "internal"},
		model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"},
	)

	scored := runRelevant(t, dir, "internal/auth/oauth/token.go")
	a := findScored(t, scored, ancestor)
	if a.score != scoreDir || len(a.reasons) != 1 || a.reasons[0] != reasonDir {
		t.Fatalf("ancestor dir = score %d reasons %v, want %d [dir]", a.score, a.reasons, scoreDir)
	}
	st := findScored(t, scored, stacked)
	if st.score != scoreDir || len(st.reasons) != 1 || st.reasons[0] != reasonDir {
		t.Fatalf("stacked dirs = score %d reasons %v, want %d [dir] (no stacking)", st.score, st.reasons, scoreDir)
	}
}

func TestRelevantSiblingSurfaces(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "pkg/a.go", "v1\n")

	sib := makeNote(t, dir, "sibling note", model.Anchor{Kind: model.AnchorPath, Value: "pkg/b.go"})
	// A path anchor in a different directory is not a sibling.
	unrelated := makeNote(t, dir, "unrelated", model.Anchor{Kind: model.AnchorPath, Value: "other/c.go"})

	scored := runRelevant(t, dir, "pkg/a.go")
	s := findScored(t, scored, sib)
	if s.score != scoreSibling || len(s.reasons) != 1 || s.reasons[0] != reasonSibling {
		t.Fatalf("sibling = score %d reasons %v, want %d [sibling]", s.score, s.reasons, scoreSibling)
	}
	for _, m := range scored {
		if m.note.ID == unrelated {
			t.Fatalf("unrelated note %s should not surface, got %v", unrelated, m.reasons)
		}
	}
}

func TestRelevantMergedCommitAndBranch(t *testing.T) {
	dir := relevantRepo(t)
	first := commitFileAs(t, dir, relevantMe, "core/x.go", "v1\n")

	// A feature branch whose tip later merges into main.
	relevantGit(t, dir, relevantMe, "branch", "feature")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "feature")
	featTip := commitFileAs(t, dir, relevantMe, "core/y.go", "v1\n")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "main")
	relevantGit(t, dir, relevantMe, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	mergedCommit := makeNote(t, dir, "merged commit", model.Anchor{Kind: model.AnchorCommit, Value: string(first)})
	mergedBranch := makeNote(t, dir, "merged branch", model.Anchor{Kind: model.AnchorBranch, Value: "feature"})
	_ = featTip

	scored := runRelevant(t, dir, "unrelated/path.go")
	mc := findScored(t, scored, mergedCommit)
	if mc.score != scoreMergedCommit || len(mc.reasons) != 1 || mc.reasons[0] != reasonMergedCommit {
		t.Fatalf("merged commit = score %d reasons %v, want %d [merged-commit]", mc.score, mc.reasons, scoreMergedCommit)
	}
	mb := findScored(t, scored, mergedBranch)
	if mb.score != scoreMergedBranch || len(mb.reasons) != 1 || mb.reasons[0] != reasonMergedBranch {
		t.Fatalf("merged branch = score %d reasons %v, want %d [merged-branch]", mb.score, mb.reasons, scoreMergedBranch)
	}
}

func TestRelevantCrossAuthorBoost(t *testing.T) {
	dir := relevantRepo(t)
	// Base commit on main; HEAD diverges with a teammate-authored file and a
	// self-authored file, both siblings of the target.
	commitFileAs(t, dir, relevantMe, "base.go", "v1\n")
	relevantGit(t, dir, relevantMe, "branch", "feat-base") // mark the merge-base ref
	commitFileAs(t, dir, relevantOther, "pkg/teammate.go", "theirs\n")
	commitFileAs(t, dir, relevantMe, "pkg/mine.go", "mine\n")

	target := "pkg/target.go"
	otherSib := makeNote(t, dir, "sibling on teammate file", model.Anchor{Kind: model.AnchorPath, Value: "pkg/teammate.go"})
	selfSib := makeNote(t, dir, "sibling on self file", model.Anchor{Kind: model.AnchorPath, Value: "pkg/mine.go"})

	scored, _, err := relevantForTest(t, dir, target, "", "feat-base", false, false)
	if err != nil {
		t.Fatalf("relevantNotes: %v", err)
	}
	other := findScored(t, scored, otherSib)
	self := findScored(t, scored, selfSib)
	if other.score != scoreSibling+scoreCrossAuthor {
		t.Fatalf("teammate sibling score = %d, want %d", other.score, scoreSibling+scoreCrossAuthor)
	}
	wantReasons := []string{reasonSibling, reasonCrossAuthor}
	if len(other.reasons) != 2 || other.reasons[0] != wantReasons[0] || other.reasons[1] != wantReasons[1] {
		t.Fatalf("teammate sibling reasons = %v, want %v", other.reasons, wantReasons)
	}
	if self.score != scoreSibling {
		t.Fatalf("self sibling score = %d, want %d (no cross-author boost)", self.score, scoreSibling)
	}
	if scored[0].note.ID != otherSib {
		t.Fatalf("teammate-file sibling should outrank self-file sibling, order = %v", scoredIDs(scored))
	}
}

func TestRelevantCrossAuthorNeverMatchesAlone(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "base.go", "v1\n")
	relevantGit(t, dir, relevantMe, "branch", "feat-base")
	commitFileAs(t, dir, relevantOther, "far/teammate.go", "theirs\n")

	// A note anchored only to the teammate file, far from the target, has no
	// path/dir/sibling match — cross-author cannot surface it alone.
	makeNote(t, dir, "far teammate note", model.Anchor{Kind: model.AnchorPath, Value: "far/teammate.go"})

	scored, _, err := relevantForTest(t, dir, "pkg/target.go", "", "feat-base", false, false)
	if err != nil {
		t.Fatalf("relevantNotes: %v", err)
	}
	if len(scored) != 0 {
		t.Fatalf("cross-author surfaced a note alone: %v", scoredIDs(scored))
	}
}

func TestRelevantAttachedDropsLooseSignals(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/login.go", "v1\n")

	pathNote := makeNote(t, dir, "exact path", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/login.go"})
	dirNote := makeNote(t, dir, "dir", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})
	makeNote(t, dir, "sibling", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/logout.go"})
	makeNote(t, dir, "branch only", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	scored, _, err := relevantForTest(t, dir, "internal/auth/login.go", "", "", true, false)
	if err != nil {
		t.Fatalf("relevantNotes: %v", err)
	}
	got := scoredIDs(scored)
	want := []model.EntityID{pathNote, dirNote}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("--attached ids = %v, want %v", got, want)
	}
}

func TestRelevantWorktreeDrift(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "svc/handler.go", "v1\n")

	id := makeNote(t, dir, "path note", model.Anchor{Kind: model.AnchorPath, Value: "svc/handler.go"})
	verifyNote(t, dir, id)

	// Edit the working tree without committing.
	if err := os.WriteFile(filepath.Join(dir, "svc", "handler.go"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty: %v", err)
	}

	plain, plainV, err := relevantForTest(t, dir, "svc/handler.go", "", "", false, false)
	if err != nil {
		t.Fatalf("relevantNotes plain: %v", err)
	}
	if len(plain) != 1 {
		t.Fatalf("plain results = %d, want 1", len(plain))
	}
	if v := plainV[id]; v != "" {
		t.Fatalf("plain verdict = %q, want fresh (committed blob unchanged)", v)
	}

	wt, wtV, err := relevantForTest(t, dir, "svc/handler.go", "", "", false, true)
	if err != nil {
		t.Fatalf("relevantNotes worktree: %v", err)
	}
	if len(wt) != 1 {
		t.Fatalf("worktree results = %d, want 1", len(wt))
	}
	if v := wtV[id]; v != verdictDrifted {
		t.Fatalf("worktree verdict = %q, want %q", v, verdictDrifted)
	}
}

func TestRelevantLimitTruncates(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/login.go", "v1\n")
	makeNote(t, dir, "path", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/login.go"})
	makeNote(t, dir, "dir", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})
	makeNote(t, dir, "branch", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	stdout := runRelevantCmd(t, dir, "--limit", "2", "internal/auth/login.go")
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("--limit 2 emitted %d lines:\n%s", len(lines), stdout)
	}
}

func TestRelevantJSON(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "svc/handler.go", "v1\n")

	id := makeNote(t, dir, "path note", model.Anchor{Kind: model.AnchorPath, Value: "svc/handler.go"})
	verifyNote(t, dir, id)
	if err := os.WriteFile(filepath.Join(dir, "svc", "handler.go"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty: %v", err)
	}

	stdout := runRelevantCmd(t, dir, "--worktree", "--json", "svc/handler.go")
	var dtos []relevantDTO
	if err := json.Unmarshal([]byte(stdout), &dtos); err != nil {
		t.Fatalf("unmarshal json %q: %v", stdout, err)
	}
	if len(dtos) != 1 {
		t.Fatalf("json results = %d, want 1", len(dtos))
	}
	d := dtos[0]
	if d.Note.ID != string(id) {
		t.Errorf("json id = %q, want %q", d.Note.ID, id)
	}
	if d.Score != scorePath {
		t.Errorf("json score = %d, want %d", d.Score, scorePath)
	}
	if len(d.Reasons) != 1 || d.Reasons[0] != reasonPath {
		t.Errorf("json reasons = %v, want [%s]", d.Reasons, reasonPath)
	}
	if d.Note.Drift == nil || *d.Note.Drift != verdictDrifted {
		t.Errorf("json drift = %v, want %q", d.Note.Drift, verdictDrifted)
	}
}

// runRelevantCmd runs the relevant cobra command in dir and returns its stdout.
func runRelevantCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	cmd := newRelevantCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("relevant %v: %v\n%s", args, err, stdout.String())
	}
	return stdout.String()
}

// nonEmptyLines splits s into its non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range bytes.Split([]byte(s), []byte("\n")) {
		if len(line) > 0 {
			out = append(out, string(line))
		}
	}
	return out
}

// scoredFixture builds a note-kind scoredEntity with a synthetic id and
// UpdatedAt for sort tests, where committing entities a second apart cannot
// reliably distinguish UpdatedAt at the engine's second granularity.
func scoredFixture(id string, score int, updatedAt int64) scoredEntity {
	return scoredEntity{
		kind:  model.KindNote,
		note:  model.Note{ID: model.EntityID(id), UpdatedAt: updatedAt},
		score: score,
	}
}

func TestCompareScoredTotalOrder(t *testing.T) {
	tests := []struct {
		name string
		in   []scoredEntity
		want []model.EntityID
	}{
		{
			name: "score descending dominates",
			in: []scoredEntity{
				scoredFixture("a", 15, 100),
				scoredFixture("b", 100, 50),
				scoredFixture("c", 60, 100),
			},
			want: []model.EntityID{"b", "c", "a"},
		},
		{
			name: "equal score breaks on UpdatedAt descending",
			in: []scoredEntity{
				scoredFixture("a", 60, 100),
				scoredFixture("b", 60, 300),
				scoredFixture("c", 60, 200),
			},
			want: []model.EntityID{"b", "c", "a"},
		},
		{
			name: "equal score and UpdatedAt breaks on id ascending",
			in: []scoredEntity{
				scoredFixture("c", 60, 100),
				scoredFixture("a", 60, 100),
				scoredFixture("b", 60, 100),
			},
			want: []model.EntityID{"a", "b", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slices.Clone(tt.in)
			slices.SortFunc(got, compareScored)
			ids := scoredIDs(got)
			if !slices.Equal(ids, tt.want) {
				t.Fatalf("order = %v, want %v", ids, tt.want)
			}
			// The order is a total order, so a second sort of the already-sorted
			// slice is a no-op — pinning determinism regardless of sort stability.
			again := slices.Clone(got)
			slices.SortFunc(again, compareScored)
			if !slices.Equal(scoredIDs(again), ids) {
				t.Fatalf("re-sort changed order: %v -> %v", ids, scoredIDs(again))
			}
		})
	}
}

func TestRelevantDetachedHead(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "x.go", "v1\n")
	commitFileAs(t, dir, relevantMe, "y.go", "v2\n")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "--detach", "HEAD")

	pathNote := makeNote(t, dir, "path", model.Anchor{Kind: model.AnchorPath, Value: "x.go"})
	// On a detached HEAD the branch resolves to "", so the direct "branch"
	// signal is skipped — but main's tip is an ancestor of the detached HEAD, so
	// the same note still surfaces via "merged-branch" (head != "" runs that
	// check with branch == ""). Degradation stays sensible, not an error.
	branchNote := makeNote(t, dir, "branch only", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	scored, _, err := relevantForTest(t, dir, "x.go", "", "", false, false)
	if err != nil {
		t.Fatalf("detached HEAD must not error: %v", err)
	}
	wantIDs := []model.EntityID{pathNote, branchNote}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("detached HEAD ids = %v, want %v", scoredIDs(scored), wantIDs)
	}
	pm := findScored(t, scored, pathNote)
	if pm.score != scorePath || len(pm.reasons) != 1 || pm.reasons[0] != reasonPath {
		t.Fatalf("detached path note = score %d reasons %v, want %d [path]", pm.score, pm.reasons, scorePath)
	}
	bm := findScored(t, scored, branchNote)
	if bm.score != scoreMergedBranch || len(bm.reasons) != 1 || bm.reasons[0] != reasonMergedBranch {
		t.Fatalf("detached branch note = score %d reasons %v, want %d [merged-branch] (no plain branch on detached HEAD)", bm.score, bm.reasons, scoreMergedBranch)
	}
}

func TestRelevantUnbornHead(t *testing.T) {
	dir := relevantRepo(t)
	// No commits: HEAD is unborn but the symbolic ref still names "main", so
	// HeadBranch resolves it (not detached). Path matching is structural and
	// still works; the branch signal still fires; every head-based signal
	// (merged-commit/merged-branch/worktree drift) is skipped without error.
	pathNote := makeNote(t, dir, "path", model.Anchor{Kind: model.AnchorPath, Value: "x.go"})
	branchNote := makeNote(t, dir, "branch", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	scored, _, err := relevantForTest(t, dir, "x.go", "", "", false, false)
	if err != nil {
		t.Fatalf("unborn HEAD must not error: %v", err)
	}
	wantIDs := []model.EntityID{pathNote, branchNote}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("unborn HEAD ids = %v, want %v", scoredIDs(scored), wantIDs)
	}
	pm := findScored(t, scored, pathNote)
	if pm.score != scorePath || len(pm.reasons) != 1 || pm.reasons[0] != reasonPath {
		t.Fatalf("unborn path note = score %d reasons %v, want %d [path]", pm.score, pm.reasons, scorePath)
	}
	bm := findScored(t, scored, branchNote)
	if bm.score != scoreBranch || len(bm.reasons) != 1 || bm.reasons[0] != reasonBranch {
		t.Fatalf("unborn branch note = score %d reasons %v, want %d [branch]", bm.score, bm.reasons, scoreBranch)
	}
}

func TestRelevantMergedBranchRefDeleted(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "core/x.go", "v1\n")
	relevantGit(t, dir, relevantMe, "branch", "feature")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "feature")
	commitFileAs(t, dir, relevantMe, "core/y.go", "v1\n")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "main")
	relevantGit(t, dir, relevantMe, "merge", "-q", "--no-ff", "-m", "merge", "feature")
	// The branch is deleted after merging, the common real-world shape.
	relevantGit(t, dir, relevantMe, "branch", "-D", "feature")

	makeNote(t, dir, "merged branch gone", model.Anchor{Kind: model.AnchorBranch, Value: "feature"})
	scored, _, err := relevantForTest(t, dir, "unrelated.go", "", "", false, false)
	if err != nil {
		t.Fatalf("deleted branch ref must skip, not error: %v", err)
	}
	if len(scored) != 0 {
		t.Fatalf("deleted branch ref surfaced %v, want none", scoredIDs(scored))
	}
}

func TestRelevantCrossAuthorExcludesSharedPath(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "base.go", "v1\n")
	relevantGit(t, dir, relevantMe, "branch", "feat-base")
	// pkg/shared.go is touched by the teammate first, then by me — I have seen
	// it, so it must not count as cross-author.
	commitFileAs(t, dir, relevantOther, "pkg/shared.go", "theirs\n")
	commitFileAs(t, dir, relevantMe, "pkg/shared.go", "mine too\n")

	sharedSib := makeNote(t, dir, "shared sibling", model.Anchor{Kind: model.AnchorPath, Value: "pkg/shared.go"})
	scored, _, err := relevantForTest(t, dir, "pkg/target.go", "", "feat-base", false, false)
	if err != nil {
		t.Fatalf("relevantNotes: %v", err)
	}
	m := findScored(t, scored, sharedSib)
	if m.score != scoreSibling {
		t.Fatalf("shared-path sibling score = %d, want %d (no cross-author boost)", m.score, scoreSibling)
	}
	if len(m.reasons) != 1 || m.reasons[0] != reasonSibling {
		t.Fatalf("shared-path sibling reasons = %v, want [%s]", m.reasons, reasonSibling)
	}
}

func TestRelevantLimitZeroEmitsNothing(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "pkg/a.go", "v1\n")
	makeNote(t, dir, "p", model.Anchor{Kind: model.AnchorPath, Value: "pkg/a.go"})
	makeNote(t, dir, "s", model.Anchor{Kind: model.AnchorPath, Value: "pkg/b.go"})

	stdout := runRelevantCmd(t, dir, "--limit", "0", "pkg/a.go")
	if lines := nonEmptyLines(stdout); len(lines) != 0 {
		t.Fatalf("--limit 0 emitted %d lines:\n%s", len(lines), stdout)
	}
}

func TestRelevantLimitNegativeUnlimited(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "pkg/a.go", "v1\n")
	makeNote(t, dir, "p", model.Anchor{Kind: model.AnchorPath, Value: "pkg/a.go"})
	makeNote(t, dir, "s1", model.Anchor{Kind: model.AnchorPath, Value: "pkg/b.go"})
	makeNote(t, dir, "s2", model.Anchor{Kind: model.AnchorPath, Value: "pkg/c.go"})

	stdout := runRelevantCmd(t, dir, "--limit", "-1", "pkg/a.go")
	if lines := nonEmptyLines(stdout); len(lines) != 3 {
		t.Fatalf("--limit -1 emitted %d lines, want 3:\n%s", len(lines), stdout)
	}
}

func TestRelevantRanksDocs(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/login.go", "v1\n")

	// A note anchored to the exact path (score 100) and a doc anchored to the
	// parent dir (score 60) — both score off the same anchor signals, ranked
	// together by score descending.
	pathNote := makeNote(t, dir, "exact path", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/login.go"})
	dirDoc := makeDoc(t, dir, "auth handoff", "resuming the auth cutover", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})

	scored := runRelevant(t, dir, "internal/auth/login.go")
	wantIDs := []model.EntityID{pathNote, dirDoc}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("ranked ids = %v, want %v (path note outranks dir doc)", scoredIDs(scored), wantIDs)
	}

	note := findScored(t, scored, pathNote)
	if note.kind != model.KindNote {
		t.Fatalf("note entry kind = %q, want %q", note.kind, model.KindNote)
	}

	doc := findScored(t, scored, dirDoc)
	if doc.kind != model.KindDoc {
		t.Fatalf("doc entry kind = %q, want %q", doc.kind, model.KindDoc)
	}
	if doc.doc.When != "resuming the auth cutover" {
		t.Fatalf("doc when = %q, want the verbatim trigger", doc.doc.When)
	}
	if doc.score != scoreDir || len(doc.reasons) != 1 || doc.reasons[0] != reasonDir {
		t.Fatalf("doc = score %d reasons %v, want %d [dir]", doc.score, doc.reasons, scoreDir)
	}
}

func TestRelevantDocJSON(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/api/client.go", "v1\n")

	noteID := makeNote(t, dir, "path note", model.Anchor{Kind: model.AnchorPath, Value: "internal/api/client.go"})
	docID := makeDoc(t, dir, "api handoff", "resuming the api cutover", model.Anchor{Kind: model.AnchorDir, Value: "internal/api"})

	stdout := runRelevantCmd(t, dir, "--json", "internal/api/client.go")
	var dtos []relevantDTO
	if err := json.Unmarshal([]byte(stdout), &dtos); err != nil {
		t.Fatalf("unmarshal json %q: %v", stdout, err)
	}
	if len(dtos) != 2 {
		t.Fatalf("json results = %d, want 2", len(dtos))
	}

	// The path note (score 100) ranks above the dir doc (score 60). A note entry
	// keeps its "note" key and omits "doc"; a doc entry carries "doc" (with the
	// verbatim trigger and verdict) and omits "note".
	noteEntry := dtos[0]
	if noteEntry.Kind != string(model.KindNote) || noteEntry.Note == nil || noteEntry.Doc != nil {
		t.Fatalf("entry[0] kind=%q note=%v doc=%v, want a note entry", noteEntry.Kind, noteEntry.Note, noteEntry.Doc)
	}
	if noteEntry.Note.ID != string(noteID) {
		t.Errorf("note id = %q, want %q", noteEntry.Note.ID, noteID)
	}

	docEntry := dtos[1]
	if docEntry.Kind != string(model.KindDoc) || docEntry.Doc == nil || docEntry.Note != nil {
		t.Fatalf("entry[1] kind=%q note=%v doc=%v, want a doc entry", docEntry.Kind, docEntry.Note, docEntry.Doc)
	}
	if docEntry.Doc.ID != string(docID) {
		t.Errorf("doc id = %q, want %q", docEntry.Doc.ID, docID)
	}
	if docEntry.Doc.When != "resuming the api cutover" {
		t.Errorf("doc when = %q, want the verbatim trigger", docEntry.Doc.When)
	}
	if docEntry.Score != scoreDir {
		t.Errorf("doc score = %d, want %d", docEntry.Score, scoreDir)
	}
	if len(docEntry.Reasons) != 1 || docEntry.Reasons[0] != reasonDir {
		t.Errorf("doc reasons = %v, want [%s]", docEntry.Reasons, reasonDir)
	}
	// A freshly created (unverified) doc carries its verdict in the doc DTO.
	if docEntry.Doc.Drift == nil || *docEntry.Doc.Drift != verdictUnverified {
		t.Errorf("doc drift = %v, want %q", docEntry.Doc.Drift, verdictUnverified)
	}
}

func TestRelevantRanksLogs(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/auth/login.go", "v1\n")

	// A note (exact path, 100), a doc (dir, 60), and a log (dir, 60) all score
	// off the same anchor signals and are ranked together by score descending.
	pathNote := makeNote(t, dir, "exact path", model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/login.go"})
	dirDoc := makeDoc(t, dir, "auth handoff", "resuming the auth cutover", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})
	dirLog := makeLog(t, dir, "auth rollout", "flipped to 5%", model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"})

	scored, verdicts, err := relevantForTest(t, dir, "internal/auth/login.go", "", "", false, false)
	if err != nil {
		t.Fatalf("relevantNotes: %v", err)
	}

	// All three surface; the path note (100) ranks first, the doc and log (both
	// 60) follow ordered by id.
	if got := scoredIDs(scored); len(got) != 3 || got[0] != pathNote {
		t.Fatalf("ranked ids = %v, want path note first then doc+log", got)
	}

	log := findScored(t, scored, dirLog)
	if log.kind != model.KindLog {
		t.Fatalf("log entry kind = %q, want %q", log.kind, model.KindLog)
	}
	if log.score != scoreDir || len(log.reasons) != 1 || log.reasons[0] != reasonDir {
		t.Fatalf("log = score %d reasons %v, want %d [dir]", log.score, log.reasons, scoreDir)
	}
	// A log never drifts: its verdict is always empty, no matter the anchored
	// content's state.
	if v := verdicts[dirLog]; v != "" {
		t.Fatalf("log verdict = %q, want empty (logs never drift)", v)
	}
	// The doc still carries its own verdict, proving the empty log verdict is not
	// a blanket suppression.
	if v := verdicts[dirDoc]; v != verdictUnverified {
		t.Fatalf("doc verdict = %q, want %q", v, verdictUnverified)
	}
}

func TestRelevantLogJSONAndLeanLine(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/api/client.go", "v1\n")
	logID := makeLog(t, dir, "api rollout", "first entry", model.Anchor{Kind: model.AnchorDir, Value: "internal/api"})

	jsonOut := runRelevantCmd(t, dir, "--json", "internal/api/client.go")
	var dtos []relevantDTO
	if err := json.Unmarshal([]byte(jsonOut), &dtos); err != nil {
		t.Fatalf("unmarshal json %q: %v", jsonOut, err)
	}
	if len(dtos) != 1 {
		t.Fatalf("json results = %d, want 1", len(dtos))
	}
	entry := dtos[0]
	if entry.Kind != string(model.KindLog) || entry.Log == nil || entry.Note != nil || entry.Doc != nil {
		t.Fatalf("entry kind=%q note=%v doc=%v log=%v, want a log entry", entry.Kind, entry.Note, entry.Doc, entry.Log)
	}
	if entry.Log.ID != string(logID) {
		t.Errorf("log id = %q, want %q", entry.Log.ID, logID)
	}
	if len(entry.Log.Entries) != 1 || entry.Log.Entries[0].Text != "first entry" {
		t.Errorf("log entries = %+v, want the one appended entry", entry.Log.Entries)
	}
	if entry.Score != scoreDir {
		t.Errorf("log score = %d, want %d", entry.Score, scoreDir)
	}

	lean := runRelevantCmd(t, dir, "internal/api/client.go")
	lines := nonEmptyLines(lean)
	if len(lines) != 1 {
		t.Fatalf("lean output = %d lines, want 1:\n%s", len(lines), lean)
	}
	line := lines[0]
	// The log line carries the dir reason and a log-show hint, and never a
	// verdict flag — logs never drift.
	for _, want := range []string{reasonDir, "log show " + logID.Short()} {
		if !strings.Contains(line, want) {
			t.Fatalf("log lean line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "[") {
		t.Fatalf("log lean line %q carries a verdict flag, want none (logs never drift)", line)
	}
}

func TestRelevantDocLeanLine(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "internal/api/client.go", "v1\n")
	docID := makeDoc(t, dir, "api handoff", "resuming the api cutover", model.Anchor{Kind: model.AnchorDir, Value: "internal/api"})

	stdout := runRelevantCmd(t, dir, "internal/api/client.go")
	lines := nonEmptyLines(stdout)
	if len(lines) != 1 {
		t.Fatalf("lean output = %d lines, want 1:\n%s", len(lines), stdout)
	}
	line := lines[0]
	// The doc line carries the verbatim trigger, the dir reason, the verdict flag
	// (unverified here), and a doc-show hint — never the long body.
	for _, want := range []string{"resuming the api cutover", reasonDir, "[unverified]", "doc show " + docID.Short()} {
		if !strings.Contains(line, want) {
			t.Fatalf("doc lean line %q missing %q", line, want)
		}
	}
}
