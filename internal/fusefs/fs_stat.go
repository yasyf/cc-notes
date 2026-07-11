//go:build fuse

package fusefs

import (
	"fmt"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/model"
)

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
	case Root, KindDir:
		f.fillDirStat(stat, p)
		return 0
	case EntityFile:
		return f.statEntity(stat, n.Kind, n.ShortID)
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

// statEntity resolves a flat entity file and fills stat, using the read-only
// mode for read-only kinds (runbooks).
func (f *FS) statEntity(stat *fuse.Stat_t, kind model.Kind, shortID string) int {
	_, r, err := f.resolveEntity(kind, shortID)
	if err != nil {
		return errno(err)
	}
	if codecOf(kind).ReadOnly() {
		f.fillReadonlyEntityStat(stat, r)
	} else {
		f.fillEntityStat(stat, r)
	}
	return 0
}

func (f *FS) fillEntityStat(stat *fuse.Stat_t, r rendered) {
	created, updated := snapshotTimes(r.snapshot)
	// Nsec is left zero here; the cache-defeat decorator overrides it on
	// Getattr with VersionNsec(notesSeed(path)) so a same-second commit is
	// still a visible mtime change.
	mtime := fuse.Timespec{Sec: updated}
	f.fillStat(stat, fuse.S_IFREG|0o644, idIno(r.snapshot.EntityID()), int64(len(r.data)), mtime, created)
}

// fillReadonlyEntityStat mirrors fillEntityStat for the read-only /runbooks
// subtree: mode 0o444 so an editor opens the file read-only.
func (f *FS) fillReadonlyEntityStat(stat *fuse.Stat_t, r rendered) {
	created, updated := snapshotTimes(r.snapshot)
	mtime := fuse.Timespec{Sec: updated}
	f.fillStat(stat, fuse.S_IFREG|0o444, idIno(r.snapshot.EntityID()), int64(len(r.data)), mtime, created)
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
