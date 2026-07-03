//go:build fuse

// White-box tests for the read-only /attachments tree: real store, real LFS
// object files, callbacks driven directly (see fs_test.go).
package fusefs

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// attachNote creates a note carrying one attachment ingested from real file
// content and returns the note with the attachment's oid.
func attachNote(t *testing.T, s *store.Store, title, name string, content []byte) (model.Note, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	att, _, err := s.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	snap, err := s.Create(t.Context(), []model.Op{
		model.CreateNote{Nonce: model.NewNonce(), Title: title},
		model.AddAttachment{Name: att.Name, OID: att.OID, Size: att.Size},
	})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	return snap.(model.Note), att.OID
}

func TestAttachmentsTreeListsAndReads(t *testing.T) {
	f, s := newTestFS(t)
	content := []byte("attachment bytes served straight from the object store")
	note, _ := attachNote(t, s, "Carrier", "trace.log", content)
	bare := createNote(t, s, "Bare", "no attachments")

	root, errc := f.listDir("/")
	if errc != 0 || !slices.Contains(root, "attachments") {
		t.Fatalf("listDir(/) = %v (%d), want attachments listed", root, errc)
	}
	entities, errc := f.listDir("/attachments")
	if errc != 0 || !slices.Equal(entities, []string{note.ID.Short()}) {
		t.Fatalf("listDir(/attachments) = %v (%d), want exactly [%s]", entities, errc, note.ID.Short())
	}
	names, errc := f.listDir("/attachments/" + note.ID.Short())
	if errc != 0 || !slices.Equal(names, []string{"trace.log"}) {
		t.Fatalf("listDir(entity) = %v (%d), want [trace.log]", names, errc)
	}

	p := "/attachments/" + note.ID.Short() + "/trace.log"
	var st fuse.Stat_t
	if errc := f.Getattr(p, &st, invalidFh); errc != 0 || st.Size != int64(len(content)) || st.Mode != fuse.S_IFREG|0o444 {
		t.Fatalf("Getattr(%s) = %d size %d mode %o, want size %d mode %o", p, errc, st.Size, st.Mode, len(content), fuse.S_IFREG|0o444)
	}

	errc, fh := f.Open(p, fuse.O_RDONLY)
	if errc != 0 {
		t.Fatalf("Open(%s) = %d", p, errc)
	}
	buf := make([]byte, len(content)+16)
	n := f.Read(p, buf, 0, fh)
	if n != len(content) || !bytes.Equal(buf[:n], content) {
		t.Fatalf("Read = %d %q, want the full %d attachment bytes", n, buf[:max(n, 0)], len(content))
	}
	window := make([]byte, 10)
	if n := f.Read(p, window, 11, fh); n != 10 || !bytes.Equal(window, content[11:21]) {
		t.Fatalf("windowed Read = %d %q, want %q", n, window[:max(n, 0)], content[11:21])
	}
	if n := f.Read(p, buf, int64(len(content))+5, fh); n != 0 {
		t.Fatalf("Read past EOF = %d, want 0", n)
	}
	if n := f.Write(p, []byte("x"), 0, fh); n != -fuse.EACCES {
		t.Fatalf("Write on attachment handle = %d, want -EACCES", n)
	}
	if errc := f.Release(p, fh); errc != 0 {
		t.Fatalf("Release = %d", errc)
	}

	// Path-based read without a live handle (FUSE-T NFS fallback).
	stateless := make([]byte, len(content))
	if n := f.Read(p, stateless, 0, invalidFh); n != len(content) || !bytes.Equal(stateless, content) {
		t.Fatalf("stateless Read = %d %q, want the attachment bytes", n, stateless[:max(n, 0)])
	}

	// The read-only subtree rejects every mutation.
	if errc, _ := f.Open(p, fuse.O_RDWR); errc != -fuse.EACCES {
		t.Errorf("Open(O_RDWR) = %d, want -EACCES", errc)
	}
	if errc, _ := f.Create("/attachments/"+note.ID.Short()+"/new.txt", 0, 0o644); errc != -fuse.EPERM {
		t.Errorf("Create under /attachments = %d, want -EPERM", errc)
	}
	if errc := f.Truncate(p, 0, invalidFh); errc != -fuse.EACCES {
		t.Errorf("Truncate = %d, want -EACCES", errc)
	}
	if errc := f.Unlink(p); errc != -fuse.EPERM {
		t.Errorf("Unlink = %d, want -EPERM", errc)
	}
	if errc, _ := f.Create("/scratch.txt", 0, 0o644); errc != 0 {
		t.Fatalf("Create(/scratch.txt) = %d", errc)
	}
	if errc := f.Rename("/scratch.txt", "/attachments/"+note.ID.Short()+"/scratch.txt"); errc != -fuse.EPERM {
		t.Errorf("Rename into /attachments = %d, want -EPERM", errc)
	}

	// An entity without attachments is absent from the tree.
	if errc := f.Getattr("/attachments/"+bare.ID.Short(), &st, invalidFh); errc != -fuse.ENOENT {
		t.Errorf("Getattr(bare entity dir) = %d, want -ENOENT", errc)
	}
}

func TestAttachmentMissingObjectReadsEIO(t *testing.T) {
	f, s := newTestFS(t)
	note, oid := attachNote(t, s, "Carrier", "gone.bin", []byte("removed below"))
	content, err := s.LFS(t.Context())
	if err != nil {
		t.Fatalf("LFS: %v", err)
	}
	if err := os.Remove(content.Path(oid)); err != nil {
		t.Fatalf("remove object: %v", err)
	}

	p := "/attachments/" + note.ID.Short() + "/gone.bin"
	var st fuse.Stat_t
	if errc := f.Getattr(p, &st, invalidFh); errc != 0 || st.Size != 13 {
		t.Fatalf("Getattr = %d size %d, want the referenced size 13 — metadata stays true", errc, st.Size)
	}
	if errc, _ := f.Open(p, fuse.O_RDONLY); errc != -fuse.EIO {
		t.Fatalf("Open(missing object) = %d, want -EIO", errc)
	}
	first := f.missingLogged[oid]
	if first.IsZero() {
		t.Fatal("missing-object log was not recorded")
	}
	if errc, _ := f.Open(p, fuse.O_RDONLY); errc != -fuse.EIO {
		t.Fatalf("second Open(missing object) = %d, want -EIO", errc)
	}
	if f.missingLogged[oid] != first {
		t.Fatal("missing-object log not rate-limited: second open re-logged within the interval")
	}
}

func TestAttachmentUnknownNamesReadENOENT(t *testing.T) {
	f, s := newTestFS(t)
	note, _ := attachNote(t, s, "Carrier", "real.txt", []byte("x"))
	var st fuse.Stat_t
	for _, p := range []string{
		"/attachments/" + note.ID.Short() + "/nope.txt",
		"/attachments/deadbee",
		"/attachments/deadbee/file.txt",
	} {
		if errc := f.Getattr(p, &st, invalidFh); errc != -fuse.ENOENT {
			t.Errorf("Getattr(%s) = %d, want -ENOENT", p, errc)
		}
	}
}
