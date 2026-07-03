package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	oidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestAttachFile(t *testing.T) {
	s := initStore(t)
	content := []byte("attachment payload\x00\x01binary bytes")
	sum := sha256.Sum256(content)
	wantOID := hex.EncodeToString(sum[:])
	path := writeTempFile(t, "trace.bin", content)

	att, guarded, err := s.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	want := model.Attachment{Name: "trace.bin", OID: wantOID, Size: int64(len(content))}
	if att != want {
		t.Errorf("AttachFile = %+v, want %+v", att, want)
	}
	if !guarded {
		t.Errorf("first AttachFile guarded = false, want true (installs %s)", PruneGuardConfig)
	}
	if got := mustGit(t, s.Git.Dir, "config", "--get", "lfs.pruneverifyremotealways"); got != "true" {
		t.Errorf("lfs.pruneverifyremotealways = %q, want %q", got, "true")
	}
	content2, err := s.LFS(t.Context())
	if err != nil {
		t.Fatalf("LFS: %v", err)
	}
	if !content2.Has(wantOID) {
		t.Errorf("LFS store missing %s after AttachFile", wantOID)
	}
	stored, err := os.ReadFile(content2.Path(wantOID))
	if err != nil {
		t.Fatalf("read stored object: %v", err)
	}
	if !reflect.DeepEqual(stored, content) {
		t.Errorf("stored bytes = %q, want %q", stored, content)
	}

	_, guarded, err = s.AttachFile(t.Context(), writeTempFile(t, "second.txt", []byte("more")))
	if err != nil {
		t.Fatalf("second AttachFile: %v", err)
	}
	if guarded {
		t.Error("second AttachFile guarded = true, want false (guard already installed)")
	}
	if got := configAll(t, s, "lfs.pruneverifyremotealways"); len(got) != 1 {
		t.Errorf("lfs.pruneverifyremotealways set %d times, want once: %q", len(got), got)
	}
}

func configAll(t *testing.T, s *Store, key string) []string {
	t.Helper()
	values, err := s.Git.ConfigGetAll(t.Context(), key)
	if err != nil {
		t.Fatalf("ConfigGetAll(%s): %v", key, err)
	}
	return values
}

func TestAttachFileErrors(t *testing.T) {
	s := initStore(t)
	tests := []struct {
		name string
		path func(t *testing.T) string
		want error
	}{
		{
			name: "empty file",
			path: func(t *testing.T) string { return writeTempFile(t, "empty.bin", nil) },
			want: model.ErrInvalidValue,
		},
		{
			name: "missing file",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.bin") },
			want: os.ErrNotExist,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, guarded, err := s.AttachFile(t.Context(), tt.path(t))
			if !errors.Is(err, tt.want) {
				t.Fatalf("AttachFile error = %v, want %v", err, tt.want)
			}
			if guarded {
				t.Error("failed AttachFile guarded = true, want false")
			}
		})
	}
	if got := configAll(t, s, "lfs.pruneverifyremotealways"); len(got) != 0 {
		t.Errorf("failed attaches wrote the prune guard: %q", got)
	}
}

// attach appends an add_attachment op referencing a fabricated oid — content
// presence is irrelevant to the referenced-set scan.
func attach(t *testing.T, s *Store, ref, name, oid string, size int64) {
	t.Helper()
	if _, err := s.Append(t.Context(), ref, []model.Op{model.AddAttachment{Name: name, OID: oid, Size: size}}); err != nil {
		t.Fatalf("append add_attachment to %s: %v", ref, err)
	}
}

func detach(t *testing.T, s *Store, ref, name string) {
	t.Helper()
	if _, err := s.Append(t.Context(), ref, []model.Op{model.RemoveAttachment{Name: name}}); err != nil {
		t.Fatalf("append remove_attachment to %s: %v", ref, err)
	}
}

func referenced(t *testing.T, s *Store) []ReferencedObject {
	t.Helper()
	got, err := s.ReferencedAttachments(t.Context())
	if err != nil {
		t.Fatalf("ReferencedAttachments: %v", err)
	}
	return got
}

