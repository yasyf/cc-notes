package viz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// attIndex is a referenced-attachment lookup: byOID maps each LFS object id the
// repository's live entity state references to its store.ReferencedObject, and
// digest is the entity-ref tip digest the index was built at.
type attIndex struct {
	digest string
	byOID  map[string]store.ReferencedObject
}

// ReferencedAttachment returns the referenced LFS object with the given oid and
// whether the repository's live entity state references it. The referenced set
// is memoized over the entity ref tips and rebuilt when any of them moves.
func (b *Builder) ReferencedAttachment(ctx context.Context, oid string) (store.ReferencedObject, bool, error) {
	idx, err := b.referencedIndex(ctx)
	if err != nil {
		return store.ReferencedObject{}, false, err
	}
	obj, ok := idx.byOID[oid]
	return obj, ok, nil
}

// referencedIndex returns the referenced-attachment index, rebuilding it when
// the entity-ref digest differs from the cached one.
func (b *Builder) referencedIndex(ctx context.Context) (*attIndex, error) {
	digest, err := b.attDigest(ctx)
	if err != nil {
		return nil, err
	}

	b.attMu.Lock()
	if b.attCache != nil && b.attCache.digest == digest {
		idx := b.attCache
		b.attMu.Unlock()
		return idx, nil
	}
	b.attMu.Unlock()

	objs, err := b.store.ReferencedAttachments(ctx)
	if err != nil {
		return nil, fmt.Errorf("referenced attachments: %w", err)
	}
	byOID := make(map[string]store.ReferencedObject, len(objs))
	for _, o := range objs {
		byOID[o.OID] = o
	}
	idx := &attIndex{digest: digest, byOID: byOID}

	b.attMu.Lock()
	b.attCache = idx
	b.attMu.Unlock()
	return idx, nil
}

// attDigest hashes the sorted (ref, tip) pairs of every entity ref into the
// referenced-attachment cache key: any entity ref move, appearance, or removal
// changes it, so a cached index always matches the live entity state.
func (b *Builder) attDigest(ctx context.Context) (string, error) {
	refTips, err := b.entityRefs(ctx)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rt := range refTips {
		h.Write([]byte(rt.ref))
		h.Write([]byte{0})
		h.Write([]byte(rt.tip))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// handleBlob serves an attachment's content from the local LFS store. It
// rejects a malformed oid, 404s an oid no live entity references — never serving
// the code repository's own unrelated git-lfs objects — and turns an object in
// the referenced set but absent locally into a precise fetch hint. The served
// filename that drives the extension→MIME inference and Content-Disposition is
// only ever a recorded attachment name: a ?name= query is honored iff it exactly
// matches one of the object's recorded uses, else the first use's name stands, so
// an untrusted string can never force a Content-Type. Responses carry
// X-Content-Type-Options: nosniff and Content-Security-Policy: sandbox so a
// directly-navigated attachment (SVG, HTML) cannot execute script on this origin.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	oid := r.PathValue("oid")
	if !model.ValidAttachmentOID(oid) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid attachment oid %q: want 64 lower-hex characters", oid))
		return
	}

	obj, ok, err := s.builder.ReferencedAttachment(ctx, oid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no entity references attachment "+oid)
		return
	}

	content := s.store.LFS()
	f, err := content.Open(oid)
	if errors.Is(err, lfs.ErrObjectMissing) {
		writeError(w, http.StatusNotFound, blobMissingMessage(obj))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	name := obj.Uses[0].Name
	if q := r.URL.Query().Get("name"); slices.ContainsFunc(obj.Uses, func(u store.AttachmentUse) bool { return u.Name == q }) {
		name = q
	}
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype, err = sniffContentType(f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": name})
	if disposition == "" {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("ETag", `"`+oid+`"`)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Disposition", disposition)

	http.ServeContent(w, r, name, time.Time{}, f)
}

// sniffContentType detects f's content type from its first 512 bytes, then
// rewinds f so the caller can serve it from the start.
func sniffContentType(f *os.File) (string, error) {
	var head [512]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return http.DetectContentType(head[:n]), nil
}

// blobMissingMessage names the referenced-but-unfetched object — attachment,
// size, and owning entity — and points at the fix.
func blobMissingMessage(obj store.ReferencedObject) string {
	use := obj.Uses[0]
	return fmt.Sprintf("attachment %s (%s, on %s %s) has not been fetched locally — run cc-notes sync",
		use.Name, humanSize(obj.Size), use.Kind, use.Entity.Short())
}

// humanSize renders a byte count as a compact human-readable string.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
