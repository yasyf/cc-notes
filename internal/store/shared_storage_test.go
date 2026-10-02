package store

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func sharedFixtureOf(t *testing.T, source string) *sharedFixture {
	t.Helper()
	thin := gittest.ShallowClone(t, source, 1)
	_, thinCommon := gittest.Dirs(t, thin)
	return &sharedFixture{
		source:       source,
		sourceCommon: canonical(t, filepath.Join(source, ".git")),
		thin:         thin,
		thinCommon:   thinCommon,
		worktreeA:    gittest.AddWorktree(t, thin, "wt-a"),
		worktreeB:    gittest.AddWorktree(t, thin, "wt-b"),
	}
}

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return s
}

// hasObject reports whether the repository at dir holds sha, by exit code:
// gittest.Git fails the test on a non-zero exit, which here is an answer.
func hasObject(t *testing.T, dir string, sha model.SHA) bool {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	err := exec.Command("git", "-C", dir, "cat-file", "-e", string(sha)).Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("cat-file -e %s in %s: %v", sha, dir, err)
	}
	return err == nil
}

func refTip(t *testing.T, dir, ref string) model.SHA {
	t.Helper()
	return model.SHA(gittest.Git(t, dir, "rev-parse", "--verify", ref))
}

func assertObjectsOnlyInRecords(t *testing.T, f *sharedFixture, shas ...model.SHA) {
	t.Helper()
	for _, sha := range shas {
		if !hasObject(t, f.source, sha) {
			t.Fatalf("records object %s missing from the source", sha)
		}
		if hasObject(t, f.thin, sha) {
			t.Fatalf("records object %s leaked into the thin repository", sha)
		}
	}
}

func pruneGuardValues(t *testing.T, configPath string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range PruneGuardConfigs {
		key, _, _ := strings.Cut(line, "=")
		section, name, _ := strings.Cut(key, ".")
		values, err := gitobj.ConfigValues(configPath, section, name)
		if err != nil {
			t.Fatalf("ConfigValues(%s, %s): %v", configPath, key, err)
		}
		out[key] = values
	}
	return out
}

func assertPruneGuardInRecordsOnly(t *testing.T, f *sharedFixture) {
	t.Helper()
	for key, values := range pruneGuardValues(t, filepath.Join(f.sourceCommon, "config")) {
		if !reflect.DeepEqual(values, []string{"true"}) {
			t.Fatalf("source %s = %q, want exactly [true]", key, values)
		}
	}
	for key, values := range pruneGuardValues(t, f.config()) {
		if len(values) != 0 {
			t.Fatalf("thin config carries %s = %q; the prune guard belongs to the records repository", key, values)
		}
	}
}

func assertLFSInRecords(t *testing.T, f *sharedFixture, s *Store, oid string) {
	t.Helper()
	path := s.LFS().Path(oid)
	if want := filepath.Join(f.sourceCommon, "lfs") + string(filepath.Separator); !strings.HasPrefix(path, want) {
		t.Fatalf("LFS object path = %q, want one under %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("LFS object missing from the source store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.thinCommon, "lfs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thin repository grew an lfs dir: stat %v", err)
	}
}

