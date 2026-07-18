//go:build fuse

package fusefs

import (
	"log"
	"path"
	"slices"
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func (f *FS) Getattr(p string, stat *fuse.Stat_t, fh uint64) int {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getattrLocked(p, stat, fh)
}

// getattrLocked is Getattr's body with f.mu already held, sharing the handle-
// and path-stat resolution with statSeed.
func (f *FS) getattrLocked(p string, stat *fuse.Stat_t, fh uint64) int {
	if h := f.handleFor(p, fh, true); h != nil {
		f.fillHandleStat(stat, h)
		return 0
	}
	_, errc := f.statPath(p, stat)
	return errc
}

// fillHandleStat fills stat from an open handle's in-memory buffer (or an
// attachment's content file).
func (f *FS) fillHandleStat(stat *fuse.Stat_t, h *handle) {
	switch {
	case h.file != nil:
		f.fillStat(stat, fuse.S_IFREG|0o444, h.ino, h.size, fuse.Timespec{Sec: h.mtime}, h.birth)
	case underReadOnly(h.path):
		f.fillStat(stat, fuse.S_IFREG|0o444, h.ino, int64(len(h.buf)), fuse.Timespec{Sec: h.mtime}, h.birth)
	default:
		f.fillStat(stat, fuse.S_IFREG|0o644, h.ino, int64(len(h.buf)), fuse.Timespec{Sec: h.mtime}, h.birth)
	}
}

// handleSeed is a handle's per-version cache-defeat seed: its open-time snapshot
// head, atomic with the buffer fillHandleStat sizes. A scratch, pending, or
// attachment handle has no version and returns "".
func handleSeed(h *handle) string {
	if h.base != nil {
		return string(headOf(h.base))
	}
	return ""
}

// statSeed returns p's stat and its per-version cache-defeat seed from ONE node
// resolution, so contentd's Entry folds a coherent snapshot. Resolving size and
// version separately (Getattr then notesSeed — each re-reads the ref) lets an
// external commit land between and pair a stale size/mtime with the new version
// SHA, defeating the holder's cache-defeat. fh is the open handle (invalidFh for
// a path-wise stat).
func (f *FS) statSeed(p string, fh uint64) (fuse.Stat_t, string, int) {
	var st fuse.Stat_t
	if JunkName(path.Base(p)) {
		return st, "", -fuse.ENOENT
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handleFor(p, fh, true); h != nil {
		f.fillHandleStat(&st, h)
		return st, handleSeed(h), 0
	}
	seed, rc := f.statPath(p, &st)
	return st, seed, rc
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
	if underReadOnly(p) && flags&(fuse.O_WRONLY|fuse.O_RDWR|fuse.O_TRUNC|fuse.O_APPEND) != 0 {
		return -fuse.EACCES, invalidFh
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
		h.gen++
	}
}

func (f *FS) Create(p string, flags int, mode uint32) (int, uint64) {
	if JunkName(path.Base(p)) {
		return -fuse.EPERM, invalidFh
	}
	if underAttachments(p) || underReadOnly(p) {
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
	if h.file != nil || underReadOnly(h.path) {
		return -fuse.EACCES
	}
	if end := ofst + int64(len(buff)); end > int64(len(h.buf)) {
		h.buf = resize(h.buf, end)
	}
	copy(h.buf[ofst:], buff)
	h.dirty = true
	h.flushed = false
	h.gen++
	h.mtime = time.Now().Unix()
	return len(buff)
}

func (f *FS) Truncate(p string, size int64, fh uint64) int {
	if JunkName(path.Base(p)) {
		return -fuse.ENOENT
	}
	if underAttachments(p) || underReadOnly(p) {
		return -fuse.EACCES
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if h := f.handleFor(p, fh, false); h != nil {
		if int64(len(h.buf)) != size {
			h.buf = resize(h.buf, size)
			h.dirty = true
			h.flushed = false
			h.gen++
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
	_, errc := f.statPath(p, &st)
	return errc
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
// committing as a backstop only when no flush ever ran for this fh. It returns
// the backstop commit's errno: the in-process kernel path discards it, but
// contentd's ReleaseHandle surfaces it over the wire. ANY backstop failure —
// transient (EIO) OR a deterministic rejection (EINVAL parse, EPERM immutable) —
// KEEPS the handle and its buffer (the edit's only copy) and returns the errno,
// so a re-issued close retries and no dirty document is ever silently dropped
// while reporting success. Only a clean commit (or a non-dirty handle) drops it.
func (f *FS) Release(p string, fh uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	if h == nil {
		return 0
	}
	if h.file != nil {
		_ = h.file.Close()
		delete(f.handles, fh)
		return 0
	}
	if h.dirty && !h.flushed {
		// Re-commit until the buffer commits clean, draining writes that raced
		// commitHandle's unlocked append (each leaves the handle dirty via a bumped
		// gen). Terminates because RELEASE follows the kernel's last accepted write,
		// so the racing set is the finite in-flight batch and gen stabilizes within
		// that many iterations. Any non-zero errno keeps the handle+buffer.
		for h.dirty {
			if rc := f.commitHandle(h); rc != 0 {
				return rc
			}
		}
	}
	delete(f.handles, fh)
	return 0
}

// discardHandle drops the handle WITHOUT committing its buffer, logging a
// discarded dirty edit. It is the holder-generation-change release path
// (contentd's ReleaseAllHandles): a mid-rewrite buffer at a generation change
// is a partial multi-chunk edit, and committing even a parse-valid prefix would
// make a torn write canonical. Durability lives in the store; the editor
// re-opens against the new generation.
func (f *FS) discardHandle(p string, fh uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.handles[fh]
	if h == nil {
		return
	}
	if h.file != nil {
		_ = h.file.Close()
	}
	if h.dirty && !h.flushed {
		log.Printf("cc-notes contentd: discarding uncommitted edit on generation change: %s (%d bytes)", p, len(h.buf))
	}
	delete(f.handles, fh)
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
	if JunkName(path.Base(newpath)) || underAttachments(newpath) || underReadOnly(newpath) {
		return -fuse.EPERM
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.scratch[oldpath]
	if !ok {
		var st fuse.Stat_t
		if _, errc := f.statPath(oldpath, &st); errc != 0 {
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
	if _, errc := f.statPath(p, &st); errc != 0 {
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
