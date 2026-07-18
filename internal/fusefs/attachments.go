//go:build fuse

package fusefs

import (
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/model"
)

// missingLogInterval rate-limits the missing-object holder log: one line per
// oid per interval, however hard a reader polls a not-yet-synced file.
const missingLogInterval = time.Minute

// attachable is the folded entity behind /attachments/<short>: its identity,
// timestamps, and live attachment set.
type attachable struct {
	id      model.EntityID
	created int64
	updated int64
	atts    []model.Attachment
}

// underAttachments reports whether p lies in the read-only /attachments
// subtree, where every create, write, and truncate is rejected.
func underAttachments(p string) bool {
	return p == "/attachments" || strings.HasPrefix(p, "/attachments/")
}

// attachmentNode reports whether p names an /attachments/<short>/<name>
// file.
func attachmentNode(p string) (AttachmentFile, bool) {
	node, err := ParsePath(p)
	if err != nil {
		return AttachmentFile{}, false
	}
	af, ok := node.(AttachmentFile)
	return af, ok
}

// lookupAttachable resolves an /attachments short id across the note, doc,
// log, and investigation namespaces. Unknown, deleted, ambiguous, and
// attachment-less entities all read ENOENT — only entities with live
// attachments appear in the tree. A genuine not-found (ENOENT) keeps looking
// across namespaces; a store or git failure surfaces via errno as EIO, never a
// false ENOENT that would mask an internal error as a missing file.
func (f *FS) lookupAttachable(shortID string) (attachable, int) {
	var matches []attachable
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindLog, model.KindInvestigation} {
		_, r, err := f.resolveEntity(kind, shortID)
		if err != nil {
			if ec := errno(err); ec != -fuse.ENOENT {
				return attachable{}, ec
			}
			continue
		}
		created, updated := snapshotTimes(r.snapshot)
		matches = append(matches, attachable{
			id:      r.snapshot.EntityID(),
			created: created,
			updated: updated,
			atts:    snapshotAttachments(r.snapshot),
		})
	}
	if len(matches) != 1 || len(matches[0].atts) == 0 {
		return attachable{}, -fuse.ENOENT
	}
	return matches[0], 0
}

// snapshotAttachments returns the live attachment set of an attachable
// snapshot; lookupAttachable only yields those kinds.
func snapshotAttachments(snap model.Snapshot) []model.Attachment {
	switch s := snap.(type) {
	case model.Note:
		return s.Attachments
	case model.Doc:
		return s.Attachments
	case model.Log:
		return s.Attachments
	case model.Investigation:
		return s.Attachments
	default:
		return nil
	}
}

// findAttachment resolves /attachments/<short>/<name> to its owning entity
// and the named attachment.
func (f *FS) findAttachment(node AttachmentFile) (attachable, model.Attachment, int) {
	ent, errc := f.lookupAttachable(node.EntityShort)
	if errc != 0 {
		return attachable{}, model.Attachment{}, errc
	}
	for _, a := range ent.atts {
		if a.Name == node.Name {
			return ent, a, 0
		}
	}
	return attachable{}, model.Attachment{}, -fuse.ENOENT
}

// lfsStore lazily resolves the repository's local LFS content store — the
// git common dir is fixed for the life of the mount, so one resolution
// serves every attachment. Caller holds f.mu.
func (f *FS) lfsStore() (lfs.Store, error) {
	if !f.contentSet {
		content, err := f.store.LFS(f.ctx)
		if err != nil {
			return lfs.Store{}, err
		}
		f.content, f.contentSet = content, true
	}
	return f.content, nil
}

// openAttachment opens one attachment read-only as a per-handle *os.File:
// reads go through ReadAt on the content file, never the render cache or a
// handle buffer, so a multi-gigabyte attachment costs one window at a time.
// Caller holds f.mu.
func (f *FS) openAttachment(p string, node AttachmentFile, flags int) (int, uint64) {
	if flags&(fuse.O_WRONLY|fuse.O_RDWR|fuse.O_TRUNC|fuse.O_APPEND) != 0 {
		return -fuse.EACCES, invalidFh
	}
	ent, att, errc := f.findAttachment(node)
	if errc != 0 {
		return errc, invalidFh
	}
	file, errc := f.openContent(ent, att)
	if errc != 0 {
		return errc, invalidFh
	}
	h := &handle{
		path: p, file: file, size: att.Size,
		ino: attachmentIno(ent.id, att.Name), mtime: ent.updated, birth: ent.created,
	}
	return 0, f.newHandle(h)
}