func TestBoundStoreSharesRecords(t *testing.T) {
	ctx := t.Context()
	source := initSourceRepo(t)
	src := openStore(t, source)
	note := create(t, src, noteOps("seeded note")).(model.Note)
	noteRef := refs.For(model.KindNote, note.ID)
	seedAtt, guarded, err := src.AttachFile(ctx, writeTempFile(t, "seed.bin", []byte("seeded attachment")))
	if err != nil || !guarded {
		t.Fatalf("source AttachFile = %+v, %v, %v; want the guard installed", seedAtt, guarded, err)
	}
	seeded, err := src.Append(ctx, noteRef, []model.Op{model.AddAttachment(seedAtt)})
	if err != nil {
		t.Fatalf("Append attachment: %v", err)
	}
	doc := create(t, src, docOps("seeded doc")).(model.Doc)
	task := create(t, src, taskOps("seeded task", "main")).(model.Task)

	f := sharedFixtureOf(t, source)
	f.bind(t)
	a, b := openStore(t, f.worktreeA), openStore(t, f.worktreeB)

	for name, s := range map[string]*Store{"source": src, "worktree A": a, "worktree B": b} {
		notes, err := s.ListNotes(ctx, false, false)
		if err != nil {
			t.Fatalf("%s ListNotes: %v", name, err)
		}
		if !reflect.DeepEqual(notes, []model.Note{seeded.(model.Note)}) {
			t.Fatalf("%s ListNotes = %+v, want the seeded note with its attachment", name, notes)
		}
		docs, err := s.ListDocs(ctx, false, false)
		if err != nil {
			t.Fatalf("%s ListDocs: %v", name, err)
		}
		if !reflect.DeepEqual(docs, []model.Doc{doc}) {
			t.Fatalf("%s ListDocs = %+v, want %+v", name, docs, doc)
		}
		tasks, err := s.ListTasks(ctx)
		if err != nil {
			t.Fatalf("%s ListTasks: %v", name, err)
		}
		if !reflect.DeepEqual(tasks, []model.Task{task}) {
			t.Fatalf("%s ListTasks = %+v, want %+v", name, tasks, task)
		}
		ref, err := s.Resolve(ctx, model.KindNote, string(note.ID[:10]))
		if err != nil || ref != noteRef {
			t.Fatalf("%s Resolve = %q, %v; want %q", name, ref, err, noteRef)
		}
		has, err := s.HasNotes(ctx)
		if err != nil || !has {
			t.Fatalf("%s HasNotes = %v, %v; want true", name, has, err)
		}
	}

	fromA := create(t, a, noteOps("from worktree A")).(model.Note)
	refA := refs.For(model.KindNote, fromA.ID)
	if got := refTip(t, f.source, refA); got != fromA.Head {
		t.Fatalf("source %s = %s, want worktree A's create %s", refA, got, fromA.Head)
	}
	fresh, err := openStore(t, f.source).Load(ctx, refA)
	if err != nil {
		t.Fatalf("Load from a fresh source store: %v", err)
	}
	viaB, err := b.Load(ctx, refA)
	if err != nil {
		t.Fatalf("Load from worktree B: %v", err)
	}
	if !reflect.DeepEqual(viaB, fresh) || !reflect.DeepEqual(fresh, model.Snapshot(fromA)) {
		t.Fatalf("worktree B sees %+v, the source %+v, worktree A wrote %+v", viaB, fresh, fromA)
	}
	renamed, err := b.Append(ctx, refA, []model.Op{model.SetTitle{Title: "renamed in B"}})
	if err != nil {
		t.Fatalf("Append from worktree B: %v", err)
	}
	again, err := a.Load(ctx, refA)
	if err != nil {
		t.Fatalf("Load from worktree A after B's append: %v", err)
	}
	if !reflect.DeepEqual(again, renamed) {
		t.Fatalf("worktree A reads %+v after B appended %+v", again, renamed)
	}

	attA, guardedA, err := a.AttachFile(ctx, writeTempFile(t, "from-a.bin", []byte("from worktree A")))
	if err != nil {
		t.Fatalf("AttachFile from worktree A: %v", err)
	}
	if guardedA {
		t.Fatal("AttachFile from worktree A reinstalled the prune guard the records config already carries")
	}
	assertLFSInRecords(t, f, a, attA.OID)
	assertPruneGuardInRecordsOnly(t, f)

	assertObjectsOnlyInRecords(t, f, note.Head, seeded.(model.Note).Head, fromA.Head, renamed.(model.Note).Head)
	if _, err := os.Stat(filepath.Join(f.sourceCommon, foldCacheSubdir, string(renamed.(model.Note).Head))); err != nil {
		t.Fatalf("fold cache entry for the renamed tip is not under the source: %v", err)
	}
	f.assertNoRecords(t)
}

