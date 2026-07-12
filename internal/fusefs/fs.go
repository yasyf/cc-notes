//go:build fuse

package fusefs

import (
	"context"
	"errors"
	"hash/fnv"
	"log"
	"os"
	"sync"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/lfs"
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
	// gen counts buffer mutations; commitHandle clears dirty only if gen is
	// unchanged across its unlocked store append, so a write racing that window
	// keeps the handle dirty instead of being silently cleared (lost).
	gen uint64
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