// openContent opens att's bytes in the local LFS store. A missing object is
// EIO plus one rate-limited holder-log line naming the sync remediation:
// the reference is real, the bytes just have not been synced here yet.
// Caller holds f.mu.
func (f *FS) openContent(ent attachable, att model.Attachment) (*os.File, int) {
	content, err := f.lfsStore()
	if err != nil {
		return nil, errno(err)
	}
	file, err := content.Open(att.OID)
	if errors.Is(err, lfs.ErrObjectMissing) {
		if time.Since(f.missingLogged[att.OID]) >= missingLogInterval {
			f.missingLogged[att.OID] = time.Now()
			log.Printf("cc-notes mount: attachment %s/%s: object %s missing locally; run `cc-notes sync`",
				ent.id.Short(), att.Name, att.OID)
		}
		return nil, -fuse.EIO
	}
	if err != nil {
		return nil, errno(err)
	}
	return file, 0
}

// readAttachmentAt serves one stateless windowed read — the path-based
// fallback for reads arriving without a live handle, which FUSE-T's NFS
// layer issues after reconnects.
func (f *FS) readAttachmentAt(node AttachmentFile, buff []byte, ofst int64) int {
	ent, att, errc := f.findAttachment(node)
	if errc != 0 {
		return errc
	}
	file, errc := f.openContent(ent, att)
	if errc != 0 {
		return errc
	}
	defer file.Close()
	return readWindow(file, buff, ofst)
}

// readWindow fills buff from file at ofst, mapping EOF to the short (or
// zero) count the FUSE read contract expects.
func readWindow(file *os.File, buff []byte, ofst int64) int {
	n, err := file.ReadAt(buff, ofst)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Printf("cc-notes mount: read %s: %v", file.Name(), err)
		return -fuse.EIO
	}
	return n
}

// listAttachables returns the short ids of every live entity carrying at
// least one attachment. Superseded notes and docs still list: their content
// stays referenced until the reference is removed.
func (f *FS) listAttachables() (map[string]bool, int) {
	names := map[string]bool{}
	notes, err := f.store.ListNotes(f.ctx, false, true)
	if err != nil {
		return nil, errno(err)
	}
	for _, n := range notes {
		if len(n.Attachments) > 0 {
			names[n.ID.Short()] = true
		}
	}
	docs, err := f.store.ListDocs(f.ctx, false, true)
	if err != nil {
		return nil, errno(err)
	}
	for _, d := range docs {
		if len(d.Attachments) > 0 {
			names[d.ID.Short()] = true
		}
	}
	logs, err := f.store.ListLogs(f.ctx, false)
	if err != nil {
		return nil, errno(err)
	}
	for _, l := range logs {
		if len(l.Attachments) > 0 {
			names[l.ID.Short()] = true
		}
	}
	investigations, err := f.store.ListInvestigations(f.ctx)
	if err != nil {
		return nil, errno(err)
	}
	for _, investigation := range investigations {
		if len(investigation.Attachments) > 0 {
			names[investigation.ID.Short()] = true
		}
	}
	return names, 0
}

// fillAttachmentStat fills stat for one attachment file: a read-only
// regular file sized from the reference — st_size MUST equal the readable
// bytes — aging with its owning entity.
func (f *FS) fillAttachmentStat(stat *fuse.Stat_t, ent attachable, att model.Attachment) {
	f.fillStat(stat, fuse.S_IFREG|0o444, attachmentIno(ent.id, att.Name), att.Size, fuse.Timespec{Sec: ent.updated}, ent.created)
}

// attachmentIno derives a stable inode per (entity, attachment name).
func attachmentIno(id model.EntityID, name string) uint64 {
	return fnvHash("att:" + string(id) + "/" + name)
}
