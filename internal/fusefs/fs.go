//go:build fuse

package fusefs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// invalidFh is cgofuse's "no handle" marker.
const invalidFh = ^uint64(0)

// renderCacheMax caps the render cache; past it the cache is dropped
// wholesale, since a long-lived mount otherwise accretes one entry per
// saved version of every entity.
const renderCacheMax = 1024

// rendered is one cached render of an entity chain tip.
type rendered struct {
	snapshot model.Snapshot
	data     []byte
}

// handle is one open file: an entity edit buffer, a scratch buffer, or a
// read-only attachment. base is the open-time snapshot diffs run against; it
// is nil (and ref empty) for scratch and pending files. An attachment handle
// sets file and size instead of buf: reads go straight through ReadAt on the
// LFS object, never a memory buffer.
type handle struct {
	path    string
	ref     string
	base    model.Snapshot
	buf     []byte
	file    *os.File
	size    int64
	ino     uint64
	mtime   int64
	birth   int64
	dirty   bool
	flushed bool
}

// scratchFile is an in-memory non-entity file (editor tmp and atomic-save
// buffers). It exists from Create until Unlink or a committing Rename and
// vanishes on unmount.
type scratchFile struct {
	data  []byte
	mtime time.Time
}

// FS synthesizes the entity tree over a store: /notes/<short7>-<slug>.md
// and /tasks/<short7>.json, files rendered by render.go and committed back
// through parse + diff.
//
// Locking: one mutex guards all state (handles, render cache, scratch,
// aliases), held across store READS too — callbacks are short and the
// store serializes internally — but released across every store WRITE
// (Append/Create): the data a commit needs is snapshotted first and the
// cache updated after re-locking. Two flushes racing on one entity may
// therefore both commit; the CRDT fold merges them field-wise (LWW),
// which is exactly the concurrent-editor semantic the mount promises.
type FS struct {
	fuse.FileSystemBase

	store *store.Store
	// ctx detaches from the mount context: a flush arriving during
	// teardown still commits instead of failing on a canceled context.
	ctx   context.Context
	uid   uint32
	gid   uint32
	start time.Time

	mu      sync.Mutex
	nextFh  uint64
	handles map[uint64]*handle
	// renders caches folds and rendered bytes keyed by chain tip — a tip
	// uniquely pins one entity at one version, and every lookup re-reads
	// the ref tip first, so external CLI writes appear on the next stat.
	renders map[model.SHA]rendered
	scratch map[string]*scratchFile
	// aliases remembers pending paths committed as entities (the name an
	// editor created, before the canonical <short7>-<slug> name), so the
	// editor's stat-after-save still resolves.
	aliases map[string]string
	// content is the local LFS store /attachments reads serve from,
	// resolved lazily once; contentSet distinguishes it from the zero value.
	content    lfs.Store
	contentSet bool
	// missingLogged rate-limits the missing-attachment holder log per oid.
	missingLogged map[string]time.Time
}

// FusePassthroughOnly reports false: this FS synthesizes a tree (/notes, /tasks)
// over a git store, serving handler-generated content keyed on fuse file handles,
// not real backing files. fuse-t's FSKit backend does not honor fi->fh, so
// fusekit must keep fuse-t's NFS backend. See fusekit.PassthroughOnly. (Inert
// until cc-notes bumps fusekit to a version that reads this marker.)
func (f *FS) FusePassthroughOnly() bool { return false }

func newFS(ctx context.Context, s *store.Store) *FS {
	return &FS{
		store:         s,
		ctx:           context.WithoutCancel(ctx),
		uid:           uint32(os.Getuid()),
		gid:           uint32(os.Getgid()),
		start:         time.Now(),
		handles:       map[uint64]*handle{},
		renders:       map[model.SHA]rendered{},
		scratch:       map[string]*scratchFile{},
		aliases:       map[string]string{},
		missingLogged: map[string]time.Time{},
	}
}

// errno maps an error onto the mount's errno contract: parse failures are
// EINVAL, immutable-field edits EPERM, unresolved paths ENOENT, and
// everything else EIO. Rejected saves and store failures log to stderr —
// the mount is a foreground daemon, so stderr is the operator channel, and
// FUSE-T's NFS transport swallows commit errnos on their way to the editor
// (live-smoke finding), making the log line the one reliable signal.
func errno(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrParse):
		log.Printf("cc-notes mount: rejected save: %v", err)
		return -fuse.EINVAL
	case errors.Is(err, ErrImmutableField):
		log.Printf("cc-notes mount: rejected save: %v", err)
		return -fuse.EPERM
	case errors.Is(err, ErrPath), errors.Is(err, gitobj.ErrRefNotFound):
		return -fuse.ENOENT
	default:
		log.Printf("cc-notes mount: %v", err)
		return -fuse.EIO
	}
}

func (f *FS) Getattr(p string, stat *fuse.Stat_t, fh uint64) int {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handleFor(p, fh, true); h != nil {
		if h.file != nil {
			f.fillStat(stat, fuse.S_IFREG|0o444, h.ino, h.size, fuse.Timespec{Sec: h.mtime}, h.birth)
			return 0
		}
		f.fillStat(stat, fuse.S_IFREG|0o644, h.ino, int64(len(h.buf)), fuse.Timespec{Sec: h.mtime}, h.birth)
		return 0
	}
	return f.statPath(p, stat)
}

func (f *FS) Open(p string, flags int) (int, uint64) {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT, invalidFh
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if af, ok := attachmentNode(p); ok {
		return f.openAttachment(p, af, flags)
	}
	if sc, ok := f.scratch[p]; ok {
		h := &handle{path: p, buf: slices.Clone(sc.data), ino: pathIno(p), mtime: sc.mtime.Unix(), birth: sc.mtime.Unix()}
		truncateOnOpen(h, flags)
		return 0, f.newHandle(h)
	}
	ref, r, errc := f.openEntity(p)
	if errc != 0 {
		return errc, invalidFh
	}
	created, updated := snapshotTimes(r.snapshot)
	h := &handle{
		path: p, ref: ref, base: r.snapshot, buf: slices.Clone(r.data),
		ino: idIno(r.snapshot.EntityID()), mtime: updated, birth: created,
	}
	truncateOnOpen(h, flags)
	return 0, f.newHandle(h)
}

