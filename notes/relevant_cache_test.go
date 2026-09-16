package notes_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/notes"
)

type relevantProbe struct {
	t       *testing.T
	c       *notes.Client
	dir     string
	target  string
	renders int
}

func (p *relevantProbe) render(entries []notes.RelevantEntry) ([]byte, error) {
	p.renders++
	return json.Marshal(entries)
}

func (p *relevantProbe) expect(step string, filter notes.RelevantFilter, recompute bool) {
	p.t.Helper()
	before := p.renders
	got, err := p.c.RelevantCached(p.t.Context(), p.target, filter, "json", p.render)
	if err != nil {
		p.t.Fatalf("%s: RelevantCached: %v", step, err)
	}
	fresh, err := p.c.Relevant(p.t.Context(), p.target, filter)
	if err != nil {
		p.t.Fatalf("%s: Relevant: %v", step, err)
	}
	want, err := json.Marshal(fresh)
	if err != nil {
		p.t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		p.t.Fatalf("%s %+v: cached answer differs from a fresh one\ncached %s\nfresh  %s", step, filter, got, want)
	}
	if recomputed := p.renders > before; recomputed != recompute {
		p.t.Fatalf("%s %+v: recomputed = %t, want %t", step, filter, recomputed, recompute)
	}
}

func settle(t *testing.T, dir, path string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, path), past, past); err != nil {
		t.Fatal(err)
	}
}

func TestRelevantCachedMatchesFreshAcrossInputs(t *testing.T) {
	c, dir := newClient(t)
	commitFile(t, dir, "internal/auth/login.go", "v1\n")
	settle(t, dir, "internal/auth/login.go")
	pathNote := makeNote(t, c, "exact path", notes.AnchorSpec{Paths: []string{"internal/auth/login.go"}})
	makeNote(t, c, "feature branch", notes.AnchorSpec{Branches: []string{"feature"}, Dirs: []string{"internal"}})

	p := &relevantProbe{t: t, c: c, dir: dir, target: "internal/auth/login.go"}
	filters := []notes.RelevantFilter{{}, {Attached: true, Worktree: true}}
	each := func(step string, recompute bool) {
		t.Helper()
		for _, f := range filters {
			p.expect(step, f, recompute)
		}
	}

	each("cold", true)
	each("warm", false)

	makeNote(t, c, "added", notes.AnchorSpec{Dirs: []string{"internal/auth"}})
	each("note added", true)
	each("note added, warm", false)

	title := "exact path, revised"
	if _, err := c.EditNote(t.Context(), pathNote, notes.NoteEdit{Title: &title}); err != nil {
		t.Fatalf("EditNote: %v", err)
	}
	each("note edited", true)

	commitFile(t, dir, "internal/auth/login.go", "v2\n")
	settle(t, dir, "internal/auth/login.go")
	each("commit moved HEAD", true)
	each("commit moved HEAD, warm", false)

	gittest.Git(t, dir, "branch", "feature")
	each("branch created at HEAD", true)
	gittest.Git(t, dir, "checkout", "-q", "feature")
	each("branch switched, no ref moved", true)
	each("branch switched, warm", false)
}

func TestRelevantCachedRevalidatesWorktreeAndClock(t *testing.T) {
	t.Setenv("CC_NOTES_NOTE_STALE_AFTER", "1h")
	c, dir := newClient(t)
	commitFile(t, dir, "svc/handler.go", "v1\n")
	settle(t, dir, "svc/handler.go")
	makeNote(t, c, "handler", notes.AnchorSpec{Paths: []string{"svc/handler.go"}})

	p := &relevantProbe{t: t, c: c, dir: dir, target: "svc/handler.go"}
	worktree := notes.RelevantFilter{Attached: true, Worktree: true}
	p.expect("cold", worktree, true)
	p.expect("warm", worktree, false)

	if err := os.WriteFile(filepath.Join(dir, "svc/handler.go"), []byte("uncommitted edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.expect("racy uncommitted edit is never cached", worktree, true)
	p.expect("racy uncommitted edit, again", worktree, true)
	settle(t, dir, "svc/handler.go")
	p.expect("settled uncommitted edit", worktree, true)
	p.expect("settled uncommitted edit, warm", worktree, false)

}

func TestRelevantCachedExpiresAtTheStalenessDeadline(t *testing.T) {
	t.Setenv("CC_NOTES_NOTE_STALE_AFTER", "8s")
	c, dir := newClient(t)
	commitFile(t, dir, "svc/handler.go", "v1\n")
	makeNote(t, c, "handler", notes.AnchorSpec{Paths: []string{"svc/handler.go"}})

	p := &relevantProbe{t: t, c: c, dir: dir, target: "svc/handler.go"}
	clean := notes.RelevantFilter{}
	p.expect("fresh verdict cold", clean, true)
	p.expect("fresh verdict warm", clean, false)
	time.Sleep(9 * time.Second)
	p.expect("fresh verdict past its staleness deadline", clean, true)
	p.expect("stale verdict warm", clean, false)
}
