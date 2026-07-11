//go:build fuse

package fusefs

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
