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

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const (
	relevantMe    = "me@example.com"
	relevantOther = "other@example.com"
)

// relevantRepo scrubs the ambient git/cc-notes environment, initializes a repo
// on main with a deterministic local identity (me@example.com) so AuthorIdent
// resolves the same regardless of the test process environment, and chdirs
// into it.
func relevantRepo(t *testing.T) string {
	t.Helper()
	gittest.ScrubEnv(t)
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

// runRelevant scores notes and docs for target through the client, failing on
// error.
func runRelevant(t *testing.T, dir, target string) []notes.RelevantEntry {
	t.Helper()
	scored, err := relevantForTest(t, dir, target, "", "", false, false)
	if err != nil {
		t.Fatalf("Relevant %q: %v", target, err)
	}
	return scored
}

// relevantForTest opens the client in dir and runs the relevance scan.
func relevantForTest(t *testing.T, dir, target, branchFlag, baseFlag string, attached, worktree bool) ([]notes.RelevantEntry, error) {
	t.Helper()
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("notes.Open: %v", err)
	}
	return c.Relevant(t.Context(), target, notes.RelevantFilter{Branch: branchFlag, Base: baseFlag, Attached: attached, Worktree: worktree})
}

// entryID returns a scored entry's entity id, regardless of kind.
func entryID(e notes.RelevantEntry) model.EntityID {
	switch e.Kind {
	case model.KindDoc:
		return e.Doc.ID
	case model.KindLog:
		return e.Log.ID
	default:
		return e.Note.ID
	}
}

// scoredIDs returns the ordered entity ids of a scored slice.
func scoredIDs(scored []notes.RelevantEntry) []model.EntityID {
	out := make([]model.EntityID, len(scored))
	for i, e := range scored {
		out[i] = entryID(e)
	}
	return out
}

// findScored returns the scored entry for id, failing if absent.
func findScored(t *testing.T, scored []notes.RelevantEntry, id model.EntityID) notes.RelevantEntry {
	t.Helper()
	for _, e := range scored {
		if entryID(e) == id {
			return e
		}
	}
	t.Fatalf("entity %s not in results %v", id, scoredIDs(scored))
	return notes.RelevantEntry{}
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
	if d.Score != 100 {
		t.Errorf("json score = %d, want %d", d.Score, 100)
	}
	if len(d.Reasons) != 1 || d.Reasons[0] != "path" {
		t.Errorf("json reasons = %v, want [path]", d.Reasons)
	}
	if d.Note.Drift == nil || *d.Note.Drift != verdictDrifted {
		t.Errorf("json drift = %v, want %q", d.Note.Drift, verdictDrifted)
	}
}

// runRelevantCmd runs the relevant cobra command in dir and returns its stdout.
func runRelevantCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	root := NewRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs(append([]string{"relevant"}, args...))
	if err := root.ExecuteContext(t.Context()); err != nil {
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

func TestRelevantDetachedHead(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "x.go", "v1\n")
	commitFileAs(t, dir, relevantMe, "y.go", "v2\n")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "--detach", "HEAD")

	pathNote := makeNote(t, dir, "path", model.Anchor{Kind: model.AnchorPath, Value: "x.go"})
	// Detached at main's tip — the jj colocation norm. CurrentBranch resolves
	// main, so the branch anchor now scores via the plain "branch" signal, not
	// "merged-branch" (branchAnchorMerged skips an anchor equal to the branch).
	branchNote := makeNote(t, dir, "branch only", model.Anchor{Kind: model.AnchorBranch, Value: "main"})

	scored, err := relevantForTest(t, dir, "x.go", "", "", false, false)
	if err != nil {
		t.Fatalf("detached HEAD must not error: %v", err)
	}
	wantIDs := []model.EntityID{pathNote, branchNote}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("detached HEAD ids = %v, want %v", scoredIDs(scored), wantIDs)
	}
	pm := findScored(t, scored, pathNote)
	if pm.Score != 100 || len(pm.Reasons) != 1 || pm.Reasons[0] != "path" {
		t.Fatalf("detached path note = score %d reasons %v, want 100 [path]", pm.Score, pm.Reasons)
	}
	bm := findScored(t, scored, branchNote)
	if bm.Score != 40 || len(bm.Reasons) != 1 || bm.Reasons[0] != "branch" {
		t.Fatalf("detached branch note = score %d reasons %v, want 40 [branch] (main resolves on a detached-at-tip HEAD)", bm.Score, bm.Reasons)
	}
}

