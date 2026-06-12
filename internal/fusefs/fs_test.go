//go:build fuse

// White-box callback-level tests: they drive the cgofuse callbacks
// directly over a real store in a real git repository — no kernel mount,
// so they run in CI where the macOS TCC grant is impossible.
package fusefs

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
)

// scrubGitEnv clears every git environment knob that could leak host state
// into a test and pins global/system config to /dev/null.
func scrubGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
		"GIT_EDITOR", "EMAIL", "CC_NOTES_ACTOR",
	} {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func newTestFS(t *testing.T) (*FS, *store.Store) {
	t.Helper()
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Test User")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return newFS(context.Background(), s), s
}

func createNote(t *testing.T, s *store.Store, title, body string) model.Note {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title, Body: body}})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	return snap.(model.Note)
}

func createTask(t *testing.T, s *store.Store, branch model.Branch, title string) model.Task {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateTask{
		Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Priority: 2, Branch: branch,
	}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return snap.(model.Task)
}

func mustTip(t *testing.T, s *store.Store, ref string) model.SHA {
	t.Helper()
	tip, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("tip %s: %v", ref, err)
	}
	return tip
}

func readNames(t *testing.T, f *FS, dir string) []string {
	t.Helper()
	var names []string
	errc := f.Readdir(dir, func(name string, _ *fuse.Stat_t, _ int64) bool {
		if name != "." && name != ".." {
			names = append(names, name)
		}
		return true
	}, 0, invalidFh)
	if errc != 0 {
		t.Fatalf("Readdir(%s) = %d", dir, errc)
	}
	return names
}

// mustWriteAll truncates the handle and writes data from offset 0 — the
// callback-level shape of a full rewrite.
func mustWriteAll(t *testing.T, f *FS, p string, fh uint64, data []byte) {
	t.Helper()
	if errc := f.Truncate(p, 0, fh); errc != 0 {
		t.Fatalf("Truncate(%s) = %d", p, errc)
	}
	if n := f.Write(p, data, 0, fh); n != len(data) {
		t.Fatalf("Write(%s) = %d, want %d", p, n, len(data))
	}
}

func TestReaddirTreeSynthesis(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Alpha Note", "body\n")
	taskMain := createTask(t, s, "main", "On main")
	taskNested := createTask(t, s, "feature/login", "On nested branch")

	for _, tc := range []struct {
		dir  string
		want []string
	}{
		{"/", []string{"notes", "tasks"}},
		{"/notes", []string{NoteFilename(note)}},
		{"/tasks", []string{"feature", "main"}},
		{"/tasks/feature", []string{"login"}},
		{"/tasks/feature/login", []string{TaskFilename(taskNested)}},
		{"/tasks/main", []string{TaskFilename(taskMain)}},
	} {
		if got := readNames(t, f, tc.dir); !slices.Equal(got, tc.want) {
			t.Errorf("Readdir(%s) = %v, want %v", tc.dir, got, tc.want)
		}
	}

	var st fuse.Stat_t
	if errc := f.Getattr("/tasks/feature", &st, invalidFh); errc != 0 || st.Mode&fuse.S_IFDIR == 0 {
		t.Errorf("Getattr(/tasks/feature) = %d mode %o, want dir", errc, st.Mode)
	}
	if errc := f.Getattr("/tasks/nope", &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(/tasks/nope) = %d, want -ENOENT", errc)
	}
}

func TestGetattrSizeMatchesRead(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Sized", "some body text\n")
	task := createTask(t, s, "feature/login", "Sized task")

	for _, p := range []string{
		"/notes/" + NoteFilename(note),
		"/tasks/feature/login/" + TaskFilename(task),
	} {
		var st fuse.Stat_t
		if errc := f.Getattr(p, &st, invalidFh); errc != 0 {
			t.Fatalf("Getattr(%s) = %d", p, errc)
		}
		errc, fh := f.Open(p, fuse.O_RDONLY)
		if errc != 0 {
			t.Fatalf("Open(%s) = %d", p, errc)
		}
		buf := make([]byte, st.Size+64)
		n := f.Read(p, buf, 0, fh)
		f.Release(p, fh)
		if int64(n) != st.Size {
			t.Errorf("%s: st_size %d != read length %d", p, st.Size, n)
		}
	}
}

