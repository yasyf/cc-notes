package notes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func relevantEntryOf(ctx context.Context, t *testing.T, c *Client, target string, filter RelevantFilter) (relevantCacheEntry, bool) {
	t.Helper()
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := c.s.ReadRelevantCache(relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, "json"))
	if !ok {
		return relevantCacheEntry{}, false
	}
	var entry relevantCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("decode cache entry: %v", err)
	}
	return entry, true
}

func TestRelevantCachedRacyEntryWaitsOutTheWindow(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	written := time.Now()
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	call := func(step string, wantRenders int) relevantCacheEntry {
		t.Helper()
		if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
			t.Fatalf("%s: RelevantCached: %v", step, err)
		}
		if renders != wantRenders {
			t.Fatalf("%s: renders = %d, want %d", step, renders, wantRenders)
		}
		entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{})
		if !ok {
			t.Fatalf("%s: no cache entry was written", step)
		}
		return entry
	}

	cold := call("cold", 1)
	if !cold.Racy {
		t.Fatal("a capture within the racy window of the entity write must be marked racy")
	}
	inside := call("inside the window", 1)
	if !inside.Racy || inside.Built <= cold.Built {
		t.Fatalf("inside the window: racy = %t built %d (cold %d); want a racy rewrite by the key tier", inside.Racy, inside.Built, cold.Built)
	}
	time.Sleep(time.Until(written.Add(relevantRacyWindow + 200*time.Millisecond)))
	promoted := call("after the window", 1)
	if promoted.Racy || promoted.Built <= inside.Built {
		t.Fatalf("after the window: racy = %t built %d (inside %d); want a settled rewrite", promoted.Racy, promoted.Built, inside.Built)
	}
	warm := call("warm", 1)
	if warm.Built != promoted.Built {
		t.Fatalf("warm: built %d, want the promoted entry %d left untouched by the warm tier", warm.Built, promoted.Built)
	}
}

func TestFileStampRacyCoversCtime(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour).UnixNano()
	fresh := now.Add(-time.Second).UnixNano()
	cases := []struct {
		name      string
		stamp     fileStamp
		racy      bool
		racyMtime bool
	}{
		{"settled", fileStamp{ModTime: old, Ctime: old}, false, false},
		{"fresh mtime", fileStamp{ModTime: fresh, Ctime: old}, true, true},
		{"fresh ctime", fileStamp{ModTime: old, Ctime: fresh}, true, false},
		{"missing", fileStamp{Missing: true, ModTime: fresh, Ctime: fresh}, false, false},
		{"device", fileStamp{Mode: os.ModeDevice}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stamp.racy(now); got != tc.racy {
				t.Fatalf("racy = %t, want %t", got, tc.racy)
			}
			if got := tc.stamp.racyMtime(now); got != tc.racyMtime {
				t.Fatalf("racyMtime = %t, want %t", got, tc.racyMtime)
			}
		})
	}

	file := filepath.Join(t.TempDir(), "backdated")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}
	stamp := stampOf(file)
	if !stamp.racy(time.Now()) {
		t.Fatalf("a backdated mtime behind a fresh ctime must be racy: %+v", stamp)
	}
	if stamp.racyMtime(time.Now()) {
		t.Fatalf("the drift-file rule reads mtime only: %+v", stamp)
	}
}

func TestRelevantCacheEntryHitGuards(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	settled := relevantCacheEntry{Env: relevantEnv(), Exe: exe, Git: git, Built: now.Add(-time.Minute).UnixNano()}
	cases := []struct {
		name string
		edit func(e *relevantCacheEntry)
		hit  bool
	}{
		{"settled", func(*relevantCacheEntry) {}, true},
		{"built in the future", func(e *relevantCacheEntry) { e.Built = now.Add(time.Minute).UnixNano() }, false},
		{"racy", func(e *relevantCacheEntry) { e.Racy = true }, false},
		{"revalidate", func(e *relevantCacheEntry) { e.Revalidate = true }, false},
		{"past the deadline", func(e *relevantCacheEntry) { e.Deadline = now.Add(-time.Second).UnixNano() }, false},
		{"other git", func(e *relevantCacheEntry) { e.Git = filepath.Join(t.TempDir(), "git") }, false},
		{"other environment", func(e *relevantCacheEntry) { e.Env = "0" }, false},
		{"moved stamp", func(e *relevantCacheEntry) { e.Stamps = []fileStamp{{Path: exe, Missing: true}} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := settled
			tc.edit(&entry)
			if got := entry.hit(now); got != tc.hit {
				t.Fatalf("hit = %t, want %t", got, tc.hit)
			}
		})
	}
}