func truncateOnOpen(h *handle, flags int) {
	if flags&fuse.O_TRUNC != 0 && len(h.buf) > 0 {
		h.buf = nil
		h.dirty = true
	}
}

func (f *FS) Create(p string, flags int, mode uint32) (int, uint64) {
	if JunkName(path.Base(p)) {
		return -fuse.EPERM, invalidFh
	}
	if underAttachments(p) {
		return -fuse.EPERM, invalidFh
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.scratch[p] = &scratchFile{mtime: now}
	return 0, f.newHandle(&handle{path: p, ino: pathIno(p), mtime: now.Unix(), birth: now.Unix()})
}

func (f *FS) Read(p string, buff []byte, ofst int64, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var data []byte
	if h := f.handleFor(p, fh, true); h != nil {
		if h.file != nil {
			return readWindow(h.file, buff, ofst)
		}
		data = h.buf
	} else if sc, ok := f.scratch[p]; ok {
		data = sc.data
	} else if af, ok := attachmentNode(p); ok {
		// Stateless windowed read: FUSE-T's NFS layer reads by path after
		// reconnects, and an attachment must never round-trip through a
		// render buffer.
		return f.readAttachmentAt(af, buff, ofst)
	} else {
		_, r, errc := f.openEntity(p)
		if errc != 0 {
			return errc
		}
		data = r.data
	}
	if ofst >= int64(len(data)) {
		return 0
	}
	return copy(buff, data[ofst:])
}

func (f *FS) Write(p string, buff []byte, ofst int64, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handleFor(p, fh, true)
	if h == nil {
		return -fuse.EBADF
	}
	if h.file != nil {
		return -fuse.EACCES
	}
	if end := ofst + int64(len(buff)); end > int64(len(h.buf)) {
		h.buf = resize(h.buf, end)
	}
	copy(h.buf[ofst:], buff)
	h.dirty = true
	h.flushed = false
	h.mtime = time.Now().Unix()
	return len(buff)
}

func (f *FS) Truncate(p string, size int64, fh uint64) int {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT
	}
	if underAttachments(p) {
		return -fuse.EACCES
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handleFor(p, fh, false); h != nil {
		if int64(len(h.buf)) != size {
			h.buf = resize(h.buf, size)
			h.dirty = true
			h.flushed = false
			h.mtime = time.Now().Unix()
		}
		return 0
	}
	if sc, ok := f.scratch[p]; ok {
		if int64(len(sc.data)) != size {
			sc.data = resize(sc.data, size)
			sc.mtime = time.Now()
		}
		return 0
	}
	// An open-less truncate of an entity file is accepted silently (like
	// chmod): editors that pre-truncate before opening must not fail, and
	// the open-write-flush cycle that follows carries the real edit.
	var st fuse.Stat_t
	return f.statPath(p, &st)
}

// Flush marks the handle flushed so the Release backstop won't double-commit;
// the actual commit is the cache-defeat decorator's Commit hook (notesCommit),
// which fusekit runs after BOTH Flush and Fsync. Flush is the editor's close(2)
// boundary, but FUSE-T's NFS client swallows the flush errno — so routing
// notesCommit through Fsync too (the COMMIT the client DOES report) is what
// makes a bad save fail loudly at close.
func (f *FS) Flush(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handles[fh]; h != nil {
		h.flushed = true
	}
	return 0
}

// Release drops the handle, closing an attachment's content file and
// committing as a backstop only when no flush ever ran for this fh; its
// status is kernel-discarded either way.
func (f *FS) Release(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	delete(f.handles, fh)
	if h == nil {
		return 0
	}
	if h.file != nil {
		_ = h.file.Close()
		return 0
	}
	if h.dirty && !h.flushed {
		_ = f.commitHandle(h)
	}
	return 0
}

// Fsync is a no-op handler: the commit it used to drive is now the cache-defeat
// decorator's Commit hook (notesCommit), which fusekit runs after both Fsync
// and Flush. FUSE-T's NFS client issues a COMMIT (fsync) before the close(2)
// flush and reports ITS errno to the writer while swallowing the flush errno,
// so notesCommit firing on Fsync is what makes a bad save fail loudly at close
// (and at an editor's explicit fsync). It does not set flushed, so a transient
// (EIO) commit failure here still leaves the Release backstop armed.
func (f *FS) Fsync(p string, datasync bool, fh uint64) int { return 0 }

// notesCommit commits the handle's buffer — the cache-defeat decorator's Commit
// hook, which fusekit runs after BOTH the Flush and the Fsync handlers. Routing
// the commit through one callback fired on both write boundaries keeps the rich
// semantics (the transient-vs-deterministic error split, the last-good-render
// revert, the failed-create draft-preserve, the path-fallback handle lookup) in
// ONE place — commitHandle — instead of duplicated across Flush and Fsync. It
// returns commitHandle's errno (zero on success, or a clean no-op for an
// already-committed handle).
func (f *FS) notesCommit(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	if h == nil {
		return 0
	}
	return f.commitHandle(h)
}

func (f *FS) Rename(oldpath string, newpath string) int {
	if JunkName(path.Base(oldpath)) {
		return -fuse.ENOENT
	}
	if JunkName(path.Base(newpath)) || underAttachments(newpath) {
		return -fuse.EPERM
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.scratch[oldpath]
	if !ok {
		var st fuse.Stat_t
		if errc := f.statPath(oldpath, &st); errc != 0 {
			return errc
		}
		return -fuse.EPERM // entities and directories never move
	}
	if _, ok := entityTarget(newpath); ok {
		// The atomic save: an editor wrote the full document to a scratch
		// file and renames it onto the entity (or new-entity) name.
		ref, _, errc := f.commitDocument(newpath, sc.data)
		if errc != 0 {
			return errc
		}
		delete(f.scratch, oldpath)
		f.aliases[newpath] = ref
		return 0
	}
	delete(f.scratch, oldpath)
	f.scratch[newpath] = sc
	for _, h := range f.handles {
		if h.path == oldpath {
			h.path = newpath
		}
	}
	return 0
}

func (f *FS) Unlink(p string) int {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.scratch[p]; ok {
		delete(f.scratch, p)
		return 0
	}
	var st fuse.Stat_t
	if errc := f.statPath(p, &st); errc != 0 {
		return errc
	}
	return -fuse.EPERM // entities tombstone via the CLI, never via unlink
}

func (f *FS) Opendir(p string) (int, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, errc := f.listDir(p); errc != 0 {
		return errc, invalidFh
	}
	return 0, 0
}

func (f *FS) Readdir(p string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	names, errc := f.listDir(p)
	if errc != 0 {
		return errc
	}
	// Entries fill with nil stats so the kernel issues a per-name Getattr
	// — the path-based Getattr is where the synthesized sizes live.
	fill(".", nil, 0)
	fill("..", nil, 0)
	for _, name := range names {
		if !fill(name, nil, 0) {
			return 0
		}
	}
	return 0
}

func (f *FS) Releasedir(p string, fh uint64) int { return 0 }

func (f *FS) Mknod(p string, mode uint32, dev uint64) int          { return -fuse.EPERM }
func (f *FS) Mkdir(p string, mode uint32) int                      { return -fuse.EPERM }
func (f *FS) Rmdir(p string) int                                   { return -fuse.EPERM }
func (f *FS) Link(oldpath string, newpath string) int              { return -fuse.EPERM }
func (f *FS) Symlink(target string, newpath string) int            { return -fuse.EPERM }
func (f *FS) Chown(p string, uid uint32, gid uint32) int           { return -fuse.EPERM }
func (f *FS) Chmod(p string, mode uint32) int                      { return 0 }
func (f *FS) Utimens(p string, tmsp []fuse.Timespec) int           { return 0 }
func (f *FS) Setxattr(p, name string, value []byte, flags int) int { return -fuse.EPERM }
func (f *FS) Getxattr(p string, name string) (int, []byte)         { return -fuse.ENOATTR, nil }
func (f *FS) Removexattr(p string, name string) int                { return -fuse.ENOATTR }
func (f *FS) Listxattr(p string, fill func(name string) bool) int  { return 0 }

func (f *FS) Statfs(p string, stat *fuse.Statfs_t) int {
	*stat = fuse.Statfs_t{
		Bsize:   4096,
		Frsize:  4096,
		Blocks:  1 << 24,
		Bfree:   1 << 24,
		Bavail:  1 << 24,
		Files:   1 << 20,
		Ffree:   1 << 20,
		Namemax: 255,
	}
	return 0
}

// --- handle and commit machinery ---

func (f *FS) newHandle(h *handle) uint64 {
	f.nextFh++
	f.handles[f.nextFh] = h
	return f.nextFh
}

// handleFor finds the handle for fh, falling back to the newest handle
// open at p: FUSE-T's NFS layer stats and truncates by path mid-write, and
// serving the stale rendered size there truncates the write in flight
// (cc-pool's getattrSnapshot lesson). mustDirty restricts the fallback to
// dirty handles — Getattr and Read keep external CLI edits visible through
// clean paths — while Truncate must hit the just-opened clean handle that
// a stripped O_TRUNC targets (FUSE-T drops the flag from open and issues a
// path-based SETATTR size=0 instead).
func (f *FS) handleFor(p string, fh uint64, mustDirty bool) *handle {
	if h, ok := f.handles[fh]; ok {
		return h
	}
	var newest *handle
	var newestFh uint64
	for id, h := range f.handles {
		if h.path == p && (h.dirty || !mustDirty) && id >= newestFh {
			newest, newestFh = h, id
		}
	}
	return newest
}

// commitHandle commits h's buffer: entity handles diff against their
// open-time base and append; dirty pending files at entity-shaped paths
// create their entity; everything else writes back to its scratch entry.
// Caller holds f.mu; the store writes run with it released.
func (f *FS) commitHandle(h *handle) int {
	if !h.dirty {
		return 0
	}
	data := slices.Clone(h.buf)
	if h.ref != "" {
		base, ref := h.base, h.ref
		f.mu.Unlock()
		snap, errc := f.appendDiff(ref, base, data)
		f.mu.Lock()
		switch {
		case errc == -fuse.EIO:
			// Transient store failure: keep the buffer dirty so the
			// Release backstop retries the commit.
			return errc
		case errc != 0:
			// Deterministic content failure (parse error, immutable
			// edit): revert to the last good render so the broken bytes
			// don't shadow the entity for path-based readers — the editor
			// holds its own copy of the rejected buffer.
			h.buf = renderDocument(base)
			h.dirty = false
			return errc
		}
		if snap != nil {
			f.cacheRender(snap)
			h.base = snap
			created, updated := snapshotTimes(snap)
			h.birth, h.mtime = created, updated
		}
		h.dirty = false
		return 0
	}
	if _, ok := entityTarget(h.path); ok {
		ref, snap, errc := f.commitDocument(h.path, data)
		if errc != 0 {
			// Preserve the draft: the scratch entry is the only copy of a
			// pending document, so a failed create must not drop it. The
			// next write re-dirties the handle and retries the create.
			if sc := f.scratch[h.path]; sc != nil {
				sc.data, sc.mtime = data, time.Now()
			}
			h.dirty = false
			return errc
		}
		delete(f.scratch, h.path)
		f.aliases[h.path] = ref
		h.ref, h.base = ref, snap
		h.ino = idIno(snap.EntityID())
		h.dirty = false
		return 0
	}
	sc := f.scratch[h.path]
	if sc == nil {
		// Unlinked while open: the data dies with the handle, as it would
		// on any filesystem.
		h.dirty = false
		return 0
	}
	sc.data, sc.mtime = data, time.Now()
	h.dirty = false
	return 0
}

// appendDiff parses data against base's kind, diffs, and appends. A nil
// snapshot with errc 0 means zero ops: identical content commits nothing.
// Runs WITHOUT f.mu held.
func (f *FS) appendDiff(ref string, base model.Snapshot, data []byte) (model.Snapshot, int) {
	ops, err := diffDocument(base, data)
	if err != nil {
		return nil, errno(err)
	}
	if len(ops) == 0 {
		return nil, 0
	}
	snap, err := f.store.Append(f.ctx, ref, ops)
	if err != nil {
		return nil, errno(err)
	}
	return snap, 0
}

func diffDocument(base model.Snapshot, data []byte) ([]model.Op, error) {
	switch b := base.(type) {
	case model.Note:
		parsed, err := ParseNote(data)
		if err != nil {
			return nil, err
		}
		return DiffNote(b, parsed)
	case model.Doc:
		parsed, err := ParseDoc(data)
		if err != nil {
			return nil, err
		}
		return DiffDoc(b, parsed)
	case model.Log:
		parsed, err := ParseLog(data)
		if err != nil {
			return nil, err
		}
		return DiffLog(b, parsed)
	case model.Task:
		parsed, err := ParseTask(data)
		if err != nil {
			return nil, err
		}
		return DiffTask(b, parsed)
	case model.Sprint:
		parsed, err := ParseSprint(data)
		if err != nil {
			return nil, err
		}
		return DiffSprint(b, parsed)
	case model.Project:
		parsed, err := ParseProject(data)
		if err != nil {
			return nil, err
		}
		return DiffProject(b, parsed)
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", base))
	}
}

// commitDocument writes data to the entity p names: an alias or resolvable
// short id appends the diff against the current tip, anything else
// entity-shaped creates a new entity. Caller holds f.mu; the store writes
// run with it released. It returns the entity's ref and folded snapshot.
func (f *FS) commitDocument(p string, data []byte) (string, model.Snapshot, int) {
	kind, ok := entityTarget(p)
	if !ok {
		panic("fusefs: commitDocument on non-entity path " + p)
	}
	ref, r, errc := f.resolveTarget(p, kind)
	switch {
	case errc == 0:
		f.mu.Unlock()
		snap, errc := f.appendDiff(ref, r.snapshot, data)
		f.mu.Lock()
		if errc != 0 {
			return "", nil, errc
		}
		if snap == nil {
			snap = r.snapshot
		} else {
			f.cacheRender(snap)
		}
		return ref, snap, 0
	case errc != -fuse.ENOENT:
		return "", nil, errc
	}
	ops, err := newEntityOps(kind, data)
	if err != nil {
		return "", nil, errno(err)
	}
	f.mu.Unlock()
	snap, _, cerr := f.store.Create(f.ctx, ops)
	f.mu.Lock()
	if cerr != nil {
		return "", nil, errno(cerr)
	}
	f.cacheRender(snap)
	return refFor(snap), snap, 0
}

// resolveTarget resolves a commit target: an alias first, then the short
// id embedded in the filename. -ENOENT means "no such entity yet" — the
// caller falls through to create.
func (f *FS) resolveTarget(p string, kind refs.Kind) (string, rendered, int) {
	if ref, ok := f.aliases[p]; ok {
		tip, err := f.store.Repo.Tip(f.ctx, ref)
		if err != nil {
			delete(f.aliases, p)
			return "", rendered{}, -fuse.ENOENT
		}
		r, err := f.renderTip(tip)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	}
	shortID, ok := ShortIDOf(path.Base(p))
	if !ok {
		return "", rendered{}, -fuse.ENOENT
	}
	ref, r, err := f.resolveEntity(kind, shortID)
	if err != nil {
		return "", rendered{}, errno(err)
	}
	return ref, r, 0
}

// resolveEntity routes a short id to the resolver for its kind.
func (f *FS) resolveEntity(kind refs.Kind, shortID string) (string, rendered, error) {
	switch kind {
	case refs.KindNote:
		return f.resolveNote(shortID)
	case refs.KindDoc:
		return f.resolveDoc(shortID)
	case refs.KindLog:
		return f.resolveLog(shortID)
	case refs.KindTask:
		return f.resolveTask(shortID)
	case refs.KindSprint:
		return f.resolveSprint(shortID)
	case refs.KindProject:
		return f.resolveProject(shortID)
	default:
		panic("fusefs: resolveEntity on unknown kind " + string(kind))
	}
}

func newEntityOps(kind refs.Kind, data []byte) ([]model.Op, error) {
	switch kind {
	case refs.KindNote:
		parsed, err := ParseNote(data)
		if err != nil {
			return nil, err
		}
		return NewNote(parsed)
	case refs.KindDoc:
		parsed, err := ParseDoc(data)
		if err != nil {
			return nil, err
		}
		return NewDoc(parsed)
	case refs.KindLog:
		parsed, err := ParseLog(data)
		if err != nil {
			return nil, err
		}
		return NewLog(parsed)
	case refs.KindTask:
		parsed, err := ParseTask(data)
		if err != nil {
			return nil, err
		}
		return NewTask(parsed, model.Branch(stringValue(parsed.Branch)))
	case refs.KindSprint:
		parsed, err := ParseSprint(data)
		if err != nil {
			return nil, err
		}
		return NewSprint(parsed)
	case refs.KindProject:
		parsed, err := ParseProject(data)
		if err != nil {
			return nil, err
		}
		return NewProject(parsed)
	default:
		panic("fusefs: newEntityOps on unknown kind " + string(kind))
	}
}

// entityTarget classifies p as a committable entity path — a ".md" name
// directly under /notes or /docs, or a ".json" name directly under /tasks,
// /sprints, or /projects. The nested browse-tree leaves live at deeper paths
// and never match. ok is false for everything else; those paths stay in-memory
// scratch files.
func entityTarget(p string) (kind refs.Kind, ok bool) {
	dir, name := path.Dir(p), path.Base(p)
	switch {
	case dir == "/notes" && strings.HasSuffix(name, ".md") && name != ".md":
		return refs.KindNote, true
	case dir == "/docs" && strings.HasSuffix(name, ".md") && name != ".md":
		return refs.KindDoc, true
	case dir == "/logs" && strings.HasSuffix(name, ".md") && name != ".md":
		return refs.KindLog, true
	case dir == "/tasks" && strings.HasSuffix(name, ".json") && name != ".json":
		return refs.KindTask, true
	case dir == "/sprints" && strings.HasSuffix(name, ".json") && name != ".json":
		return refs.KindSprint, true
	case dir == "/projects" && strings.HasSuffix(name, ".json") && name != ".json":
		return refs.KindProject, true
	}
	return "", false
}

func refFor(snap model.Snapshot) string {
	switch s := snap.(type) {
	case model.Note:
		return refs.Note(s.ID)
	case model.Doc:
		return refs.Doc(s.ID)
	case model.Log:
		return refs.Log(s.ID)
	case model.Task:
		return refs.Task(s.ID)
	case model.Sprint:
		return refs.Sprint(s.ID)
	case model.Project:
		return refs.Project(s.ID)
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

// --- resolution and rendering ---

// openEntity resolves p to an entity ref and its render at the current
// tip; directories answer EISDIR.
func (f *FS) openEntity(p string) (string, rendered, int) {
	if ref, ok := f.aliases[p]; ok {
		tip, err := f.store.Repo.Tip(f.ctx, ref)
		if err != nil {
			delete(f.aliases, p)
			return "", rendered{}, -fuse.ENOENT
		}
		r, err := f.renderTip(tip)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	}
	node, err := ParsePath(p)
	if err != nil {
		return "", rendered{}, -fuse.ENOENT
	}
	switch n := node.(type) {
	case NoteFile:
		ref, r, err := f.resolveNote(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	case DocFile:
		ref, r, err := f.resolveDoc(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	case LogFile:
		ref, r, err := f.resolveLog(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	case TaskFile:
		ref, r, err := f.resolveTask(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	case SprintFile:
		ref, r, err := f.resolveSprint(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	case ProjectFile:
		ref, r, err := f.resolveProject(n.ShortID)
		if err != nil {
			return "", rendered{}, errno(err)
		}
		return ref, r, 0
	default:
		return "", rendered{}, -fuse.EISDIR
	}
}

// resolveNote maps a short id to the live note it names. Unknown, deleted,
// and ambiguous short ids all read as ErrPath — the filesystem namespace
// has no way to disambiguate a colliding prefix.
func (f *FS) resolveNote(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.NotesPrefix, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	note, ok := r.snapshot.(model.Note)
	if !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want note", ref, r.snapshot)
	}
	if note.Deleted {
		return "", rendered{}, fmt.Errorf("%w: note %s is deleted", ErrPath, shortID)
	}
	return ref, r, nil
}

// resolveDoc maps a short id to the live doc it names. Unknown, deleted, and
// ambiguous short ids all read as ErrPath — the filesystem namespace has no way
// to disambiguate a colliding prefix.
func (f *FS) resolveDoc(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.DocsRoot, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	doc, ok := r.snapshot.(model.Doc)
	if !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want doc", ref, r.snapshot)
	}
	if doc.Deleted {
		return "", rendered{}, fmt.Errorf("%w: doc %s is deleted", ErrPath, shortID)
	}
	return ref, r, nil
}

// resolveLog maps a short id to the live log it names. Unknown, deleted, and
// ambiguous short ids all read as ErrPath — the filesystem namespace has no way
// to disambiguate a colliding prefix.
func (f *FS) resolveLog(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.LogsRoot, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	log, ok := r.snapshot.(model.Log)
	if !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want log", ref, r.snapshot)
	}
	if log.Deleted {
		return "", rendered{}, fmt.Errorf("%w: log %s is deleted", ErrPath, shortID)
	}
	return ref, r, nil
}

// resolveTask maps a short id to the task it names in the flat task
// namespace.
func (f *FS) resolveTask(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.TasksRoot, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	if _, ok := r.snapshot.(model.Task); !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want task", ref, r.snapshot)
	}
	return ref, r, nil
}

// resolveSprint maps a short id to the sprint it names.
func (f *FS) resolveSprint(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.SprintsRoot, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	if _, ok := r.snapshot.(model.Sprint); !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want sprint", ref, r.snapshot)
	}
	return ref, r, nil
}

// resolveProject maps a short id to the project it names.
func (f *FS) resolveProject(shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.ProjectsRoot, shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	if _, ok := r.snapshot.(model.Project); !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want project", ref, r.snapshot)
	}
	return ref, r, nil
}

func (f *FS) resolveRef(prefix, shortID string) (string, model.SHA, error) {
	tips, err := f.store.Repo.ListPrefix(f.ctx, prefix)
	if err != nil {
		return "", "", err
	}
	var ref string
	var tip model.SHA
	matches := 0
	for name, t := range tips {
		if !refs.DirectChild(prefix, name) {
			continue
		}
		if strings.HasPrefix(strings.TrimPrefix(name, prefix), shortID) {
			matches++
			ref, tip = name, t
		}
	}
	if matches != 1 {
		return "", "", fmt.Errorf("%w: %s%s matches %d refs", ErrPath, prefix, shortID, matches)
	}
	return ref, tip, nil
}

// renderTip folds and renders the chain at tip through the cache. Callers
// always re-read the ref tip first, so the cache can never serve a stale
// version for a current tip.
func (f *FS) renderTip(tip model.SHA) (rendered, error) {
	if r, ok := f.renders[tip]; ok {
		return r, nil
	}
	chain, err := f.store.Repo.ReadChain(f.ctx, tip)
	if err != nil {
		return rendered{}, err
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		return rendered{}, err
	}
	r := rendered{snapshot: snap, data: renderDocument(snap)}
	f.cacheInsert(tip, r)
	return r, nil
}

// cacheRender stores snap's render keyed by its own chain head — for
// snapshots returned by Append/Create, whose head IS the new ref tip.
func (f *FS) cacheRender(snap model.Snapshot) {
	f.cacheInsert(headOf(snap), rendered{snapshot: snap, data: renderDocument(snap)})
}

func (f *FS) cacheInsert(tip model.SHA, r rendered) {
	if len(f.renders) >= renderCacheMax {
		clear(f.renders)
	}
	f.renders[tip] = r
}

func renderDocument(snap model.Snapshot) []byte {
	switch s := snap.(type) {
	case model.Note:
		return RenderNote(s)
	case model.Doc:
		return RenderDoc(s)
	case model.Log:
		return RenderLog(s)
	case model.Task:
		return RenderTask(s)
	case model.Sprint:
		return RenderSprint(s)
	case model.Project:
		return RenderProject(s)
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

func headOf(snap model.Snapshot) model.SHA {
	switch s := snap.(type) {
	case model.Note:
		return s.Head
	case model.Doc:
		return s.Head
	case model.Log:
		return s.Head
	case model.Task:
		return s.Head
	case model.Sprint:
		return s.Head
	case model.Project:
		return s.Head
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

func snapshotTimes(snap model.Snapshot) (created, updated int64) {
	switch s := snap.(type) {
	case model.Note:
		return s.CreatedAt, s.UpdatedAt
	case model.Doc:
		return s.CreatedAt, s.UpdatedAt
	case model.Log:
		return s.CreatedAt, s.UpdatedAt
	case model.Task:
		return s.CreatedAt, s.UpdatedAt
	case model.Sprint:
		return s.CreatedAt, s.UpdatedAt
	case model.Project:
		return s.CreatedAt, s.UpdatedAt
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

// --- tree synthesis ---

// statPath fills stat for a path with no open handle.
func (f *FS) statPath(p string, stat *fuse.Stat_t) int {
	if sc, ok := f.scratch[p]; ok {
		f.fillStat(stat, fuse.S_IFREG|0o644, pathIno(p), int64(len(sc.data)), fuse.Timespec{Sec: sc.mtime.Unix(), Nsec: int64(sc.mtime.Nanosecond())}, sc.mtime.Unix())
		return 0
	}
	if ref, ok := f.aliases[p]; ok {
		tip, err := f.store.Repo.Tip(f.ctx, ref)
		if err != nil {
			delete(f.aliases, p)
			return -fuse.ENOENT
		}
		r, err := f.renderTip(tip)
		if err != nil {
			return errno(err)
		}
		f.fillEntityStat(stat, r)
		return 0
	}
	node, err := ParsePath(p)
	if err != nil {
		return -fuse.ENOENT
	}
	switch n := node.(type) {
	case Root, NotesDir, DocsDir, LogsDir, TasksRoot, SprintsDir, ProjectsDir:
		f.fillDirStat(stat, p)
		return 0
	case NoteFile:
		_, r, rerr := f.resolveNote(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case DocFile:
		_, r, rerr := f.resolveDoc(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case LogFile:
		_, r, rerr := f.resolveLog(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case TaskFile:
		_, r, rerr := f.resolveTask(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case SprintFile:
		_, r, rerr := f.resolveSprint(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case ProjectFile:
		_, r, rerr := f.resolveProject(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case ProjectBrowseDir, ProjectSprintsDir, ProjectSprintDir, ProjectSprintTasksDir, ProjectTasksDir, SprintBrowseDir, SprintTasksDir:
		if errc := f.validateBrowseDir(node); errc != 0 {
			return errc
		}
		f.fillDirStat(stat, p)
		return 0
	case ProjectSprintTaskLink, ProjectTaskLink, SprintTaskLink:
		task, target, errc := f.resolveLink(p, node)
		if errc != 0 {
			return errc
		}
		f.fillSymlinkStat(stat, task, len(target))
		return 0
	case AttachmentsDir:
		f.fillDirStat(stat, p)
		return 0
	case AttachmentEntityDir:
		if _, errc := f.lookupAttachable(n.EntityShort); errc != 0 {
			return errc
		}
		f.fillDirStat(stat, p)
		return 0
	case AttachmentFile:
		ent, att, errc := f.findAttachment(n)
		if errc != 0 {
			return errc
		}
		f.fillAttachmentStat(stat, ent, att)
		return 0
	default:
		panic(fmt.Sprintf("fusefs: unknown node %T", node))
	}
}

// listDir synthesizes dir p's entries: live entities plus in-memory scratch
// files, sorted and deduplicated.
func (f *FS) listDir(p string) ([]string, int) {
	node, err := ParsePath(p)
	if err != nil {
		return nil, -fuse.ENOENT
	}
	names := map[string]bool{}
	switch n := node.(type) {
	case Root:
		names["notes"], names["docs"], names["logs"], names["tasks"], names["sprints"], names["projects"], names["attachments"] = true, true, true, true, true, true, true
	case NotesDir:
		notes, err := f.store.ListNotes(f.ctx, false, false)
		if err != nil {
			return nil, errno(err)
		}
		for _, note := range notes {
			names[NoteFilename(note)] = true
		}
	case DocsDir:
		docs, err := f.store.ListDocs(f.ctx, false, false)
		if err != nil {
			return nil, errno(err)
		}
		for _, doc := range docs {
			names[DocFilename(doc)] = true
		}
	case LogsDir:
		logs, err := f.store.ListLogs(f.ctx, false)
		if err != nil {
			return nil, errno(err)
		}
		for _, l := range logs {
			names[LogFilename(l)] = true
		}
	case TasksRoot:
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, t := range tasks {
			names[TaskFilename(t)] = true
		}
	case SprintsDir:
		sprints, err := f.store.ListSprints(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, s := range sprints {
			names[SprintFilename(s)] = true
			names[s.ID.Short()] = true
		}
	case ProjectsDir:
		projects, err := f.store.ListProjects(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, p := range projects {
			names[ProjectFilename(p)] = true
			names[p.ID.Short()] = true
		}
	case ProjectBrowseDir:
		if _, errc := f.lookupProject(n.ProjShort); errc != 0 {
			return nil, errc
		}
		names["sprints"], names["tasks"] = true, true
	case ProjectSprintsDir:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return nil, errc
		}
		sprints, err := f.store.ListSprints(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, s := range sprints {
			if s.Project == project.ID {
				names[s.ID.Short()] = true
			}
		}
	case ProjectSprintDir:
		if _, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort); errc != 0 {
			return nil, errc
		}
		names["tasks"] = true
	case ProjectSprintTasksDir:
		sprint, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, t := range tasks {
			if t.Sprint == sprint.ID {
				names[TaskFilename(t)] = true
			}
		}
	case ProjectTasksDir:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		sprints, err := f.store.ListSprints(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		inProject := projectTaskSet(tasks, sprints, project.ID)
		for _, t := range tasks {
			if inProject[t.ID] {
				names[TaskFilename(t)] = true
			}
		}
	case SprintBrowseDir:
		if _, errc := f.lookupSprint(n.SprintShort); errc != 0 {
			return nil, errc
		}
		names["tasks"] = true
	case SprintTasksDir:
		sprint, errc := f.lookupSprint(n.SprintShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, t := range tasks {
			if t.Sprint == sprint.ID {
				names[TaskFilename(t)] = true
			}
		}
	case AttachmentsDir:
		attached, errc := f.listAttachables()
		if errc != 0 {
			return nil, errc
		}
		maps.Copy(names, attached)
	case AttachmentEntityDir:
		ent, errc := f.lookupAttachable(n.EntityShort)
		if errc != 0 {
			return nil, errc
		}
		for _, a := range ent.atts {
			names[a.Name] = true
		}
	default:
		return nil, -fuse.ENOTDIR
	}
	for sp := range f.scratch {
		if path.Dir(sp) == p {
			names[path.Base(sp)] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), 0
}

// --- nested browse tree ---

// Readlink resolves a browse-tree task leaf to its relative target under the
// flat /tasks namespace, validating the full membership chain first. A broken
// chain or unknown id reads ENOENT; a non-link path reads EINVAL.
func (f *FS) Readlink(p string) (int, string) {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT, ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	node, err := ParsePath(p)
	if err != nil {
		return -fuse.ENOENT, ""
	}
	switch node.(type) {
	case ProjectSprintTaskLink, ProjectTaskLink, SprintTaskLink:
		_, target, errc := f.resolveLink(p, node)
		if errc != 0 {
			return errc, ""
		}
		return 0, target
	default:
		return -fuse.EINVAL, ""
	}
}

// lookupProject resolves a project browse-dir short id to its snapshot,
// mapping any failure to an errno.
func (f *FS) lookupProject(shortID string) (model.Project, int) {
	_, r, err := f.resolveProject(shortID)
	if err != nil {
		return model.Project{}, errno(err)
	}
	return r.snapshot.(model.Project), 0
}

// lookupSprint resolves a sprint browse-dir short id to its snapshot.
func (f *FS) lookupSprint(shortID string) (model.Sprint, int) {
	_, r, err := f.resolveSprint(shortID)
	if err != nil {
		return model.Sprint{}, errno(err)
	}
	return r.snapshot.(model.Sprint), 0
}

// lookupTask resolves a task leaf short id to its snapshot.
func (f *FS) lookupTask(shortID string) (model.Task, int) {
	_, r, err := f.resolveTask(shortID)
	if err != nil {
		return model.Task{}, errno(err)
	}
	return r.snapshot.(model.Task), 0
}

// lookupSprintInProject resolves the sprint and confirms it belongs to the
// project the browse path names.
func (f *FS) lookupSprintInProject(projShort, sprintShort string) (model.Sprint, int) {
	project, errc := f.lookupProject(projShort)
	if errc != 0 {
		return model.Sprint{}, errc
	}
	sprint, errc := f.lookupSprint(sprintShort)
	if errc != 0 {
		return model.Sprint{}, errc
	}
	if sprint.Project != project.ID {
		return model.Sprint{}, -fuse.ENOENT
	}
	return sprint, 0
}

// validateBrowseDir confirms the project/sprint chain a browse directory names
// exists and is linked as the path claims.
func (f *FS) validateBrowseDir(node Node) int {
	switch n := node.(type) {
	case ProjectBrowseDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectSprintsDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectTasksDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectSprintDir:
		_, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		return errc
	case ProjectSprintTasksDir:
		_, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		return errc
	case SprintBrowseDir:
		_, errc := f.lookupSprint(n.SprintShort)
		return errc
	case SprintTasksDir:
		_, errc := f.lookupSprint(n.SprintShort)
		return errc
	default:
		panic(fmt.Sprintf("fusefs: validateBrowseDir on non-dir node %T", node))
	}
}

// resolveLink validates a browse-tree link's full membership chain, resolves the
// leaf task, and returns that task plus the relative symlink target. A broken
// chain or unknown id maps to ENOENT.
func (f *FS) resolveLink(p string, node Node) (model.Task, string, int) {
	task, errc := f.linkTask(node)
	if errc != 0 {
		return model.Task{}, "", errc
	}
	return task, SymlinkTarget(p, "tasks/"+TaskFilename(task)), 0
}

// linkTask validates the membership chain a browse-tree link encodes and
// returns the leaf task it points at.
func (f *FS) linkTask(node Node) (model.Task, int) {
	switch n := node.(type) {
	case SprintTaskLink:
		sprint, errc := f.lookupSprint(n.SprintShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		if task.Sprint != sprint.ID {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	case ProjectSprintTaskLink:
		sprint, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		if task.Sprint != sprint.ID {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	case ProjectTaskLink:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		member, err := f.taskInProject(task, project.ID)
		if err != nil {
			return model.Task{}, errno(err)
		}
		if !member {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	default:
		panic(fmt.Sprintf("fusefs: linkTask on non-link node %T", node))
	}
}

// taskInProject reports whether task belongs to projectID, mirroring the CLI's
// reverse index: a direct project pointer, or membership through a sprint whose
// project is projectID.
func (f *FS) taskInProject(task model.Task, projectID model.EntityID) (bool, error) {
	if task.Project == projectID {
		return true, nil
	}
	if task.Sprint == "" {
		return false, nil
	}
	sprints, err := f.store.ListSprints(f.ctx)
	if err != nil {
		return false, err
	}
	for _, s := range sprints {
		if s.ID == task.Sprint {
			return s.Project == projectID, nil
		}
	}
	return false, nil
}

// projectTaskSet returns the set of task ids in projectID: tasks pointed
// directly at the project, unioned with tasks whose sprint belongs to it.
func projectTaskSet(tasks []model.Task, sprints []model.Sprint, projectID model.EntityID) map[model.EntityID]bool {
	projectSprints := make(map[model.EntityID]bool)
	for _, s := range sprints {
		if s.Project == projectID {
			projectSprints[s.ID] = true
		}
	}
	set := make(map[model.EntityID]bool)
	for _, t := range tasks {
		if t.Project == projectID || (t.Sprint != "" && projectSprints[t.Sprint]) {
			set[t.ID] = true
		}
	}
	return set
}

// --- stat plumbing ---

func (f *FS) fillEntityStat(stat *fuse.Stat_t, r rendered) {
	created, updated := snapshotTimes(r.snapshot)
	// Nsec is left zero here; the cache-defeat decorator overrides it on
	// Getattr with VersionNsec(notesSeed(path)) so a same-second commit is
	// still a visible mtime change.
	mtime := fuse.Timespec{Sec: updated}
	f.fillStat(stat, fuse.S_IFREG|0o644, idIno(r.snapshot.EntityID()), int64(len(r.data)), mtime, created)
}

// fillSymlinkStat fills a browse-tree leaf as a symlink: S_IFLNK with the
// target length as size — the kernel reads exactly that many bytes from
// Readlink — and the linked task's times, so the leaf ages with its target.
func (f *FS) fillSymlinkStat(stat *fuse.Stat_t, task model.Task, size int) {
	// Nsec left zero; the decorator overrides it via notesSeed on Getattr.
	mtime := fuse.Timespec{Sec: task.UpdatedAt}
	f.fillStat(stat, fuse.S_IFLNK|0o777, idIno(task.ID), int64(size), mtime, task.CreatedAt)
}

func (f *FS) fillDirStat(stat *fuse.Stat_t, p string) {
	*stat = fuse.Stat_t{
		Ino:      pathIno(p),
		Mode:     fuse.S_IFDIR | 0o755,
		Nlink:    2,
		Uid:      f.uid,
		Gid:      f.gid,
		Atim:     fuse.Timespec{Sec: f.start.Unix()},
		Mtim:     fuse.Timespec{Sec: f.start.Unix()},
		Ctim:     fuse.Timespec{Sec: f.start.Unix()},
		Birthtim: fuse.Timespec{Sec: f.start.Unix()},
		Blksize:  4096,
	}
}

// fillStat synthesizes a file stat. st_size MUST equal the bytes a read
// returns — FUSE-T's NFS layer truncates reads past the advertised size.
func (f *FS) fillStat(stat *fuse.Stat_t, mode uint32, ino uint64, size int64, mtime fuse.Timespec, birth int64) {
	*stat = fuse.Stat_t{
		Ino:      ino,
		Mode:     mode,
		Nlink:    1,
		Uid:      f.uid,
		Gid:      f.gid,
		Size:     size,
		Atim:     mtime,
		Mtim:     mtime,
		Ctim:     mtime,
		Birthtim: fuse.Timespec{Sec: birth},
		Blksize:  4096,
		Blocks:   (size + 511) / 512,
	}
}

// notesSeed resolves p to the entity (or browse-tree task) it names and returns
// that entity's CURRENT chain-tip SHA — the per-version seed the cache-defeat
// decorator folds into the Getattr mtime nanoseconds via fusekit.VersionNsec.
// Entity timestamps have second granularity, so a save whose commit lands in
// the same second would otherwise leave the mtime unchanged, and FUSE-T's NFS
// client would keep serving its own written pages over the differing canonical
// render (live-smoke finding). Re-reading the live tip means an external CLI
// commit shows on the next Getattr. Directories, scratch files, pending
// (uncommitted) entities, and unresolved paths have no version and return ""
// (the decorator then leaves the Nsec untouched). It is the path-based
// replacement for the per-handle mtime nanosecond the FS used to cache.
func (f *FS) notesSeed(p string, _ *fuse.Stat_t) string {
	if JunkName(path.Base(p)) {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.scratch[p]; ok {
		return ""
	}
	if ref, ok := f.aliases[p]; ok {
		tip, err := f.store.Repo.Tip(f.ctx, ref)
		if err != nil {
			return ""
		}
		r, err := f.renderTip(tip)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	}
	node, err := ParsePath(p)
	if err != nil {
		return ""
	}
	switch n := node.(type) {
	case NoteFile:
		_, r, err := f.resolveNote(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case DocFile:
		_, r, err := f.resolveDoc(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case LogFile:
		_, r, err := f.resolveLog(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case TaskFile:
		_, r, err := f.resolveTask(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case SprintFile:
		_, r, err := f.resolveSprint(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case ProjectFile:
		_, r, err := f.resolveProject(n.ShortID)
		if err != nil {
			return ""
		}
		return string(headOf(r.snapshot))
	case ProjectSprintTaskLink, ProjectTaskLink, SprintTaskLink:
		task, errc := f.linkTask(node)
		if errc != 0 {
			return ""
		}
		return string(task.Head)
	case AttachmentFile:
		// Content is addressed by oid: replacing an attachment under the
		// same name changes the seed, so cached pages cannot survive.
		_, att, errc := f.findAttachment(n)
		if errc != 0 {
			return ""
		}
		return att.OID
	default:
		return ""
	}
}

// idIno derives a stable inode from the entity id, invariant across slug
// renames; pathIno covers directories and scratch files.
func idIno(id model.EntityID) uint64 { return fnvHash("id:" + string(id)) }

func pathIno(p string) uint64 { return fnvHash("path:" + p) }

func fnvHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func resize(buf []byte, size int64) []byte {
	if size <= int64(len(buf)) {
		return buf[:size]
	}
	return append(buf, make([]byte, size-int64(len(buf)))...)
}