func TestFlushCommitsDiffedOps(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Editable", "first line\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)
	before := mustTip(t, s, ref)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	extra := []byte("appended line\n")
	if n := f.Write(p, extra, int64(len(RenderNote(note))), fh); n != len(extra) {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	after := mustTip(t, s, ref)
	if after == before {
		t.Fatal("tip did not advance")
	}
	chain, err := s.Repo.ReadChain(t.Context(), after)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	var tipOps []model.Op
	for _, c := range chain {
		if c.SHA == after {
			tipOps = c.Pack.Ops
		}
	}
	if len(tipOps) != 1 {
		t.Fatalf("tip commit ops = %v, want exactly one", tipOps)
	}
	setBody, ok := tipOps[0].(model.SetBody)
	if !ok {
		t.Fatalf("tip op = %T, want SetBody", tipOps[0])
	}
	if want := "first line\nappended line\n"; setBody.Body != want {
		t.Errorf("SetBody.Body = %q, want %q", setBody.Body, want)
	}
	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := folded.(model.Note).Body; got != "first line\nappended line\n" {
		t.Errorf("folded body = %q", got)
	}
}

func TestIdenticalRewriteNoCommit(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Stable", "unchanged body\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)
	before := mustTip(t, s, ref)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, RenderNote(note))
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	if after := mustTip(t, s, ref); after != before {
		t.Errorf("identical rewrite advanced the tip %s -> %s", before, after)
	}
}

func TestParseErrorEINVAL(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Fragile", "body\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)
	before := mustTip(t, s, ref)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, []byte("not: [valid frontmatter\n"))
	if errc := f.Flush(p, fh); errc != -fuse.EINVAL {
		t.Errorf("Flush = %d, want -EINVAL", errc)
	}

	// The rejected buffer reverts to the last good render so it cannot
	// shadow the entity for path-based readers.
	var st fuse.Stat_t
	if errc := f.Getattr(p, &st, invalidFh); errc != 0 || st.Size != int64(len(RenderNote(note))) {
		t.Errorf("Getattr after failed flush = %d size %d, want render size %d", errc, st.Size, len(RenderNote(note)))
	}
	f.Release(p, fh)

	if after := mustTip(t, s, ref); after != before {
		t.Errorf("failed parse advanced the tip")
	}
}

