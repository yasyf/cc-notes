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
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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

// handle is one open file: an entity edit buffer or a scratch buffer.
// base is the open-time snapshot diffs run against; it is nil (and ref
// empty) for scratch and pending files.
type handle struct {
	path    string
	ref     string
	base    model.Snapshot
	buf     []byte
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
// and /tasks/<branch.../>/<short7>.json, files rendered by render.go and
// committed back through parse + diff.
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
}

func newFS(ctx context.Context, s *store.Store) *FS {
	return &FS{
		store:   s,
		ctx:     context.WithoutCancel(ctx),
		uid:     uint32(os.Getuid()),
		gid:     uint32(os.Getgid()),
		start:   time.Now(),
		handles: map[uint64]*handle{},
		renders: map[model.SHA]rendered{},
		scratch: map[string]*scratchFile{},
		aliases: map[string]string{},
	}
}

// errno maps an error onto the mount's errno contract: parse failures are
// EINVAL, immutable-field edits EPERM, unresolved paths ENOENT, and
// everything else EIO, logged to stderr — the mount is a foreground
// daemon, so stderr is the operator channel.
func errno(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrParse):
		return -fuse.EINVAL
	case errors.Is(err, ErrImmutableField):
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
	if h := f.handleFor(p, fh); h != nil {
		f.fillStat(stat, fuse.S_IFREG|0o644, h.ino, int64(len(h.buf)), h.mtime, h.birth)
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
	h := &handle{path: p, ref: ref, base: r.snapshot, buf: slices.Clone(r.data), ino: idIno(r.snapshot.EntityID()), mtime: updated, birth: created}
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
	if h := f.handleFor(p, fh); h != nil {
		data = h.buf
	} else if sc, ok := f.scratch[p]; ok {
		data = sc.data
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
	h := f.handleFor(p, fh)
	if h == nil {
		return -fuse.EBADF
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handleFor(p, fh); h != nil {
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

// Flush commits the handle's buffer — this, not Release, is the write
// boundary: release errors are discarded by the kernel, flush errors reach
// the editor's close(2).
func (f *FS) Flush(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	if h == nil {
		return 0
	}
	h.flushed = true
	return f.commitHandle(h)
}

// Release drops the handle, committing as a backstop only when no flush
// ever ran for this fh; its status is kernel-discarded either way.
func (f *FS) Release(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	delete(f.handles, fh)
	if h != nil && h.dirty && !h.flushed {
		_ = f.commitHandle(h)
	}
	return 0
}

func (f *FS) Fsync(p string, datasync bool, fh uint64) int { return 0 }

func (f *FS) Rename(oldpath string, newpath string) int {
	if JunkName(path.Base(oldpath)) {
		return -fuse.ENOENT
	}
	if JunkName(path.Base(newpath)) {
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
	if _, _, isEntity := entityTarget(newpath); isEntity {
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

// handleFor finds the handle for fh, falling back to the newest dirty
// handle open at p: FUSE-T's NFS layer stats by path mid-write, and
// serving the stale rendered size there truncates the write in flight
// (cc-pool's getattrSnapshot lesson).
func (f *FS) handleFor(p string, fh uint64) *handle {
	if h, ok := f.handles[fh]; ok {
		return h
	}
	var newest *handle
	var newestFh uint64
	for id, h := range f.handles {
		if h.path == p && h.dirty && id >= newestFh {
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
		if errc != 0 {
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
	if _, _, ok := entityTarget(h.path); ok {
		ref, snap, errc := f.commitDocument(h.path, data)
		if errc != 0 {
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
	case model.Task:
		parsed, err := ParseTask(data)
		if err != nil {
			return nil, err
		}
		return DiffTask(b, parsed)
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", base))
	}
}

// commitDocument writes data to the entity p names: an alias or resolvable
// short id appends the diff against the current tip, anything else
// entity-shaped creates a new entity. Caller holds f.mu; the store writes
// run with it released. It returns the entity's ref and folded snapshot.
func (f *FS) commitDocument(p string, data []byte) (string, model.Snapshot, int) {
	isNote, branch, ok := entityTarget(p)
	if !ok {
		panic("fusefs: commitDocument on non-entity path " + p)
	}
	ref, r, errc := f.resolveTarget(p, isNote, branch)
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
	ops, err := newEntityOps(isNote, branch, data)
	if err != nil {
		return "", nil, errno(err)
	}
	f.mu.Unlock()
	snap, cerr := f.store.Create(f.ctx, ops)
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
func (f *FS) resolveTarget(p string, isNote bool, branch model.Branch) (string, rendered, int) {
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
	var ref string
	var r rendered
	var err error
	if isNote {
		ref, r, err = f.resolveNote(shortID)
	} else {
		ref, r, err = f.resolveTask(branch, shortID)
	}
	if err != nil {
		return "", rendered{}, errno(err)
	}
	return ref, r, 0
}

func newEntityOps(isNote bool, branch model.Branch, data []byte) ([]model.Op, error) {
	if isNote {
		parsed, err := ParseNote(data)
		if err != nil {
			return nil, err
		}
		return NewNote(parsed)
	}
	parsed, err := ParseTask(data)
	if err != nil {
		return nil, err
	}
	return NewTask(parsed, branch)
}

// entityTarget classifies p as a committable entity path — a ".md" name
// directly under /notes, or a ".json" name under a branch directory. ok is
// false for everything else; those paths stay in-memory scratch files.
func entityTarget(p string) (isNote bool, branch model.Branch, ok bool) {
	dir, name := path.Dir(p), path.Base(p)
	switch {
	case dir == "/notes" && strings.HasSuffix(name, ".md") && name != ".md":
		return true, "", true
	case strings.HasPrefix(dir, "/tasks/") && strings.HasSuffix(name, ".json") && name != ".json":
		return false, model.Branch(strings.TrimPrefix(dir, "/tasks/")), true
	}
	return false, "", false
}

func refFor(snap model.Snapshot) string {
	switch s := snap.(type) {
	case model.Note:
		return refs.Note(s.ID)
	case model.Task:
		return refs.Task(s.Branch, s.ID)
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
	case TaskFile:
		ref, r, err := f.resolveTask(n.Branch, n.ShortID)
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

// resolveTask maps a short id within branch's namespace to the live task
// it names. A promoted-away chain — folded branch no longer the ref's —
// reads as ErrPath: it is not live here.
func (f *FS) resolveTask(branch model.Branch, shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.TasksPrefix(branch), shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	task, ok := r.snapshot.(model.Task)
	if !ok {
		return "", rendered{}, fmt.Errorf("ref %s folds as %T, want task", ref, r.snapshot)
	}
	if task.Branch != branch {
		return "", rendered{}, fmt.Errorf("%w: task %s promoted off %s", ErrPath, shortID, branch)
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
	case model.Task:
		return RenderTask(s)
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

func headOf(snap model.Snapshot) model.SHA {
	switch s := snap.(type) {
	case model.Note:
		return s.Head
	case model.Task:
		return s.Head
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

func snapshotTimes(snap model.Snapshot) (created, updated int64) {
	switch s := snap.(type) {
	case model.Note:
		return s.CreatedAt, s.UpdatedAt
	case model.Task:
		return s.CreatedAt, s.UpdatedAt
	default:
		panic(fmt.Sprintf("fusefs: unknown snapshot type %T", snap))
	}
}

// --- tree synthesis ---

// statPath fills stat for a path with no open handle.
func (f *FS) statPath(p string, stat *fuse.Stat_t) int {
	if sc, ok := f.scratch[p]; ok {
		f.fillStat(stat, fuse.S_IFREG|0o644, pathIno(p), int64(len(sc.data)), sc.mtime.Unix(), sc.mtime.Unix())
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
	case Root, NotesDir, TasksRoot:
		f.fillDirStat(stat, p)
		return 0
	case TaskBranchDir:
		return f.statBranchDir(n.Branch, p, stat)
	case NoteFile:
		_, r, rerr := f.resolveNote(n.ShortID)
		if rerr != nil {
			return errno(rerr)
		}
		f.fillEntityStat(stat, r)
		return 0
	case TaskFile:
		_, r, rerr := f.resolveTask(n.Branch, n.ShortID)
		if rerr == nil {
			f.fillEntityStat(stat, r)
			return 0
		}
		if !errors.Is(rerr, ErrPath) {
			return errno(rerr)
		}
		// A ".json" name may itself be a branch path component.
		return f.statBranchDir(model.Branch(strings.TrimPrefix(p, "/tasks/")), p, stat)
	default:
		panic(fmt.Sprintf("fusefs: unknown node %T", node))
	}
}

func (f *FS) statBranchDir(branch model.Branch, p string, stat *fuse.Stat_t) int {
	set, err := f.branchSet()
	if err != nil {
		return errno(err)
	}
	if !branchDirExists(set, branch) {
		return -fuse.ENOENT
	}
	f.fillDirStat(stat, p)
	return 0
}

// listDir synthesizes dir p's entries: live entities and branch dirs plus
// in-memory scratch files, sorted and deduplicated.
func (f *FS) listDir(p string) ([]string, int) {
	node, err := ParsePath(p)
	if err != nil {
		return nil, -fuse.ENOENT
	}
	names := map[string]bool{}
	switch n := node.(type) {
	case Root:
		names["notes"], names["tasks"] = true, true
	case NotesDir:
		notes, err := f.store.ListNotes(f.ctx, false)
		if err != nil {
			return nil, errno(err)
		}
		for _, note := range notes {
			names[NoteFilename(note)] = true
		}
	case TasksRoot:
		set, err := f.branchSet()
		if err != nil {
			return nil, errno(err)
		}
		for _, d := range subdirsOf(set, "") {
			names[d] = true
		}
	case TaskBranchDir:
		errc := f.listBranchDir(n.Branch, names)
		if errc != 0 {
			return nil, errc
		}
	case TaskFile:
		// A ".json" name may itself be a branch path component.
		errc := f.listBranchDir(model.Branch(strings.TrimPrefix(p, "/tasks/")), names)
		if errc != 0 {
			return nil, errc
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

func (f *FS) listBranchDir(branch model.Branch, names map[string]bool) int {
	set, err := f.branchSet()
	if err != nil {
		return errno(err)
	}
	if !branchDirExists(set, branch) {
		return -fuse.ENOENT
	}
	for _, d := range subdirsOf(set, branch) {
		names[d] = true
	}
	if set[branch] {
		tasks, err := f.store.ListTasks(f.ctx, branch)
		if err != nil {
			return errno(err)
		}
		for _, t := range tasks {
			names[TaskFilename(t)] = true
		}
	}
	return 0
}

// branchSet returns every branch owning at least one task ref. Liveness is
// not consulted: a branch whose tasks were all promoted away still shows
// an empty directory until its refs are garbage-collected.
func (f *FS) branchSet() (map[model.Branch]bool, error) {
	tips, err := f.store.Repo.ListPrefix(f.ctx, refs.TasksRoot)
	if err != nil {
		return nil, err
	}
	set := make(map[model.Branch]bool, len(tips))
	for name := range tips {
		parsed, err := refs.Parse(name)
		if err != nil {
			return nil, fmt.Errorf("task ref: %w", err)
		}
		set[parsed.Branch] = true
	}
	return set, nil
}

// branchDirExists reports whether branch names a task branch dir: an exact
// branch or a parent prefix of one (branch feature/login puts a plain
// directory at /tasks/feature).
func branchDirExists(set map[model.Branch]bool, branch model.Branch) bool {
	if set[branch] {
		return true
	}
	for known := range set {
		if strings.HasPrefix(string(known), string(branch)+"/") {
			return true
		}
	}
	return false
}

// subdirsOf returns the next path component under dir for every branch
// nested below it; dir "" means /tasks itself.
func subdirsOf(set map[model.Branch]bool, dir model.Branch) []string {
	prefix := ""
	if dir != "" {
		prefix = string(dir) + "/"
	}
	names := map[string]bool{}
	for b := range set {
		rest, ok := strings.CutPrefix(string(b), prefix)
		if !ok || rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		names[rest] = true
	}
	return slices.Sorted(maps.Keys(names))
}

// --- stat plumbing ---

func (f *FS) fillEntityStat(stat *fuse.Stat_t, r rendered) {
	created, updated := snapshotTimes(r.snapshot)
	f.fillStat(stat, fuse.S_IFREG|0o644, idIno(r.snapshot.EntityID()), int64(len(r.data)), updated, created)
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
func (f *FS) fillStat(stat *fuse.Stat_t, mode uint32, ino uint64, size, mtime, birth int64) {
	*stat = fuse.Stat_t{
		Ino:      ino,
		Mode:     mode,
		Nlink:    1,
		Uid:      f.uid,
		Gid:      f.gid,
		Size:     size,
		Atim:     fuse.Timespec{Sec: mtime},
		Mtim:     fuse.Timespec{Sec: mtime},
		Ctim:     fuse.Timespec{Sec: mtime},
		Birthtim: fuse.Timespec{Sec: birth},
		Blksize:  4096,
		Blocks:   (size + 511) / 512,
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
