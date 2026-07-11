//go:build fuse

package fusefs

import (
	"errors"
	"path"
	"slices"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

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
	ops, err := codecOf(base.Meta().Kind).Diff(base, data)
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
	ops, err := codecOf(kind).New(data)
	if err != nil {
		return "", nil, errno(err)
	}
	f.mu.Unlock()
	snap, cerr := f.store.Create(f.ctx, ops)
	f.mu.Lock()
	var dup *store.DuplicateError
	if errors.As(cerr, &dup) {
		snap = dup.Existing
	} else if cerr != nil {
		return "", nil, errno(cerr)
	}
	f.cacheRender(snap)
	return refFor(snap), snap, 0
}

// resolveTarget resolves a commit target: an alias first, then the short
// id embedded in the filename. -ENOENT means "no such entity yet" — the
// caller falls through to create.
func (f *FS) resolveTarget(p string, kind model.Kind) (string, rendered, int) {
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
