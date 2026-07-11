//go:build fuse

// White-box callback-level tests: they drive the cgofuse callbacks
// directly over a real store in a real git repository — no kernel mount,
// so they run in CI where the macOS TCC grant is impossible.
package fusefs

import (
	"bytes"
	"context"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/fusekit"
)

// The production mount always wraps the FS in fusekit's cache-defeat decorator,
// which drives the commit (notesCommit) after BOTH Flush and Fsync and overrides
// the Getattr mtime nanosecond from notesSeed. The bare FS these white-box tests
// drive is unwrapped, so flush/fsync/getattrDefeated reproduce exactly what the
// decorator does — exercising the real production behavior without weakening any
// assertion.

// flush mirrors the decorator's Flush: the FS Flush handler, then the commit.
func flush(f *FS, p string, fh uint64) int {
	if rc := f.Flush(p, fh); rc != 0 {
		return rc
	}
	return f.notesCommit(p, fh)
}

// fsync mirrors the decorator's Fsync: the FS Fsync handler, then the commit.
func fsync(f *FS, p string, fh uint64) int {
	if rc := f.Fsync(p, false, fh); rc != 0 {
		return rc
	}
	return f.notesCommit(p, fh)
}

// getattrDefeated mirrors the decorator's Getattr: the FS Getattr, then the
// per-version mtime-nanosecond override fusekit applies from notesSeed.
func getattrDefeated(f *FS, p string, stat *fuse.Stat_t, fh uint64) int {
	rc := f.Getattr(p, stat, fh)
	if rc == 0 {
		if seed := f.notesSeed(p, stat); seed != "" {
			stat.Mtim.Nsec = fusekit.VersionNsec(seed)
		}
	}
	return rc
}

func newTestFS(t *testing.T) (*FS, *store.Store) {
	t.Helper()
	s, err := store.Open(gittest.InitRepo(t))
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

func createDoc(t *testing.T, s *store.Store, title, body, when string) model.Doc {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: title, Body: body, When: when}})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return snap.(model.Doc)
}

func createLog(t *testing.T, s *store.Store, title string, entries ...string) model.Log {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	log := snap.(model.Log)
	for _, text := range entries {
		next, err := s.Append(t.Context(), refs.Log(log.ID), []model.Op{model.AppendEntry{Text: text}})
		if err != nil {
			t.Fatalf("append entry: %v", err)
		}
		log = next.(model.Log)
	}
	return log
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
	doc := createDoc(t, s, "Alpha Doc", "doc body\n", "editing the parser")
	taskMain := createTask(t, s, "main", "On main")
	taskNested := createTask(t, s, "feature/login", "On nested branch")
	wantTasks := []string{TaskFilename(taskMain), TaskFilename(taskNested)}
	slices.Sort(wantTasks)

	for _, tc := range []struct {
		dir  string
		want []string
	}{
		{"/", []string{"attachments", "docs", "logs", "notes", "projects", "runbooks", "sprints", "tasks"}},
		{"/notes", []string{NoteFilename(note)}},
		{"/docs", []string{DocFilename(doc)}},
		{"/tasks", wantTasks},
	} {
		if got := readNames(t, f, tc.dir); !slices.Equal(got, tc.want) {
			t.Errorf("Readdir(%s) = %v, want %v", tc.dir, got, tc.want)
		}
	}

	var st fuse.Stat_t
	if errc := f.Getattr("/tasks/"+TaskFilename(taskMain), &st, invalidFh); errc != 0 || st.Mode&fuse.S_IFREG == 0 {
		t.Errorf("Getattr(/tasks/%s) = %d mode %o, want file", TaskFilename(taskMain), errc, st.Mode)
	}
	if errc := f.Getattr("/tasks/deadbee.json", &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(/tasks/deadbee.json) = %d, want -ENOENT", errc)
	}
}