// TestRelevantAmbiguousHead pins the empty-branch degrade: on a genuinely
// unresolvable HEAD (no trunk, advanced past the sole bookmark) the branch
// resolves to "" and the plain "branch" signal is skipped, while a merged branch
// anchor still surfaces via "merged-branch".
func TestRelevantAmbiguousHead(t *testing.T) {
	dir := relevantRepo(t)
	// No trunk: rename the unborn main to wip so main never exists, then detach
	// and advance past wip's tip. CurrentBranch cannot resolve a branch.
	relevantGit(t, dir, relevantMe, "checkout", "-q", "-b", "wip")
	commitFileAs(t, dir, relevantMe, "x.go", "v1\n")
	relevantGit(t, dir, relevantMe, "checkout", "-q", "--detach", "HEAD")
	commitFileAs(t, dir, relevantMe, "y.go", "v2\n")

	pathNote := makeNote(t, dir, "path", model.Anchor{Kind: model.AnchorPath, Value: "x.go"})
	branchNote := makeNote(t, dir, "branch only", model.Anchor{Kind: model.AnchorBranch, Value: "wip"})

	// --base=wip: with no trunk the default cross-author base ("main") does not
	// resolve; name an existing base so the run exercises the branch degrade, not
	// that orthogonal path.
	scored, err := relevantForTest(t, dir, "x.go", "", "wip", false, false)
	if err != nil {
		t.Fatalf("ambiguous HEAD must not error: %v", err)
	}
	wantIDs := []model.EntityID{pathNote, branchNote}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("ambiguous HEAD ids = %v, want %v", scoredIDs(scored), wantIDs)
	}
	pm := findScored(t, scored, pathNote)
	if pm.Score != 100 || len(pm.Reasons) != 1 || pm.Reasons[0] != "path" {
		t.Fatalf("ambiguous path note = score %d reasons %v, want 100 [path]", pm.Score, pm.Reasons)
	}
	bm := findScored(t, scored, branchNote)
	if bm.Score != 20 || len(bm.Reasons) != 1 || bm.Reasons[0] != "merged-branch" {
		t.Fatalf("ambiguous branch note = score %d reasons %v, want 20 [merged-branch] (no plain branch on an unresolvable HEAD)", bm.Score, bm.Reasons)
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

	scored, err := relevantForTest(t, dir, "x.go", "", "", false, false)
	if err != nil {
		t.Fatalf("unborn HEAD must not error: %v", err)
	}
	wantIDs := []model.EntityID{pathNote, branchNote}
	if !slices.Equal(scoredIDs(scored), wantIDs) {
		t.Fatalf("unborn HEAD ids = %v, want %v", scoredIDs(scored), wantIDs)
	}
	pm := findScored(t, scored, pathNote)
	if pm.Score != 100 || len(pm.Reasons) != 1 || pm.Reasons[0] != "path" {
		t.Fatalf("unborn path note = score %d reasons %v, want 100 [path]", pm.Score, pm.Reasons)
	}
	bm := findScored(t, scored, branchNote)
	if bm.Score != 40 || len(bm.Reasons) != 1 || bm.Reasons[0] != "branch" {
		t.Fatalf("unborn branch note = score %d reasons %v, want 40 [branch]", bm.Score, bm.Reasons)
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
	scored, err := relevantForTest(t, dir, "unrelated.go", "", "", false, false)
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
	scored, err := relevantForTest(t, dir, "pkg/target.go", "", "feat-base", false, false)
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}
	m := findScored(t, scored, sharedSib)
	if m.Score != 15 {
		t.Fatalf("shared-path sibling score = %d, want 15 (no cross-author boost)", m.Score)
	}
	if len(m.Reasons) != 1 || m.Reasons[0] != "sibling" {
		t.Fatalf("shared-path sibling reasons = %v, want [sibling]", m.Reasons)
	}
}