// TestStrippedOTruncRewrite pins the FUSE-T NFS save sequence observed
// live: open WITHOUT O_TRUNC, a path-based Truncate(0) with an invalid fh,
// writes, then an fsync (COMMIT) that carries the errno — close-time flush
// errors are swallowed by the NFS client.
func TestStrippedOTruncRewrite(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Rewritten", "old body\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	if errc := f.Truncate(p, 0, invalidFh); errc != 0 {
		t.Fatalf("path-based Truncate = %d", errc)
	}
	edited := note
	edited.Body = "new body\n"
	doc := RenderNote(edited)
	if n := f.Write(p, doc, 0, fh); n != len(doc) {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Fsync(p, false, fh); errc != 0 {
		t.Fatalf("Fsync = %d", errc)
	}
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := folded.(model.Note).Body; got != "new body\n" {
		t.Errorf("body = %q, want full replace (no splice over the old render)", got)
	}
}

// TestMtimeChangesPerVersion pins the NFS cache-invalidation contract: a
// same-second commit must still change the reported mtime (via its
// per-version nanosecond component), or FUSE-T's client keeps serving its
// own written pages over the differing canonical render.
func TestMtimeChangesPerVersion(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Versioned", "v1\n")
	p := "/notes/" + NoteFilename(note)

	var before fuse.Stat_t
	if errc := f.Getattr(p, &before, invalidFh); errc != 0 {
		t.Fatalf("Getattr = %d", errc)
	}
	if _, err := s.Append(t.Context(), refs.Note(note.ID), []model.Op{model.SetBody{Body: "v2\n"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	var after fuse.Stat_t
	if errc := f.Getattr(p, &after, invalidFh); errc != 0 {
		t.Fatalf("Getattr = %d", errc)
	}
	if before.Mtim == after.Mtim {
		t.Errorf("mtime unchanged across versions: %+v", after.Mtim)
	}
}

// TestFsyncSurfacesParseError pins that a bad document fails at fsync —
// the only error channel FUSE-T's NFS client reliably reports.
func TestFsyncSurfacesParseError(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Guarded", "body\n")
	p := "/notes/" + NoteFilename(note)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	if errc := f.Truncate(p, 0, invalidFh); errc != 0 {
		t.Fatalf("Truncate = %d", errc)
	}
	if n := f.Write(p, []byte("garbage: [\n"), 0, fh); n != 11 {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Fsync(p, false, fh); errc != -fuse.EINVAL {
		t.Errorf("Fsync = %d, want -EINVAL", errc)
	}
	f.Release(p, fh)
}

func TestImmutableEditEPERM(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Locked", "body\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)
	before := mustTip(t, s, ref)

	doc := bytes.Replace(RenderNote(note), []byte(string(note.ID)), []byte(strings.Repeat("0", len(note.ID))), 1)
	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, doc)
	if errc := f.Flush(p, fh); errc != -fuse.EPERM {
		t.Errorf("Flush = %d, want -EPERM", errc)
	}
	f.Release(p, fh)

	if after := mustTip(t, s, ref); after != before {
		t.Errorf("immutable edit advanced the tip")
	}
}

func TestAtomicSaveCreatesNote(t *testing.T) {
	f, s := newTestFS(t)
	createNote(t, s, "Existing", "so /notes lists\n")

	tmp := "/notes/.fresh-note.md.tmp.1234"
	errc, fh := f.Create(tmp, fuse.O_WRONLY, 0o644)
	if errc != 0 {
		t.Fatalf("Create = %d", errc)
	}
	doc := []byte("---\ntitle: Fresh Note\ntags: [draft]\n---\nWritten through the mount.\n")
	if n := f.Write(tmp, doc, 0, fh); n != len(doc) {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Flush(tmp, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(tmp, fh)

	target := "/notes/fresh-note.md"
	if errc := f.Rename(tmp, target); errc != 0 {
		t.Fatalf("Rename = %d", errc)
	}

	notes, err := s.ListNotes(t.Context(), false)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	var created model.Note
	for _, n := range notes {
		if n.Title == "Fresh Note" {
			created = n
		}
	}
	if created.ID == "" {
		t.Fatal("atomic save did not create the note")
	}
	if created.Body != "Written through the mount.\n" || !slices.Equal(created.Tags, []string{"draft"}) {
		t.Errorf("created note = %+v", created)
	}

	// The tmp name is gone; the rename target aliases the new entity.
	var st fuse.Stat_t
	if errc := f.Getattr(tmp, &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(tmp) = %d, want -ENOENT", errc)
	}
	if errc := f.Getattr(target, &st, invalidFh); errc != 0 || st.Size != int64(len(RenderNote(created))) {
		t.Errorf("Getattr(target) = %d size %d, want render size %d", errc, st.Size, len(RenderNote(created)))
	}
	if got := readNames(t, f, "/notes"); !slices.Contains(got, NoteFilename(created)) {
		t.Errorf("readdir /notes = %v, missing %s", got, NoteFilename(created))
	}
}

func TestPendingFlushCreatesTask(t *testing.T) {
	f, s := newTestFS(t)
	createTask(t, s, "main", "Existing")

	p := "/tasks/main/draft.json"
	errc, fh := f.Create(p, fuse.O_WRONLY, 0o644)
	if errc != 0 {
		t.Fatalf("Create = %d", errc)
	}
	doc := []byte(`{"title": "From the mount", "priority": 1}`)
	if n := f.Write(p, doc, 0, fh); n != len(doc) {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	tasks, err := s.ListTasks(t.Context(), "main")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var created model.Task
	for _, task := range tasks {
		if task.Title == "From the mount" {
			created = task
		}
	}
	if created.ID == "" {
		t.Fatal("pending flush did not create the task")
	}
	if created.Priority != 1 || created.Status != model.StatusOpen {
		t.Errorf("created task = %+v", created)
	}
}

func TestScratchUnlink(t *testing.T) {
	f, s := newTestFS(t)
	createNote(t, s, "Anchor", "body\n")

	p := "/notes/scratch.txt"
	errc, fh := f.Create(p, fuse.O_WRONLY, 0o644)
	if errc != 0 {
		t.Fatalf("Create = %d", errc)
	}
	if n := f.Write(p, []byte("temp"), 0, fh); n != 4 {
		t.Fatalf("Write = %d", n)
	}
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	var st fuse.Stat_t
	if errc := f.Getattr(p, &st, invalidFh); errc != 0 || st.Size != 4 {
		t.Fatalf("Getattr = %d size %d", errc, st.Size)
	}
	if got := readNames(t, f, "/notes"); !slices.Contains(got, "scratch.txt") {
		t.Errorf("scratch not listed: %v", got)
	}
	if errc := f.Unlink(p); errc != 0 {
		t.Fatalf("Unlink = %d", errc)
	}
	if errc := f.Getattr(p, &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr after unlink = %d, want -ENOENT", errc)
	}
}

func TestEntityImmovableAndJunk(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Pinned", "body\n")
	p := "/notes/" + NoteFilename(note)

	if errc := f.Unlink(p); errc != -fuse.EPERM {
		t.Errorf("Unlink(entity) = %d, want -EPERM", errc)
	}
	if errc := f.Rename(p, "/notes/elsewhere.md"); errc != -fuse.EPERM {
		t.Errorf("Rename(entity) = %d, want -EPERM", errc)
	}
	if errc := f.Mkdir("/notes/sub", 0o755); errc != -fuse.EPERM {
		t.Errorf("Mkdir = %d, want -EPERM", errc)
	}

	var st fuse.Stat_t
	for _, junk := range []string{"/notes/.DS_Store", "/notes/._" + NoteFilename(note), "/.Spotlight-V100"} {
		if errc := f.Getattr(junk, &st, invalidFh); errc != -fuse.ENOENT {
			t.Errorf("Getattr(%s) = %d, want -ENOENT", junk, errc)
		}
		if errc, _ := f.Open(junk, fuse.O_RDONLY); errc != -fuse.ENOENT {
			t.Errorf("Open(%s) = %d, want -ENOENT", junk, errc)
		}
	}
}

func TestExternalAppendMergesWithFlush(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Original Title", "original body\n")
	ref := refs.Note(note.ID)
	p := "/notes/" + NoteFilename(note)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}

	// External CLI edit lands between open and flush.
	if _, err := s.Append(t.Context(), ref, []model.Op{model.SetTitle{Title: "External Title"}}); err != nil {
		t.Fatalf("external append: %v", err)
	}

	edited := note
	edited.Body = "edited body\n"
	mustWriteAll(t, f, p, fh, RenderNote(edited))
	if errc := f.Flush(p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := folded.(model.Note)
	if got.Title != "External Title" {
		t.Errorf("title = %q, want the external edit preserved", got.Title)
	}
	if got.Body != "edited body\n" {
		t.Errorf("body = %q, want the mount edit preserved", got.Body)
	}
}

func TestDeletedNoteHidden(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Doomed", "body\n")
	if _, err := s.Append(t.Context(), refs.Note(note.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if got := readNames(t, f, "/notes"); slices.Contains(got, NoteFilename(note)) {
		t.Errorf("tombstoned note still listed: %v", got)
	}
	var st fuse.Stat_t
	if errc := f.Getattr("/notes/"+NoteFilename(note), &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(deleted) = %d, want -ENOENT", errc)
	}
}