func TestGetattrSizeMatchesRead(t *testing.T) {
	f, s := newTestFS(t)
	note := createNote(t, s, "Sized", "some body text\n")
	task := createTask(t, s, "feature/login", "Sized task")

	for _, p := range []string{
		"/notes/" + NoteFilename(note),
		"/tasks/" + TaskFilename(task),
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
	if errc := flush(f, p, fh); errc != 0 {
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
	if errc := flush(f, p, fh); errc != 0 {
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
	if errc := flush(f, p, fh); errc != -fuse.EINVAL {
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
	if errc := fsync(f, p, fh); errc != 0 {
		t.Fatalf("Fsync = %d", errc)
	}
	if errc := flush(f, p, fh); errc != 0 {
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
	if errc := getattrDefeated(f, p, &before, invalidFh); errc != 0 {
		t.Fatalf("Getattr = %d", errc)
	}
	if _, err := s.Append(t.Context(), refs.Note(note.ID), []model.Op{model.SetBody{Body: "v2\n"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	var after fuse.Stat_t
	if errc := getattrDefeated(f, p, &after, invalidFh); errc != 0 {
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
	if errc := fsync(f, p, fh); errc != -fuse.EINVAL {
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
	if errc := flush(f, p, fh); errc != -fuse.EPERM {
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
	if errc := flush(f, tmp, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(tmp, fh)

	target := "/notes/fresh-note.md"
	if errc := f.Rename(tmp, target); errc != 0 {
		t.Fatalf("Rename = %d", errc)
	}

	notes, err := s.ListNotes(t.Context(), false, false)
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

	p := "/tasks/draft.json"
	errc, fh := f.Create(p, fuse.O_WRONLY, 0o644)
	if errc != 0 {
		t.Fatalf("Create = %d", errc)
	}
	doc := []byte(`{"title": "From the mount", "priority": 1}`)
	if n := f.Write(p, doc, 0, fh); n != len(doc) {
		t.Fatalf("Write = %d", n)
	}
	if errc := flush(f, p, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(p, fh)

	tasks, err := s.ListTasks(t.Context())
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
	if errc := flush(f, p, fh); errc != 0 {
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
	if errc := flush(f, p, fh); errc != 0 {
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

func TestDocFlushSetsWhen(t *testing.T) {
	f, s := newTestFS(t)
	doc := createDoc(t, s, "Triggers", "doc body\n", "before editing the parser")
	ref := refs.Doc(doc.ID)
	p := "/docs/" + DocFilename(doc)
	before := mustTip(t, s, ref)

	edited := bytes.Replace(RenderDoc(doc), []byte("before editing the parser"), []byte("after touching the parser"), 1)
	if bytes.Equal(edited, RenderDoc(doc)) {
		t.Fatal("when line not found in render")
	}

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, edited)
	if errc := flush(f, p, fh); errc != 0 {
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
	setWhen, ok := tipOps[0].(model.SetWhen)
	if !ok {
		t.Fatalf("tip op = %T, want SetWhen", tipOps[0])
	}
	if setWhen.When != "after touching the parser" {
		t.Errorf("SetWhen.When = %q, want %q", setWhen.When, "after touching the parser")
	}
	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := folded.(model.Doc).When; got != "after touching the parser" {
		t.Errorf("folded when = %q", got)
	}
}

func TestAtomicSaveCreatesDoc(t *testing.T) {
	f, s := newTestFS(t)
	createDoc(t, s, "Existing", "so /docs lists\n", "")

	tmp := "/docs/.fresh-doc.md.tmp.1234"
	errc, fh := f.Create(tmp, fuse.O_WRONLY, 0o644)
	if errc != 0 {
		t.Fatalf("Create = %d", errc)
	}
	body := []byte("---\ntitle: Fresh Doc\nwhen: touching the fold\ntags: [draft]\n---\nWritten through the mount.\n")
	if n := f.Write(tmp, body, 0, fh); n != len(body) {
		t.Fatalf("Write = %d", n)
	}
	if errc := flush(f, tmp, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(tmp, fh)

	target := "/docs/fresh-doc.md"
	if errc := f.Rename(tmp, target); errc != 0 {
		t.Fatalf("Rename = %d", errc)
	}

	docs, err := s.ListDocs(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	var created model.Doc
	for _, d := range docs {
		if d.Title == "Fresh Doc" {
			created = d
		}
	}
	if created.ID == "" {
		t.Fatal("atomic save did not create the doc")
	}
	if created.Body != "Written through the mount.\n" || created.When != "touching the fold" || !slices.Equal(created.Tags, []string{"draft"}) {
		t.Errorf("created doc = %+v", created)
	}
	var st fuse.Stat_t
	if errc := f.Getattr(target, &st, invalidFh); errc != 0 || st.Size != int64(len(RenderDoc(created))) {
		t.Errorf("Getattr(target) = %d size %d, want render size %d", errc, st.Size, len(RenderDoc(created)))
	}
	if got := readNames(t, f, "/docs"); !slices.Contains(got, DocFilename(created)) {
		t.Errorf("readdir /docs = %v, missing %s", got, DocFilename(created))
	}
}

func TestDeletedDocHidden(t *testing.T) {
	f, s := newTestFS(t)
	doc := createDoc(t, s, "Doomed Doc", "body\n", "")
	if _, err := s.Append(t.Context(), refs.Doc(doc.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if got := readNames(t, f, "/docs"); slices.Contains(got, DocFilename(doc)) {
		t.Errorf("tombstoned doc still listed: %v", got)
	}
	var st fuse.Stat_t
	if errc := f.Getattr("/docs/"+DocFilename(doc), &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(deleted) = %d, want -ENOENT", errc)
	}
}

func TestLogAppendEntryFlush(t *testing.T) {
	f, s := newTestFS(t)
	log := createLog(t, s, "Rollout", "flipped to 5%\n")
	ref := refs.Log(log.ID)
	p := "/logs/" + LogFilename(log)
	before := mustTip(t, s, ref)

	// Append a new fenced entry at EOF, exactly as an editor would: the fence
	// fields on a brand-new entry are ignored — author/ts come from the commit.
	appended := append(slices.Clone(RenderLog(log)),
		[]byte("<!-- cc-notes:entry author=\"ignored\" ts=\"1999-01-01T00:00:00Z\" -->\nrolled back to 0%\n")...)

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, appended)
	if errc := flush(f, p, fh); errc != 0 {
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
	appendEntry, ok := tipOps[0].(model.AppendEntry)
	if !ok {
		t.Fatalf("tip op = %T, want AppendEntry", tipOps[0])
	}
	if want := "rolled back to 0%\n"; appendEntry.Text != want {
		t.Errorf("AppendEntry.Text = %q, want %q", appendEntry.Text, want)
	}
	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := folded.(model.Log).Entries
	if len(entries) != 2 || entries[0].Text != "flipped to 5%\n" || entries[1].Text != "rolled back to 0%\n" {
		t.Errorf("folded entries = %#v", entries)
	}
}

func TestLogMidFileEditEPERM(t *testing.T) {
	f, s := newTestFS(t)
	log := createLog(t, s, "Locked", "first entry\n", "second entry\n")
	ref := refs.Log(log.ID)
	p := "/logs/" + LogFilename(log)
	before := mustTip(t, s, ref)

	canonical := RenderLog(log)
	edited := bytes.Replace(canonical, []byte("first entry\n"), []byte("rewritten entry\n"), 1)
	if bytes.Equal(edited, canonical) {
		t.Fatal("edit did not change the rendered bytes")
	}

	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, p, fh, edited)
	if errc := flush(f, p, fh); errc != -fuse.EPERM {
		t.Errorf("Flush = %d, want -EPERM", errc)
	}

	// The deterministic-rejection path reverts the handle buffer to the last
	// good render so path-based readers never see the rejected bytes.
	h := f.handles[fh]
	if h == nil {
		t.Fatal("handle gone after rejected flush")
	}
	if !bytes.Equal(h.buf, canonical) {
		t.Errorf("handle buffer not reverted to canonical bytes:\n got %q\nwant %q", h.buf, canonical)
	}
	f.Release(p, fh)

	if after := mustTip(t, s, ref); after != before {
		t.Errorf("immutable edit advanced the tip")
	}
}

// TestLogCLIEntriesMountRoundTrip drives the mount path for a 2+ entry log built
// from CLI-stored entry text (no trailing newlines, exactly as `log append`
// stores it). Open/Read must return canonical bytes whose fences are anchored at
// line starts, a flush of the unmodified render must succeed (not -EINVAL from a
// ParseLog failure on a glued-together fence), and appending a new entry on top
// must commit one AppendEntry.
func TestLogCLIEntriesMountRoundTrip(t *testing.T) {
	f, s := newTestFS(t)
	log := createLog(t, s, "Rollout", "flipped to 5%", "rolled back to 0%")
	ref := refs.Log(log.ID)
	p := "/logs/" + LogFilename(log)
	before := mustTip(t, s, ref)

	canonical := RenderLog(log)
	if !bytes.Contains(canonical, []byte("flipped to 5%\n<!-- cc-notes:entry")) {
		t.Fatalf("rendered fence not anchored at line start:\n%s", canonical)
	}
	if _, err := ParseLog(canonical); err != nil {
		t.Fatalf("ParseLog(canonical): %v", err)
	}

	var st fuse.Stat_t
	if errc := f.Getattr(p, &st, invalidFh); errc != 0 {
		t.Fatalf("Getattr(%s) = %d", p, errc)
	}
	errc, fh := f.Open(p, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	buf := make([]byte, st.Size)
	if n := f.Read(p, buf, 0, fh); int64(n) != st.Size {
		t.Fatalf("Read = %d, want %d", n, st.Size)
	}
	if !bytes.Equal(buf, canonical) {
		t.Fatalf("read bytes diverge from canonical render:\n got %q\nwant %q", buf, canonical)
	}

	// Flushing the unmodified render must round-trip cleanly: no diff, no parse
	// failure, no -EINVAL.
	if errc := flush(f, p, fh); errc != 0 {
		t.Fatalf("flush of unmodified render = %d, want 0", errc)
	}
	if after := mustTip(t, s, ref); after != before {
		t.Errorf("no-op flush advanced the tip")
	}

	// Append a third entry at EOF and flush; one AppendEntry lands.
	appended := append(slices.Clone(canonical),
		[]byte("<!-- cc-notes:entry author=\"ignored\" ts=\"1999-01-01T00:00:00Z\" -->\nincident closed\n")...)
	mustWriteAll(t, f, p, fh, appended)
	if errc := flush(f, p, fh); errc != 0 {
		t.Fatalf("flush of appended entry = %d, want 0", errc)
	}
	f.Release(p, fh)

	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries := folded.(model.Log).Entries
	if len(entries) != 3 ||
		entries[0].Text != "flipped to 5%" ||
		entries[1].Text != "rolled back to 0%" ||
		entries[2].Text != "incident closed\n" {
		t.Errorf("folded entries = %#v", entries)
	}
}

func createSprint(t *testing.T, s *store.Store, title string) model.Sprint {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create sprint: %v", err)
	}
	return snap.(model.Sprint)
}

func createProject(t *testing.T, s *store.Store, title string) model.Project {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return snap.(model.Project)
}

func appendOps(t *testing.T, s *store.Store, ref string, ops ...model.Op) {
	t.Helper()
	if _, err := s.Append(t.Context(), ref, ops); err != nil {
		t.Fatalf("append %s: %v", ref, err)
	}
}

// browseFixture builds a project P holding a sprint S, a task T pointed at S,
// and a task D pointed directly at P — the membership shape the nested browse
// tree renders.
func browseFixture(t *testing.T) (f *FS, s *store.Store, p model.Project, sp model.Sprint, taskT, direct model.Task) {
	t.Helper()
	f, s = newTestFS(t)
	p = createProject(t, s, "Proj")
	sp = createSprint(t, s, "Sprint One")
	appendOps(t, s, refs.Sprint(sp.ID), model.SetProject{Project: p.ID})
	taskT = createTask(t, s, "main", "In sprint")
	appendOps(t, s, refs.Task(taskT.ID), model.SetSprint{Sprint: sp.ID})
	direct = createTask(t, s, "main", "Direct in project")
	appendOps(t, s, refs.Task(direct.ID), model.SetProject{Project: p.ID})
	return f, s, p, sp, taskT, direct
}

func TestBrowseTreeReaddir(t *testing.T) {
	f, _, p, sp, taskT, direct := browseFixture(t)
	pShort, sShort := p.ID.Short(), sp.ID.Short()
	tFile, dFile := TaskFilename(taskT), TaskFilename(direct)

	// /projects and /sprints carry both the flat <short>.json file and the
	// <short> browse dir.
	if got := readNames(t, f, "/projects"); !slices.Contains(got, pShort+".json") || !slices.Contains(got, pShort) {
		t.Errorf("readdir /projects = %v, want both %q and %q", got, pShort+".json", pShort)
	}
	if got := readNames(t, f, "/sprints"); !slices.Contains(got, sShort+".json") || !slices.Contains(got, sShort) {
		t.Errorf("readdir /sprints = %v, want both %q and %q", got, sShort+".json", sShort)
	}

	if got := readNames(t, f, "/projects/"+pShort); !slices.Equal(got, []string{"sprints", "tasks"}) {
		t.Errorf("readdir /projects/<p> = %v, want [sprints tasks]", got)
	}
	if got := readNames(t, f, "/projects/"+pShort+"/sprints"); !slices.Contains(got, sShort) {
		t.Errorf("readdir /projects/<p>/sprints = %v, want %q", got, sShort)
	}
	if got := readNames(t, f, "/projects/"+pShort+"/sprints/"+sShort+"/tasks"); !slices.Contains(got, tFile) {
		t.Errorf("readdir project/sprint/tasks = %v, want %q", got, tFile)
	}
	if got := readNames(t, f, "/projects/"+pShort+"/tasks"); !slices.Contains(got, tFile) || !slices.Contains(got, dFile) {
		t.Errorf("readdir /projects/<p>/tasks = %v, want %q and %q", got, tFile, dFile)
	}
	if got := readNames(t, f, "/sprints/"+sShort+"/tasks"); !slices.Contains(got, tFile) {
		t.Errorf("readdir /sprints/<s>/tasks = %v, want %q", got, tFile)
	}
}

func TestBrowseTreeSymlinkStatAndReadlink(t *testing.T) {
	f, _, p, sp, taskT, _ := browseFixture(t)
	tFile := TaskFilename(taskT)
	link := "/projects/" + p.ID.Short() + "/sprints/" + sp.ID.Short() + "/tasks/" + tFile

	var st fuse.Stat_t
	if errc := f.Getattr(link, &st, invalidFh); errc != 0 || st.Mode&fuse.S_IFLNK == 0 {
		t.Fatalf("Getattr(%s) = %d mode %o, want symlink", link, errc, st.Mode)
	}
	errc, target := f.Readlink(link)
	if errc != 0 {
		t.Fatalf("Readlink(%s) = %d", link, errc)
	}
	if want := "../../../../../tasks/" + tFile; target != want {
		t.Errorf("Readlink target = %q, want %q", target, want)
	}
	if got := path.Join(path.Dir(link), target); got != "/tasks/"+tFile {
		t.Errorf("resolved target = %q, want /tasks/%s", got, tFile)
	}
	// The advertised size equals the target length, so the kernel reads it whole.
	if st.Size != int64(len(target)) {
		t.Errorf("symlink size %d != target length %d", st.Size, len(target))
	}
}

// TestBrowseTreeReadThrough is the load-bearing test: following a nested
// symlink to the flat task file and editing there must change the real entity.
func TestBrowseTreeReadThrough(t *testing.T) {
	f, s, p, sp, taskT, _ := browseFixture(t)
	tFile := TaskFilename(taskT)
	link := "/projects/" + p.ID.Short() + "/sprints/" + sp.ID.Short() + "/tasks/" + tFile

	// Follow the symlink to the flat file, exactly as the kernel would.
	errc, target := f.Readlink(link)
	if errc != 0 {
		t.Fatalf("Readlink = %d", errc)
	}
	flat := path.Join(path.Dir(link), target)
	if flat != "/tasks/"+tFile {
		t.Fatalf("resolved flat path = %q, want /tasks/%s", flat, tFile)
	}

	// Edit the title through the flat file the symlink points at.
	cur, err := s.Load(t.Context(), refs.Task(taskT.ID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	edited := cur.(model.Task)
	edited.Title = "Edited Through Symlink"

	errc, fh := f.Open(flat, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open(%s) = %d", flat, errc)
	}
	mustWriteAll(t, f, flat, fh, RenderTask(edited))
	if errc := flush(f, flat, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(flat, fh)

	folded, err := s.Load(t.Context(), refs.Task(taskT.ID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := folded.(model.Task).Title; got != "Edited Through Symlink" {
		t.Errorf("title = %q, want the edit through the symlink to land on the real file", got)
	}
}

func TestBrowseTreeBrokenChain(t *testing.T) {
	f, s, p, sp, taskT, direct := browseFixture(t)
	orphan := createSprint(t, s, "Orphan") // no project membership
	pShort, sShort := p.ID.Short(), sp.ID.Short()
	tFile, dFile := TaskFilename(taskT), TaskFilename(direct)

	var st fuse.Stat_t
	// D is direct in the project, never in sprint S.
	badTask := "/sprints/" + sShort + "/tasks/" + dFile
	if errc := f.Getattr(badTask, &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(%s) = %d, want -ENOENT (task not in sprint)", badTask, errc)
	}
	if errc, _ := f.Readlink(badTask); errc != -fuse.ENOENT {
		t.Errorf("Readlink(%s) = %d, want -ENOENT", badTask, errc)
	}
	// The orphan sprint is not in project P.
	badSprint := "/projects/" + pShort + "/sprints/" + orphan.ID.Short()
	if errc := f.Getattr(badSprint, &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(%s) = %d, want -ENOENT (sprint not in project)", badSprint, errc)
	}
	badLink := "/projects/" + pShort + "/sprints/" + orphan.ID.Short() + "/tasks/" + tFile
	if errc, _ := f.Readlink(badLink); errc != -fuse.ENOENT {
		t.Errorf("Readlink(%s) = %d, want -ENOENT (sprint not in project)", badLink, errc)
	}
}

func TestFlatSprintFileEdit(t *testing.T) {
	f, s, p, sp, _, _ := browseFixture(t)
	ref := refs.Sprint(sp.ID)
	flat := "/sprints/" + SprintFilename(sp)

	// Editable: change the title through the flat file.
	cur, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	edited := cur.(model.Sprint)
	edited.Title = "Renamed Sprint"
	errc, fh := f.Open(flat, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, flat, fh, RenderSprint(edited))
	if errc := flush(f, flat, fh); errc != 0 {
		t.Fatalf("Flush = %d", errc)
	}
	f.Release(flat, fh)

	folded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := folded.(model.Sprint).Title; got != "Renamed Sprint" {
		t.Errorf("title = %q, want the flat-file edit to land", got)
	}

	// Immutable: the project membership pointer changes only via the CLI.
	reloaded := folded.(model.Sprint)
	before := mustTip(t, s, ref)
	doc := bytes.Replace(RenderSprint(reloaded), []byte(string(p.ID)), []byte(strings.Repeat("0", len(p.ID))), 1)
	errc, fh = f.Open(flat, fuse.O_RDWR)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	mustWriteAll(t, f, flat, fh, doc)
	if errc := flush(f, flat, fh); errc != -fuse.EPERM {
		t.Errorf("Flush of project edit = %d, want -EPERM", errc)
	}
	f.Release(flat, fh)
	if after := mustTip(t, s, ref); after != before {
		t.Errorf("immutable project edit advanced the tip")
	}
}
