//go:build fuse

package fusefs

import (
	"path"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/model"
)

// entitySeed returns the current chain-tip head of the flat entity file kind
// and shortID name, or "" when it does not resolve.
func (f *FS) entitySeed(kind model.Kind, shortID string) string {
	_, r, err := f.resolveEntity(kind, shortID)
	if err != nil {
		return ""
	}
	return string(headOf(r.snapshot))
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
	case EntityFile:
		return f.entitySeed(n.Kind, n.ShortID)
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
