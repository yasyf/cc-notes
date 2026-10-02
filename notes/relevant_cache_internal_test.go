package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func relevantEntryOf(ctx context.Context, t *testing.T, c *Client, target string, filter RelevantFilter) (relevantCacheEntry, bool) {
	t.Helper()
	_, data, ok, err := relevantCacheBytes(ctx, c, target, filter, "json")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return relevantCacheEntry{}, false
	}
	entry, ok := parseRelevantCacheEntry(data)
	if !ok {
		t.Fatalf("undecodable relevance cache entry %q", data)
	}
	return entry, true
}

func TestRelevantCacheEntryRoundTrip(t *testing.T) {
	stamps := []fileStamp{{Path: "/repo/.git/refs/heads", ModTime: 2, Inode: 3}}
	for _, tc := range []struct {
		name  string
		entry relevantCacheEntry
	}{
		{"newlines in the header and the output", relevantCacheEntry{
			Key:    "key\nwith a newline",
			Built:  1,
			Stamps: stamps,
			Deps:   relevantDeps{Branches: []string{"refs/heads/main", "refs/heads/work"}, Commits: []string{"v1.0"}, Config: []string{"/home/me/.gitconfig"}},
			Output: []byte("note one\nnote two\n"),
		}},
		{"empty output", relevantCacheEntry{Key: "k", Built: 1, Stamps: stamps, Output: []byte{}}},
		{"output that is itself JSON", relevantCacheEntry{Key: "k", Built: 1, Deps: relevantDeps{Branches: []string{"refs/heads/main"}}, Output: []byte(`{"branches":["refs/heads/other"]}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.entry.encode()
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := parseRelevantCacheEntry(data); !ok || !reflect.DeepEqual(got, tc.entry) {
				t.Fatalf("round trip = %+v, %t; want %+v\nencoded %q", got, ok, tc.entry, data)
			}
		})
	}

	valid, err := relevantCacheEntry{Key: "k", Output: []byte("out\n"), Deps: relevantDeps{Branches: []string{"refs/heads/main"}}}.encode()
	if err != nil {
		t.Fatal(err)
	}
	header := bytes.IndexByte(valid, '\n') + 1
	output := header + len("out\n")
	withOutputLen := func(n int64) []byte {
		reheaded := bytes.Replace(valid[:header], []byte(`"output_len":4}`), fmt.Appendf(nil, `"output_len":%d}`, n), 1)
		if bytes.Equal(reheaded, valid[:header]) {
			t.Fatalf("header %q carries no output_len to rewrite", valid[:header])
		}
		return slices.Concat(reheaded, valid[header:])
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated output", valid[:output-1]},
		{"header without its newline", valid[:header-1]},
		{"header not JSON", slices.Concat([]byte("not json\n"), valid[header:])},
		{"deps missing", valid[:output]},
		{"deps not JSON", slices.Concat(valid[:output], []byte("not json"))},
		{"empty", nil},
		{"output_len negative", withOutputLen(-1)},
		{"output_len one past the end of the file", withOutputLen(int64(len(valid)-header) + 1)},
		{"output_len far larger than the file", withOutputLen(1 << 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := parseRelevantCacheEntry(tc.data); ok {
				t.Fatalf("parseRelevantCacheEntry(%q) = %+v, want undecodable", tc.data, got)
			}
		})
	}
}

func TestRelevantCachedRacyEntryWaitsOutTheWindow(t *testing.T) {
	const window = 6 * time.Second
	defer SetRelevantRacyWindow(window)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	notesDir := lstampOf(filepath.Join(c.s.CommonDir(), "refs", "cc-notes", "notes"))
	written := time.Unix(0, max(notesDir.ModTime, notesDir.Ctime))
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
	time.Sleep(time.Until(written.Add(window + 500*time.Millisecond)))
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
	gittest.Git(t, dir, "config", "include.path", "inc/extra.inc")
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if err := os.Mkdir(filepath.Join(c.s.CommonDir(), "inc"), 0o750); err != nil {
		t.Fatal(err)
	}
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
	t.Setenv("RELEVANT_GUARD_TOUCH", filepath.Join(c.s.CommonDir(), "inc", "extra.inc"))
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

func TestRelevantCachedPersistsASettledCaptureInAQuietRepository(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	anchors := []model.Anchor{{Kind: model.AnchorPath, Value: "svc/handler.go"}, {Kind: model.AnchorCommit, Value: "ORIG_HEAD"}}
	if _, err := s.Create(ctx, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "handler", Anchors: anchors}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	gitDir := c.s.GitDir()
	rootFiles := []string{"packed-refs", "shallow", "ORIG_HEAD"}
	for _, name := range rootFiles {
		if _, err := os.Lstat(filepath.Join(gitDir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s is present in the git dir: %v", name, err)
		}
	}

	in, err := c.relevantInputs(ctx, "svc/handler.go", RelevantFilter{}, "json", relevantDeps{})
	if err != nil {
		t.Fatalf("relevantInputs: %v", err)
	}
	if !guards(in, realPath(t, gitDir)) {
		t.Fatalf("missing root files left the git dir unguarded during capture: %+v", in.guards)
	}

	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{})
	if !ok {
		t.Fatal("a quiet capture guarding the git dir persisted no entry")
	}
	if entry.Racy || entry.Revalidate {
		t.Fatalf("racy = %t revalidate = %t, want a settled entry", entry.Racy, entry.Revalidate)
	}
	missing := make(map[string]bool)
	for _, stamp := range entry.Stamps {
		if stamp.Missing && filepath.Dir(stamp.Path) == gitDir {
			missing[filepath.Base(stamp.Path)] = true
		}
	}
	for _, name := range rootFiles {
		if !missing[name] {
			t.Fatalf("the entry carries no missing stamp for %s: %+v", name, entry.Stamps)
		}
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil || renders != 1 {
		t.Fatalf("warm call: err %v renders %d, want the settled entry served", err, renders)
	}
}

func TestRelevantCachedGuardsTheRealDirectoryBehindALinkedInclude(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "config", "include.path", "inc/extra.config")
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	real := realPath(t, t.TempDir())
	link := filepath.Join(c.s.CommonDir(), "inc")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	in, err := c.relevantInputs(ctx, "svc/handler.go", RelevantFilter{}, "json", relevantDeps{})
	if err != nil {
		t.Fatalf("relevantInputs: %v", err)
	}
	if in.revalidate {
		t.Fatal("an include below a directory link that resolves forced revalidation")
	}
	if !guards(in, real) || guards(in, link) {
		t.Fatalf("guards %+v; want the real directory %s and never the link %s", in.guards, real, link)
	}

	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{})
	if !ok {
		t.Fatal("a quiet capture reaching its include through a directory link persisted no entry")
	}
	if entry.Racy || entry.Revalidate {
		t.Fatalf("racy = %t revalidate = %t, want a settled entry", entry.Racy, entry.Revalidate)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil || renders != 1 {
		t.Fatalf("warm call: err %v renders %d, want the settled entry served", err, renders)
	}
}

func TestRelevantCachedGuardsTheDirectoryHoldingATraversedLink(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "config", "include.path", "inc/link/extra.config")
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	real := realPath(t, t.TempDir())
	inc := filepath.Join(c.s.CommonDir(), "inc")
	if err := os.Mkdir(inc, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(inc, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	in, err := c.relevantInputs(ctx, "svc/handler.go", RelevantFilter{}, "json", relevantDeps{})
	if err != nil {
		t.Fatalf("relevantInputs: %v", err)
	}
	if in.revalidate {
		t.Fatal("an include below a directory link that resolves forced revalidation")
	}
	if holder := realPath(t, inc); !guards(in, holder) || !guards(in, real) || guards(in, link) {
		t.Fatalf("guards %+v; want the directory holding the link %s and the real target %s, never the link %s", in.guards, holder, real, link)
	}

	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	entry, ok := relevantEntryOf(ctx, t, c, "svc/handler.go", RelevantFilter{})
	if !ok {
		t.Fatal("a quiet capture reaching its include through a nested directory link persisted no entry")
	}
	if entry.Racy || entry.Revalidate {
		t.Fatalf("racy = %t revalidate = %t, want a settled entry", entry.Racy, entry.Revalidate)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil || renders != 1 {
		t.Fatalf("warm call: err %v renders %d, want the settled entry served", err, renders)
	}
}

func TestRelevantInputsDistrustAStampTheWalkContradicts(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	cases := []struct {
		name    string
		present bool
	}{
		{name: "the base appears between its stamp and the walk", present: false},
		{name: "the base vanishes between its stamp and the walk", present: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newWBClient(t)
			ctx := t.Context()
			gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
			root := model.SHA(strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD")))
			gittest.Git(t, dir, "pack-refs", "--all")
			gitDir := c.s.GitDir()
			if err := os.WriteFile(filepath.Join(gitDir, "shallow"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			orig := filepath.Join(gitDir, "ORIG_HEAD")
			set := map[bool]func(){
				true:  func() { gittest.Git(t, dir, "update-ref", "ORIG_HEAD", string(root)) },
				false: func() { gittest.Git(t, dir, "update-ref", "-d", "ORIG_HEAD") },
			}
			if tc.present {
				set[true]()
			}
			in, err := c.relevantInputs(ctx, "svc/handler.go", RelevantFilter{}, "json", relevantDeps{})
			if err != nil {
				t.Fatalf("relevantInputs: %v", err)
			}
			real := realPath(t, gitDir)
			if guards(in, real) {
				t.Fatalf("fixture: the git dir is guarded before ORIG_HEAD is stamped: %+v", in.guards)
			}
			s := lstampOf(orig)
			if s.Missing == tc.present {
				t.Fatalf("stamp missing = %t, want %t", s.Missing, !tc.present)
			}
			set[!tc.present]()
			in.keep(s)
			if !guards(in, real) {
				t.Fatalf("a stamp the walk contradicted left the git dir unguarded: %+v", in.guards)
			}
			if !in.untrusted {
				t.Fatal("a stamp the walk contradicted left the capture trusted")
			}
			resolved, err := c.s.Git.ResolveRevs(ctx, []string{"ORIG_HEAD^{commit}"})
			if err != nil {
				t.Fatalf("ResolveRevs: %v", err)
			}
			var want model.SHA
			if !tc.present {
				want = root
			}
			if got := resolved["ORIG_HEAD^{commit}"]; got != want {
				t.Fatalf("the capture's read resolved ORIG_HEAD to %q, want %q", got, want)
			}
			set[tc.present]()
			if in.close() {
				t.Fatal("close accepted a capture whose base changed between its stamp and its walk and changed back before close")
			}
			if !in.racy() {
				t.Fatal("racy accepted a capture whose base changed between its stamp and its walk")
			}
		})
	}
}

func guards(in *relevantInputs, path string) bool {
	return slices.ContainsFunc(in.guards, func(g fileStamp) bool { return g.Path == path })
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
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

func TestRelevantCachedDetachedHeadKeysOnTheResolvedBranch(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	emptyCommit := func(message string) string {
		t.Helper()
		gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", message)
		return gittest.Git(t, dir, "rev-parse", "HEAD")
	}
	root := emptyCommit("root")
	gittest.Git(t, dir, "checkout", "-q", "-b", "feat")
	feat := emptyCommit("feat")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/main", root)
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/other", root)
	gittest.Git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gittest.Git(t, dir, "checkout", "-q", "--detach", "feat")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}, Branches: []string{"feat"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	logPath := filepath.Join(wrapper, "git.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RELEVANT_DETACHED_LOG\"\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("RELEVANT_DETACHED_LOG", logPath)
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))

	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	step := func(name, want string) {
		t.Helper()
		if err := os.WriteFile(logPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before := renders
		got, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render)
		if err != nil {
			t.Fatalf("%s: RelevantCached: %v", name, err)
		}
		log, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		calls := strings.Count(string(log), "\n")
		tier := "tier2"
		switch {
		case renders > before:
			tier = "tier3"
		case calls == 0:
			tier = "tier1"
		}
		if tier != want {
			t.Fatalf("%s: %s (git calls %d, rendered %t), want %s\n%s", name, tier, calls, renders > before, want, log)
		}
		fresh, err := c.Relevant(ctx, "svc/handler.go", RelevantFilter{})
		if err != nil {
			t.Fatalf("%s: Relevant: %v", name, err)
		}
		wantOut, err := json.Marshal(fresh)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(wantOut) {
			t.Fatalf("%s: cached answer differs from a fresh one\ncached %s\nfresh  %s", name, got, wantOut)
		}
	}

	step("cold", "tier3")
	step("warm", "tier1")
	gittest.Git(t, dir, "update-ref", "refs/heads/unrelated", root)
	step("an unrelated bookmark moved while detached", "tier2")
	step("warm after the unrelated bookmark", "tier1")
	gittest.Git(t, dir, "update-ref", "refs/remotes/origin/other", feat)
	step("a fetch that moved only a non-trunk remote branch", "tier1")
	gittest.Git(t, dir, "update-ref", "refs/heads/main", feat)
	step("trunk moved onto the bookmark", "tier3")
	step("warm after the trunk move", "tier1")
	gittest.Git(t, dir, "update-ref", "refs/heads/main", root)
	step("trunk moved back", "tier3")
	gittest.Git(t, dir, "update-ref", "refs/heads/feat2", feat)
	step("a second bookmark made the nearest one ambiguous", "tier3")
	step("warm after the ambiguity", "tier1")
}

func TestRelevantCachedFollowsWorktreeQualifiedBases(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	cases := []struct {
		name       string
		base       string
		fromLinked bool
		linkedAt   string
		setup      func(t *testing.T, main, linked, a string)
		move       func(t *testing.T, main, linked, b string)
	}{
		{"linked HEAD", "worktrees/linked/HEAD", false, "a", nil, func(t *testing.T, _, linked, b string) {
			gittest.Git(t, linked, "switch", "-q", "--detach", b)
		}},
		{"main HEAD", "main-worktree/HEAD", true, "b", nil, func(t *testing.T, main, _, _ string) {
			gittest.Git(t, main, "switch", "-q", "--detach", "HEAD~1")
		}},
		{"linked worktree ref", "worktrees/linked/refs/worktree/base", false, "b", func(t *testing.T, _, linked, a string) {
			gittest.Git(t, linked, "update-ref", "refs/worktree/base", a)
		}, func(t *testing.T, _, linked, b string) {
			gittest.Git(t, linked, "update-ref", "refs/worktree/base", b)
		}},
		{"main worktree ref", "main-worktree/refs/worktree/base", true, "b", func(t *testing.T, main, _, a string) {
			gittest.Git(t, main, "update-ref", "refs/worktree/base", a)
		}, func(t *testing.T, main, _, b string) {
			gittest.Git(t, main, "update-ref", "refs/worktree/base", b)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, main := newWBClient(t)
			ctx := t.Context()
			write := func(content string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(main, "svc"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(main, "svc", "handler.go"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				gittest.Git(t, main, "add", "-A")
			}
			write("v1\n")
			gittest.Git(t, main, "commit", "-q", "-m", "a")
			a := gittest.Git(t, main, "rev-parse", "HEAD")
			write("v2\n")
			gittest.Git(t, main, "-c", "user.name=Other", "-c", "user.email=other@example.com", "commit", "-q", "-m", "b")
			b := gittest.Git(t, main, "rev-parse", "HEAD")
			linked := filepath.Join(t.TempDir(), "linked")
			at := map[string]string{"a": a, "b": b}[tc.linkedAt]
			gittest.Git(t, main, "worktree", "add", "-q", "--detach", linked, at)
			if tc.setup != nil {
				tc.setup(t, main, linked, a)
			}
			if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			client := c
			if tc.fromLinked {
				var err error
				if client, err = Open(linked); err != nil {
					t.Fatalf("Open(%s): %v", linked, err)
				}
			}
			filter := RelevantFilter{Base: tc.base}
			renders := 0
			render := func(entries []RelevantEntry) ([]byte, error) {
				renders++
				return json.Marshal(entries)
			}
			call := func(step string, recompute bool) []byte {
				t.Helper()
				before := renders
				got, err := client.RelevantCached(ctx, "svc/handler.go", filter, "json", render)
				if err != nil {
					t.Fatalf("%s: RelevantCached: %v", step, err)
				}
				if recomputed := renders > before; recomputed != recompute {
					t.Fatalf("%s: recomputed = %t, want %t", step, recomputed, recompute)
				}
				fresh, err := client.Relevant(ctx, "svc/handler.go", filter)
				if err != nil {
					t.Fatalf("%s: Relevant: %v", step, err)
				}
				want, err := json.Marshal(fresh)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Fatalf("%s: cached answer differs from a fresh one\ncached %s\nfresh  %s", step, got, want)
				}
				return got
			}
			before := call("cold", true)
			call("warm", false)
			tc.move(t, main, linked, b)
			after := call("base moved in the other worktree", true)
			if string(before) == string(after) {
				t.Fatalf("moving the base changed nothing; the fixture carries no base-dependent signal: %s", after)
			}
		})
	}
}

func TestRelevantCachedUseConfigOnlyWithoutIdentityErrors(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	gittest.Git(t, dir, "config", "user.useConfigOnly", "true")
	gittest.Git(t, dir, "config", "--unset", "user.email")
	gittest.Git(t, dir, "config", "--unset", "user.name")
	render := func(entries []RelevantEntry) ([]byte, error) { return json.Marshal(entries) }
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err == nil {
		t.Fatal("RelevantCached succeeded without an author identity under user.useConfigOnly; want git's identity error")
	}
}

func TestRelevantCachedTreatsAnInvalidBranchAnchorAsMissing(t *testing.T) {
	defer SetRelevantRacyWindow(0)()
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(ctx, NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}, Branches: []string{"bad\nname"}}}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	renders := 0
	render := func(entries []RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	got, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render)
	if err != nil {
		t.Fatalf("RelevantCached: %v", err)
	}
	fresh, err := c.Relevant(ctx, "svc/handler.go", RelevantFilter{})
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}
	want, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("cached answer differs from a fresh one\ncached %s\nfresh  %s", got, want)
	}
	if _, err := c.RelevantCached(ctx, "svc/handler.go", RelevantFilter{}, "json", render); err != nil || renders != 1 {
		t.Fatalf("warm call: err %v renders %d, want a served entry", err, renders)
	}
}

func TestConfigTrue(t *testing.T) {
	cases := []struct {
		value    string
		hasValue bool
		want     bool
	}{
		{"", false, true},
		{"", true, false},
		{"true", true, true},
		{"Yes", true, true},
		{"on", true, true},
		{"1", true, true},
		{"-7", true, true},
		{"0", true, false},
		{"false", true, false},
		{"off", true, false},
		{"maybe", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := configTrue(gitcmd.ConfigEntry{Value: tc.value, HasValue: tc.hasValue}); got != tc.want {
				t.Fatalf("configTrue(%q, %t) = %t, want %t", tc.value, tc.hasValue, got, tc.want)
			}
		})
	}
}