func TestBoundStoreAttachGuardsRecordsConfig(t *testing.T) {
	ctx := t.Context()
	f := newSharedFixture(t)
	f.bind(t)
	a, b := openStore(t, f.worktreeA), openStore(t, f.worktreeB)

	att, guarded, err := a.AttachFile(ctx, writeTempFile(t, "first.bin", []byte("first attachment")))
	if err != nil {
		t.Fatalf("AttachFile from worktree A: %v", err)
	}
	if !guarded {
		t.Fatal("first AttachFile through a bound store did not report installing the prune guard")
	}
	assertLFSInRecords(t, f, a, att.OID)
	assertPruneGuardInRecordsOnly(t, f)

	second, guarded, err := b.AttachFile(ctx, writeTempFile(t, "second.bin", []byte("second attachment")))
	if err != nil {
		t.Fatalf("AttachFile from worktree B: %v", err)
	}
	if guarded {
		t.Fatal("second AttachFile reinstalled a guard the records config already carries")
	}
	assertLFSInRecords(t, f, b, second.OID)
	assertPruneGuardInRecordsOnly(t, f)
	f.assertNoRecords(t)
}

func TestBoundStoreCAS(t *testing.T) {
	ctx := t.Context()
	f := newSharedFixture(t)
	f.bind(t)
	a, b := openStore(t, f.worktreeA), openStore(t, f.worktreeB)

	note := create(t, a, noteOps("cas")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	appended, err := b.Append(ctx, ref, []model.Op{model.SetTitle{Title: "appended in B"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := refTip(t, f.source, ref); got != appended.(model.Note).Head {
		t.Fatalf("source %s = %s after Append, want %s", ref, got, appended.(model.Note).Head)
	}
	compacted, err := a.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	base := compacted.(model.Note).Head
	if got := refTip(t, f.source, ref); got != base {
		t.Fatalf("source %s = %s after Compact, want %s", ref, got, base)
	}
	theirs, err := b.PrepareAppendAt(ctx, ref, base, []model.Op{model.SetTitle{Title: "theirs"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	ours, err := a.Append(ctx, ref, []model.Op{model.SetTitle{Title: "ours"}})
	if err != nil {
		t.Fatalf("Append ours: %v", err)
	}
	oursTip := ours.(model.Note).Head
	merged, err := b.Merge(ctx, ref, oursTip, theirs.New)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := refTip(t, f.source, ref); got != merged {
		t.Fatalf("source %s = %s after Merge, want %s", ref, got, merged)
	}

	if _, err := a.Merge(ctx, ref, base, theirs.New); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("Merge from a stale tip = %v, want ErrCASMismatch", err)
	}
	if err := b.RecordsGit.UpdateRefs(ctx, []gitcmd.RefUpdate{theirs.RefUpdate()}); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale prepared publish = %v, want ErrCASMismatch", err)
	}
	if got := refTip(t, f.source, ref); got != merged {
		t.Fatalf("source %s = %s after the contended attempts, want %s untouched", ref, got, merged)
	}
	assertObjectsOnlyInRecords(t, f, note.Head, appended.(model.Note).Head, base, theirs.New, oursTip, merged)
	f.assertNoRecords(t)
}

func TestBoundStoreIgnoresInheritedRouting(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	bare := gittest.InitBare(t)
	gittest.Git(t, f.source, "remote", "add", "origin", bare)

	var kept, doomed model.Note
	t.Run("mutate under routing variables aimed at the thin repository", func(t *testing.T) {
		ctx := t.Context()
		t.Setenv("GIT_DIR", f.thinCommon)
		t.Setenv("GIT_COMMON_DIR", f.thinCommon)
		t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(f.thinCommon, "objects"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(f.thinCommon, "index"))
		s := openStore(t, f.thin)
		kept = create(t, s, noteOps("kept")).(model.Note)
		doomed = create(t, s, noteOps("doomed")).(model.Note)
		doomedRef := refs.For(model.KindNote, doomed.ID)
		if _, err := s.Append(ctx, doomedRef, []model.Op{model.DeleteNote{}}); err != nil {
			t.Fatalf("DeleteNote: %v", err)
		}
		if err := s.RecordsGit.Push(ctx, "origin", doomedRef+":"+doomedRef); err != nil {
			t.Fatalf("push the tombstoned ref: %v", err)
		}
		misrouted, err := gitcmd.Git{Dir: f.source}.Refs(ctx, refs.Namespace)
		if err != nil {
			t.Fatalf("control Refs: %v", err)
		}
		if len(misrouted) != 0 {
			t.Fatalf("control: a checkout handle on the source answered %d refs; GIT_DIR is not steering git at the thin repository", len(misrouted))
		}
		pruned, failed, err := s.PruneTombstones(ctx, "origin")
		if err != nil {
			t.Fatalf("PruneTombstones: %v", err)
		}
		if pruned != 1 || failed != 0 {
			t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
		}
	})

	keptRef := refs.For(model.KindNote, kept.ID)
	if got := gittest.Git(t, f.source, "for-each-ref", "--format=%(refname)", refs.Namespace); got != keptRef {
		t.Fatalf("source refs/cc-notes/ = %q, want exactly %q", got, keptRef)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", refs.Namespace); got != "" {
		t.Fatalf("records remote still holds %q after the prune", got)
	}
	assertObjectsOnlyInRecords(t, f, kept.Head, doomed.Head)
	f.assertNoRecords(t)
}

func TestBoundStoreFailsAfterBackendReplaced(t *testing.T) {
	ctx := t.Context()
	f := newSharedFixture(t)
	f.bind(t)
	s := openStore(t, f.worktreeA)
	note := create(t, s, noteOps("before")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	attachment := writeTempFile(t, "late.bin", []byte("never stored"))
	replaceRepo(t, f.source)

	ops := []struct {
		name string
		run  func() error
	}{
		{"ListNotes", func() error { _, err := s.ListNotes(ctx, false, false); return err }},
		{"Load", func() error { _, err := s.Load(ctx, ref); return err }},
		{"LoadAt warm", func() error { _, err := s.LoadAt(ctx, note.Head); return err }},
		{"LoadRootedAt", func() error { _, err := s.LoadRootedAt(ctx, note.Head); return err }},
		{"History", func() error { _, err := s.History(ctx, ref); return err }},
		{"HasNotes", func() error { _, err := s.HasNotes(ctx); return err }},
		{"HasSession", func() error { _, err := s.HasSession(ctx, ref, "s"); return err }},
		{"Resolve", func() error { _, err := s.Resolve(ctx, model.KindNote, string(note.ID[:8])); return err }},
		{"Create", func() error { _, err := s.Create(ctx, noteOps("after")); return err }},
		{"CreateExact", func() error { _, err := s.CreateExact(ctx, noteOps("after")); return err }},
		{"Append", func() error { _, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "after"}}); return err }},
		{"Compact", func() error { _, err := s.Compact(ctx, ref); return err }},
		{"Merge", func() error { _, err := s.Merge(ctx, ref, note.Head, note.Head); return err }},
		{"PrepareCreateExact", func() error { _, err := s.PrepareCreateExact(ctx, noteOps("after")); return err }},
		{"PrepareAppendAt", func() error {
			_, err := s.PrepareAppendAt(ctx, ref, note.Head, []model.Op{model.SetTitle{Title: "after"}})
			return err
		}},
		{"AttachFile", func() error { _, _, err := s.AttachFile(ctx, attachment); return err }},
		{"ReferencedAttachments", func() error { _, err := s.ReferencedAttachments(ctx); return err }},
		{"GCLocal", func() error { _, err := s.GCLocal(ctx); return err }},
		{"PruneTombstones", func() error { _, _, err := s.PruneTombstones(ctx, "origin"); return err }},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			_ = assertBindingError(t, op.run(), ErrBackendReplaced, f.config(), f.sourceCommon)
		})
	}
	f.assertNoRecords(t)
	if got := gittest.Git(t, f.source, "for-each-ref", refs.Namespace); got != "" {
		t.Fatalf("the replacement repository gained records:\n%s", got)
	}
	for _, sub := range []string{"lfs", foldCacheSubdir} {
		if _, err := os.Stat(filepath.Join(f.sourceCommon, sub)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the replacement repository gained %s: stat %v", sub, err)
		}
	}
}

type gitTrace struct {
	path string
}

func traceGit(t *testing.T) gitTrace {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath git: %v", err)
	}
	trace := filepath.Join(t.TempDir(), "git.log")
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CC_NOTES_GIT_TRACE\"\nexec \"$CC_NOTES_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("CC_NOTES_REAL_GIT", realGit)
	t.Setenv("CC_NOTES_GIT_TRACE", trace)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gitTrace{path: trace}
}