func TestReferencedAttachments(t *testing.T) {
	s := initStore(t)
	if got := referenced(t, s); len(got) != 0 {
		t.Fatalf("empty repo referenced = %+v, want none", got)
	}

	note, err := s.Create(t.Context(), noteOps("note with attachments"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	doc, err := s.Create(t.Context(), []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "doc", When: "always"}})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	logEntity, err := s.Create(t.Context(), []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "log"}})
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	if _, err := s.Create(t.Context(), []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "no attachments", Type: model.TypeTask, Branch: "main"}}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	noteRef := refs.Note(note.EntityID())
	docRef := refs.Doc(doc.EntityID())
	logRef := refs.Log(logEntity.EntityID())

	attach(t, s, noteRef, "shared.bin", oidA, 5)
	attach(t, s, docRef, "copy.bin", oidA, 5)
	attach(t, s, logRef, "solo.txt", oidB, 9)

	want := []ReferencedObject{
		{OID: oidA, Size: 5, Uses: []AttachmentUse{
			{Kind: refs.KindDoc, Entity: doc.EntityID(), Name: "copy.bin"},
			{Kind: refs.KindNote, Entity: note.EntityID(), Name: "shared.bin"},
		}},
		{OID: oidB, Size: 9, Uses: []AttachmentUse{
			{Kind: refs.KindLog, Entity: logEntity.EntityID(), Name: "solo.txt"},
		}},
	}
	if got := referenced(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("referenced = %+v, want %+v", got, want)
	}

	detach(t, s, docRef, "copy.bin")
	detach(t, s, logRef, "solo.txt")
	want = []ReferencedObject{
		{OID: oidA, Size: 5, Uses: []AttachmentUse{
			{Kind: refs.KindNote, Entity: note.EntityID(), Name: "shared.bin"},
		}},
	}
	if got := referenced(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("after removals, referenced = %+v, want %+v", got, want)
	}

	detach(t, s, noteRef, "shared.bin")
	if got := referenced(t, s); len(got) != 0 {
		t.Fatalf("after removing the last attachment, referenced = %+v, want none", got)
	}
}

// TestReferencedAttachmentsKeepsCheckpointState pins the ∪-checkpoint half of
// the live set: an attachment covered by a checkpoint State stays referenced
// after a later remove, because folds seed from the checkpoint and must be
// able to resolve its content.
func TestReferencedAttachmentsKeepsCheckpointState(t *testing.T) {
	s := initStore(t)
	note, err := s.Create(t.Context(), noteOps("checkpointed"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	noteRef := refs.Note(note.EntityID())
	attach(t, s, noteRef, "pinned.bin", oidA, 5)
	if _, err := s.Compact(t.Context(), noteRef); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	detach(t, s, noteRef, "pinned.bin")

	want := []ReferencedObject{
		{OID: oidA, Size: 5, Uses: []AttachmentUse{
			{Kind: refs.KindNote, Entity: note.EntityID(), Name: "pinned.bin"},
		}},
	}
	if got := referenced(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("referenced after checkpointed remove = %+v, want %+v (checkpoint State keeps it live)", got, want)
	}
}

// TestReferencedAttachmentsColdCache proves the scan does not depend on fold
// cache state: a store reopened with an empty cache directory sees the same
// set.
func TestReferencedAttachmentsColdCache(t *testing.T) {
	s := initStore(t)
	note, err := s.Create(t.Context(), noteOps("cold"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	attach(t, s, refs.Note(note.EntityID()), "cold.bin", oidB, 3)
	warm := referenced(t, s)

	reopened, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened.cache = newFoldCache(t.TempDir(), foldCacheCap)
	if cold := referenced(t, reopened); !reflect.DeepEqual(cold, warm) {
		t.Fatalf("cold-cache referenced = %+v, want %+v", cold, warm)
	}
	if !strings.HasPrefix(warm[0].OID, "b") || warm[0].Size != 3 {
		t.Fatalf("referenced = %+v, want oid %s size 3", warm, oidB)
	}
}