// TestRelevantLimitZeroUnlimited pins the harmonized bindLimit contract: --limit
// 0 means "all", so a zero cap surfaces every relevant entry rather than
// truncating to nothing.
func TestRelevantLimitZeroUnlimited(t *testing.T) {
	dir := relevantRepo(t)
	commitFileAs(t, dir, relevantMe, "pkg/a.go", "v1\n")
	makeNote(t, dir, "p", model.Anchor{Kind: model.AnchorPath, Value: "pkg/a.go"})
	makeNote(t, dir, "s", model.Anchor{Kind: model.AnchorPath, Value: "pkg/b.go"})

	stdout := runRelevantCmd(t, dir, "--limit", "0", "pkg/a.go")
	if lines := nonEmptyLines(stdout); len(lines) != 2 {
		t.Fatalf("--limit 0 (all) emitted %d lines, want 2:\n%s", len(lines), stdout)
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
	if note.Kind != model.KindNote {
		t.Fatalf("note entry kind = %q, want %q", note.Kind, model.KindNote)
	}

	doc := findScored(t, scored, dirDoc)
	if doc.Kind != model.KindDoc {
		t.Fatalf("doc entry kind = %q, want %q", doc.Kind, model.KindDoc)
	}
	if doc.Doc.When != "resuming the auth cutover" {
		t.Fatalf("doc when = %q, want the verbatim trigger", doc.Doc.When)
	}
	if doc.Score != 60 || len(doc.Reasons) != 1 || doc.Reasons[0] != "dir" {
		t.Fatalf("doc = score %d reasons %v, want 60 [dir]", doc.Score, doc.Reasons)
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
	if docEntry.Score != 60 {
		t.Errorf("doc score = %d, want %d", docEntry.Score, 60)
	}
	if len(docEntry.Reasons) != 1 || docEntry.Reasons[0] != "dir" {
		t.Errorf("doc reasons = %v, want [dir]", docEntry.Reasons)
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

	scored, err := relevantForTest(t, dir, "internal/auth/login.go", "", "", false, false)
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}

	// All three surface; the path note (100) ranks first, the doc and log (both
	// 60) follow ordered by id.
	if got := scoredIDs(scored); len(got) != 3 || got[0] != pathNote {
		t.Fatalf("ranked ids = %v, want path note first then doc+log", got)
	}

	log := findScored(t, scored, dirLog)
	if log.Kind != model.KindLog {
		t.Fatalf("log entry kind = %q, want %q", log.Kind, model.KindLog)
	}
	if log.Score != 60 || len(log.Reasons) != 1 || log.Reasons[0] != "dir" {
		t.Fatalf("log = score %d reasons %v, want 60 [dir]", log.Score, log.Reasons)
	}
	// A log never drifts: its verdict is always empty, no matter the anchored
	// content's state.
	if v := findScored(t, scored, dirLog).Verdict; v != "" {
		t.Fatalf("log verdict = %q, want empty (logs never drift)", v)
	}
	// The doc still carries its own verdict, proving the empty log verdict is not
	// a blanket suppression.
	if v := findScored(t, scored, dirDoc).Verdict; v != notes.VerdictUnverified {
		t.Fatalf("doc verdict = %q, want %q", v, notes.VerdictUnverified)
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
	if entry.Score != 60 {
		t.Errorf("log score = %d, want %d", entry.Score, 60)
	}

	lean := runRelevantCmd(t, dir, "internal/api/client.go")
	lines := nonEmptyLines(lean)
	if len(lines) != 1 {
		t.Fatalf("lean output = %d lines, want 1:\n%s", len(lines), lean)
	}
	line := lines[0]
	// The log line carries the dir reason and a log-show hint, and never a
	// verdict flag — logs never drift.
	for _, want := range []string{"dir", "log show " + logID.Short()} {
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
	for _, want := range []string{"resuming the api cutover", "dir", "[unverified]", "doc show " + docID.Short()} {
		if !strings.Contains(line, want) {
			t.Fatalf("doc lean line %q missing %q", line, want)
		}
	}
}