func TestRelevantInputsAuditConfig(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	include := filepath.Join(dir, "extra.inc")
	if err := os.WriteFile(config, []byte("[user]\n\temail = a@b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []gitcmd.ConfigEntry{{Origin: "file:" + config, Key: "user.email", Value: "a@b", HasValue: true}}
	origins := map[string]bool{config: true}
	cases := []struct {
		name      string
		watched   map[string]fileStamp
		files     []string
		dump      string
		untrusted bool
	}{
		{"consistent", map[string]fileStamp{config: stampOf(config)}, []string{config}, configText(entries), false},
		{"include target read before it was stamped", map[string]fileStamp{config: stampOf(config)}, []string{config, include}, configText(entries), true},
		{"origin stamped missing", map[string]fileStamp{config: {Path: config, Missing: true}}, []string{config}, configText(entries), true},
		{"var list disagrees with the listing", map[string]fileStamp{config: stampOf(config)}, []string{config}, "user.email=other\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &relevantInputs{watched: tc.watched, guarded: make(map[string]bool)}
			in.auditConfig(entries, tc.files, origins, tc.dump)
			if in.untrusted != tc.untrusted {
				t.Fatalf("untrusted = %t, want %t", in.untrusted, tc.untrusted)
			}
		})
	}
}

func TestRelevantCachedRefusesACaptureWhoseGuardMoved(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "config", "include.path", "extra.inc")
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	script := "#!/bin/sh\nif [ -n \"$RELEVANT_GUARD_TOUCH\" ]; then for a in \"$@\"; do [ \"$a\" = for-each-ref ] && : > \"$RELEVANT_GUARD_TOUCH\" && rm -f \"$RELEVANT_GUARD_TOUCH\"; done; fi\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("RELEVANT_GUARD_TOUCH", filepath.Join(c.s.CommonDir(), "extra.inc"))
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))

	render := func(entries []RelevantEntry) ([]byte, error) { return json.Marshal(entries) }
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	if entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{}); ok {
		t.Fatalf("an include target created and deleted during capture left no trace on its stamp, yet the entry was written: %+v", entry.Stamps)
	}
	t.Setenv("RELEVANT_GUARD_TOUCH", "")
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	if _, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{}); !ok {
		t.Fatal("a quiet capture must write the entry")
	}
}

func TestRelevantCachedPersistsAColdBuildInAnUnfoldedRepository(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(c.s.CommonDir(), "cc-notes")); err != nil {
		t.Fatal(err)
	}
	render := func(entries []RelevantEntry) ([]byte, error) { return json.Marshal(entries) }
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{})
	if !ok {
		t.Fatal("a cold build that had to create the cache directories wrote no entry")
	}
	if entry.Racy || entry.Revalidate {
		t.Fatalf("racy = %t revalidate = %t, want a settled entry", entry.Racy, entry.Revalidate)
	}
}

func TestRelevantCachedIgnoresAGitConfigOverride(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	gittest.Git(t, dir, "config", "cc-notes.noteStaleAfter", "36h")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	override := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(override, []byte("[cc-notes]\n\tnoteStaleAfter = 1h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG", override)

	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		t.Fatalf("NoteStaleAfter under GIT_CONFIG: %v", err)
	}
	if staleAfter != 36*time.Hour {
		t.Fatalf("NoteStaleAfter = %s, want the repository's 36h, not the GIT_CONFIG file's", staleAfter)
	}
	render := func(entries []RelevantEntry) ([]byte, error) { return json.Marshal(entries) }
	got, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render)
	if err != nil {
		t.Fatalf("RelevantCached under GIT_CONFIG: %v", err)
	}
	fresh, err := c.Relevant(ctx, "svc/handler.go", RelevantFilter{})
	if err != nil {
		t.Fatalf("Relevant under GIT_CONFIG: %v", err)
	}
	want, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("cached answer differs from a fresh one\ncached %s\nfresh  %s", got, want)
	}
}
