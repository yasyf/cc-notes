//go:build fuse

package fusefs

import (
	"fmt"
	"path"
	"strings"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// resolveEntity resolves a short id to the live entity of kind it names.
// Unknown, deleted, and ambiguous short ids all read as ErrPath — the
// filesystem namespace has no way to disambiguate a colliding prefix.
func (f *FS) resolveEntity(kind model.Kind, shortID string) (string, rendered, error) {
	ref, tip, err := f.resolveRef(refs.Root(kind), shortID)
	if err != nil {
		return "", rendered{}, err
	}
	r, err := f.renderTip(tip)
	if err != nil {
		return "", rendered{}, err
	}
	if got := r.snapshot.Meta().Kind; got != kind {
		return "", rendered{}, fmt.Errorf("ref %s folds as %s, want %s", ref, got, kind)
	}
	if r.snapshot.Meta().Deleted {
		return "", rendered{}, fmt.Errorf("%w: %s %s is deleted", ErrPath, kind, shortID)
	}
	return ref, r, nil
}

// entityTarget classifies p as a committable entity path — a ".md" name
// directly under /notes or /docs, or a ".json" name directly under /tasks,
// /sprints, or /projects. The nested browse-tree leaves live at deeper paths
// and never match. ok is false for everything else; those paths stay in-memory
// scratch files.
func entityTarget(p string) (kind model.Kind, ok bool) {
	dir, name := path.Dir(p), path.Base(p)
	for k, layout := range layouts {
		if layout.dir != dir || codecOf(k).ReadOnly() {
			continue
		}
		if strings.HasSuffix(name, layout.ext) && name != layout.ext {
			return k, true
		}
		return "", false
	}
	return "", false
}

// underRunbooks reports whether p lies in the read-only /runbooks subtree,
// where every create, write, truncate, and write-intent open is rejected.
func underRunbooks(p string) bool {
	return p == "/runbooks" || strings.HasPrefix(p, "/runbooks/")
}

func refFor(snap model.Snapshot) string {
	return refs.For(snap.Meta().Kind, snap.EntityID())
}

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
	case EntityFile:
		return f.openResolved(n.Kind, n.ShortID)
	default:
		return "", rendered{}, -fuse.EISDIR
	}
}

// openResolved resolves a flat entity file to its ref and render, translating
// a resolution error into the mount's errno contract.
func (f *FS) openResolved(kind model.Kind, shortID string) (string, rendered, int) {
	ref, r, err := f.resolveEntity(kind, shortID)
	if err != nil {
		return "", rendered{}, errno(err)
	}
	return ref, r, 0
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
	return codecOf(snap.Meta().Kind).Render(snap)
}

func headOf(snap model.Snapshot) model.SHA {
	return snap.Meta().Head
}

func snapshotTimes(snap model.Snapshot) (created, updated int64) {
	m := snap.Meta()
	return m.CreatedAt.Unix(), m.UpdatedAt.Unix()
}
