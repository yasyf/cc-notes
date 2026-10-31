package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const relevantMe = "me@example.com"

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
	t.Cleanup(func() { _ = s.Close() })
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
	t.Cleanup(func() { _ = s.Close() })
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
	t.Cleanup(func() { _ = s.Close() })
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
	t.Cleanup(func() { _ = s.Close() })
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
	t.Cleanup(func() { _ = s.Close() })
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
	if d.Note.Drift != verdictDrifted {
		t.Errorf("json drift = %q, want %q", d.Note.Drift, verdictDrifted)
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
	if docEntry.Doc.Drift != verdictUnverified {
		t.Errorf("doc drift = %q, want %q", docEntry.Doc.Drift, verdictUnverified)
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
	if entry.Log.EntryCount != 1 {
		t.Errorf("log entry count = %d, want the one appended entry", entry.Log.EntryCount)
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