func (g gitTrace) lines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read git trace: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestHasNotesBounded(t *testing.T) {
	rows := []struct {
		name  string
		bound bool
		seed  int
	}{
		{"unbound empty", false, 0},
		{"unbound populated", false, 3},
		{"bound empty", true, 0},
		{"bound populated", true, 3},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			f := newSharedFixture(t)
			if tc.bound {
				f.bind(t)
			}
			src := openStore(t, f.source)
			for i := range tc.seed {
				create(t, src, noteOps(strings.Repeat("x", i+1)))
			}
			dir := f.source
			if tc.bound {
				dir = f.worktreeA
			}
			s := openStore(t, dir)
			trace := traceGit(t)
			has, err := s.HasNotes(ctx)
			if err != nil {
				t.Fatalf("HasNotes: %v", err)
			}
			if has != (tc.seed > 0) {
				t.Fatalf("HasNotes = %v with %d notes", has, tc.seed)
			}
			lines := trace.lines(t)
			if len(lines) != 1 {
				t.Fatalf("HasNotes spawned %d git processes, want 1:\n%s", len(lines), strings.Join(lines, "\n"))
			}
			if !strings.Contains(lines[0], " for-each-ref --count=1 ") || !strings.HasSuffix(lines[0], " "+refs.Namespace) {
				t.Fatalf("HasNotes ran %q, want one for-each-ref --count=1 over %s", lines[0], refs.Namespace)
			}
			if tc.bound && !strings.HasPrefix(lines[0], "-C "+filepath.Dir(f.sourceCommon)+" --git-dir="+f.sourceCommon+" ") {
				t.Fatalf("HasNotes ran %q, want it pinned to the records repository %s from its working tree", lines[0], f.sourceCommon)
			}
		})
	}
}

// TestSharedStorageAttachmentIndexLivesInRecords pins the per-tip attachment
// index beside the fold cache it is keyed with: a bound clone's scan writes it
// under the records common directory and the thin common directory gains none.
func TestSharedStorageAttachmentIndexLivesInRecords(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	s := openStore(t, f.thin)
	note, err := s.Create(t.Context(), noteOps("indexed"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	ref := refs.For(model.KindNote, note.EntityID())
	attach(t, s, ref, "a.bin", oidA, 5)
	referenced(t, s)

	index := filepath.Join(attachIndexSubdir, attachIndexName)
	if _, err := os.Stat(filepath.Join(f.sourceCommon, index)); err != nil {
		t.Fatalf("attachment index missing from the records common directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.thinCommon, index)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("attachment index in the thin common directory: stat = %v, want not exist", err)
	}
	if got, want := s.readAttachIndex().Refs[ref].Tip, refTip(t, f.source, ref); got != want {
		t.Fatalf("indexed tip for %s = %s, want %s", ref, got, want)
	}
	if out := gittest.Git(t, f.thin, "for-each-ref", "--format=%(refname)", "refs/cc-notes/"); out != "" {
		t.Fatalf("thin repository holds records refs:\n%s", out)
	}
}
