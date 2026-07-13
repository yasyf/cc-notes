package notes_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// storeCreate roots a new entity chain in the repository at dir directly through
// the store, so tests build note/doc/log/runbook fixtures the notes.Client
// cannot yet create.
func storeCreate(t *testing.T, dir string, ops ...model.Op) model.Snapshot {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	snap, err := s.Create(context.Background(), ops)
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return snap
}

func TestTypedLoadsAndResolves(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "N"}).(model.Note)
	doc := storeCreate(t, dir, model.CreateDoc{Nonce: model.NewNonce(), Title: "D", When: "always"}).(model.Doc)
	logE := storeCreate(t, dir, model.CreateLog{Nonce: model.NewNonce(), Title: "L"}).(model.Log)
	rb := storeCreate(t, dir, model.CreateRunbook{Nonce: model.NewNonce(), Title: "R"}).(model.Runbook)

	if got, err := c.Note(ctx, note.ID); err != nil || got.Title != "N" {
		t.Errorf("Note = %q/%v, want N/nil", got.Title, err)
	}
	if got, err := c.Doc(ctx, doc.ID); err != nil || got.Title != "D" {
		t.Errorf("Doc = %q/%v, want D/nil", got.Title, err)
	}
	if got, err := c.Log(ctx, logE.ID); err != nil || got.Title != "L" {
		t.Errorf("Log = %q/%v, want L/nil", got.Title, err)
	}
	if got, err := c.Runbook(ctx, rb.ID); err != nil || got.Title != "R" {
		t.Errorf("Runbook = %q/%v, want R/nil", got.Title, err)
	}

	if got, err := c.ResolveNote(ctx, string(note.ID)); err != nil || got != note.ID {
		t.Errorf("ResolveNote = %q/%v, want %q", got, err, note.ID)
	}
	if got, err := c.ResolveDoc(ctx, string(doc.ID)); err != nil || got != doc.ID {
		t.Errorf("ResolveDoc = %q/%v, want %q", got, err, doc.ID)
	}
	if got, err := c.ResolveLog(ctx, string(logE.ID)); err != nil || got != logE.ID {
		t.Errorf("ResolveLog = %q/%v, want %q", got, err, logE.ID)
	}
	if got, err := c.ResolveRunbook(ctx, string(rb.ID)); err != nil || got != rb.ID {
		t.Errorf("ResolveRunbook = %q/%v, want %q", got, err, rb.ID)
	}

	for _, tc := range []struct {
		kind model.Kind
		id   model.EntityID
	}{
		{model.KindNote, note.ID},
		{model.KindDoc, doc.ID},
		{model.KindLog, logE.ID},
		{model.KindRunbook, rb.ID},
	} {
		k, id, err := c.ResolveEntity(ctx, string(tc.id))
		if err != nil || k != tc.kind || id != tc.id {
			t.Errorf("ResolveEntity(%s) = %s/%q/%v, want %s/%q", tc.id, k, id, err, tc.kind, tc.id)
		}
	}

	if _, err := c.Note(ctx, model.EntityID(strings.Repeat("a", 40))); !errors.Is(err, notes.ErrRefNotFound) {
		t.Errorf("Note on missing id = %v, want ErrRefNotFound", err)
	}
	if _, _, err := c.ResolveEntity(ctx, "zzzzzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveEntity(zzzzzz) = %v, want ErrNotFound", err)
	}
}

func TestResolveEntityCrossKind(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "N"})
	if _, err := c.CreateTask(ctx, notes.TaskSpec{Title: "T", Branch: "main"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// The empty prefix matches the sole note and the sole task, one per kind, so
	// resolution is ambiguous across kinds.
	_, _, err := c.ResolveEntity(ctx, "")
	if !errors.Is(err, notes.ErrAmbiguous) {
		t.Fatalf("ResolveEntity(\"\") cross-kind = %v, want ErrAmbiguous", err)
	}
	var ake *notes.AmbiguousKindsError
	if !errors.As(err, &ake) {
		t.Fatalf("err = %T, want *AmbiguousKindsError", err)
	}
	if len(ake.Matches) != 2 {
		t.Errorf("Matches = %d, want 2 (note + task): %+v", len(ake.Matches), ake.Matches)
	}
	kinds := map[model.Kind]bool{}
	for _, m := range ake.Matches {
		kinds[m.Kind] = true
	}
	if !kinds[model.KindNote] || !kinds[model.KindTask] {
		t.Errorf("Matches kinds = %v, want note and task", kinds)
	}
}

func TestResolveAttachable(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "N"}).(model.Note)

	k, id, err := c.ResolveAttachable(ctx, string(note.ID))
	if err != nil || k != model.KindNote || id != note.ID {
		t.Fatalf("ResolveAttachable(full) = %s/%q/%v, want note/%q", k, id, err, note.ID)
	}
	if _, _, err := c.ResolveAttachable(ctx, "zzzzzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveAttachable(zzzzzz) = %v, want ErrNotFound", err)
	}

	// Add a doc so the empty prefix matches note + doc across attachment-bearing
	// kinds.
	storeCreate(t, dir, model.CreateDoc{Nonce: model.NewNonce(), Title: "D", When: "x"})
	_, _, err = c.ResolveAttachable(ctx, "")
	var ake *notes.AmbiguousKindsError
	if !errors.As(err, &ake) {
		t.Fatalf("ResolveAttachable(\"\") = %v (%T), want *AmbiguousKindsError", err, err)
	}
	if len(ake.Matches) != 2 {
		t.Errorf("Matches = %d, want 2 (note + doc)", len(ake.Matches))
	}
}

func TestAttachRoundTrip(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	src := filepath.Join(t.TempDir(), "data.txt")
	content := []byte("payload bytes")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	att, guarded, err := c.AttachFile(ctx, src)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	if !guarded {
		t.Error("first AttachFile guarded = false, want true")
	}
	if att.Name != "data.txt" || att.Size != int64(len(content)) {
		t.Errorf("attachment = %+v, want name data.txt size %d", att, len(content))
	}

	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "with-att"}, model.AddAttachment(att)).(model.Note)
	if len(note.Attachments) != 1 {
		t.Fatalf("note attachments = %d, want 1", len(note.Attachments))
	}

	gotAtt, rc, err := c.OpenAttachment(ctx, model.KindNote, note.ID, "data.txt")
	if err != nil {
		t.Fatalf("OpenAttachment: %v", err)
	}
	if gotAtt.OID != att.OID {
		t.Errorf("OpenAttachment oid = %s, want %s", gotAtt.OID, att.OID)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("attachment bytes = %q, want %q", data, content)
	}

	_, path, err := c.AttachmentPath(ctx, model.KindNote, note.ID, "data.txt")
	if err != nil {
		t.Fatalf("AttachmentPath: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("AttachmentPath = %q, want absolute", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("AttachmentPath %q not present: %v", path, err)
	}

	infos, err := c.AttachmentInfos(ctx, note.Attachments)
	if err != nil {
		t.Fatalf("AttachmentInfos: %v", err)
	}
	if len(infos) != 1 || !infos[0].Present {
		t.Errorf("AttachmentInfos = %+v, want one Present", infos)
	}

	empty, err := c.AttachmentInfos(ctx, nil)
	if err != nil {
		t.Fatalf("AttachmentInfos(nil): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("AttachmentInfos(nil) = %v, want non-nil empty", empty)
	}

	if _, _, err := c.OpenAttachment(ctx, model.KindNote, note.ID, "nope"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("OpenAttachment(unknown name) = %v, want ErrNotFound", err)
	}

	// A metadata-only attachment (its bytes never ingested) is absent locally.
	ghost := model.Attachment{Name: "ghost.txt", OID: strings.Repeat("a", 64), Size: 7}
	ghostNote := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "ghost"}, model.AddAttachment(ghost)).(model.Note)
	if _, _, err := c.OpenAttachment(ctx, model.KindNote, ghostNote.ID, "ghost.txt"); !isMissingContent(err) {
		t.Errorf("OpenAttachment(absent bytes) = %v, want *MissingContentError", err)
	}
	if _, _, err := c.AttachmentPath(ctx, model.KindNote, ghostNote.ID, "ghost.txt"); !isMissingContent(err) {
		t.Errorf("AttachmentPath(absent bytes) = %v, want *MissingContentError", err)
	}
	ghostInfos, err := c.AttachmentInfos(ctx, ghostNote.Attachments)
	if err != nil {
		t.Fatalf("AttachmentInfos(ghost): %v", err)
	}
	if len(ghostInfos) != 1 || ghostInfos[0].Present {
		t.Errorf("ghost AttachmentInfos = %+v, want one absent", ghostInfos)
	}
}

func TestLeaseTTLAndNoteStaleAfter(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	if d, err := c.LeaseTTL(ctx); err != nil || d != time.Hour {
		t.Errorf("default LeaseTTL = %v/%v, want 1h", d, err)
	}
	if d, err := c.NoteStaleAfter(ctx); err != nil || d != 90*24*time.Hour {
		t.Errorf("default NoteStaleAfter = %v/%v, want 90d", d, err)
	}

	gittest.Git(t, dir, "config", "cc-notes.leaseTTL", "3h")
	gittest.Git(t, dir, "config", "cc-notes.noteStaleAfter", "48h")
	if d, err := c.LeaseTTL(ctx); err != nil || d != 3*time.Hour {
		t.Errorf("config LeaseTTL = %v/%v, want 3h", d, err)
	}
	if d, err := c.NoteStaleAfter(ctx); err != nil || d != 48*time.Hour {
		t.Errorf("config NoteStaleAfter = %v/%v, want 48h", d, err)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "15m")
	if d, err := c.LeaseTTL(ctx); err != nil || d != 15*time.Minute {
		t.Errorf("env LeaseTTL = %v/%v, want 15m (env overrides config)", d, err)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "not-a-duration")
	if _, err := c.LeaseTTL(ctx); err == nil {
		t.Error("malformed CC_NOTES_LEASE_TTL = nil error, want error")
	}
}

func TestScaffoldingErrorTypes(t *testing.T) {
	id := model.EntityID(strings.Repeat("a", 40))

	uce := &notes.UnmetCriteriaError{ID: id, Unmet: []model.Criterion{{ID: "crit1234", Text: "x"}}}
	if !strings.Contains(uce.Error(), "unmet") || !strings.Contains(uce.Error(), id.Short()) {
		t.Errorf("UnmetCriteriaError.Error() = %q", uce.Error())
	}

	aee := &notes.AttachmentExistsError{Name: "f.txt"}
	if !strings.Contains(aee.Error(), "f.txt") {
		t.Errorf("AttachmentExistsError.Error() = %q", aee.Error())
	}

	mce := &notes.MissingContentError{Attachment: model.Attachment{Name: "g.txt", OID: strings.Repeat("b", 64), Size: 1}}
	if !strings.Contains(mce.Error(), "g.txt") {
		t.Errorf("MissingContentError.Error() = %q", mce.Error())
	}

	ake := &notes.AmbiguousKindsError{Prefix: "ab", Matches: []notes.KindMatch{{Kind: model.KindNote, ID: id, Title: "N"}}}
	if !errors.Is(ake, notes.ErrAmbiguous) {
		t.Error("AmbiguousKindsError is not ErrAmbiguous")
	}
	if !strings.Contains(ake.Error(), "ab") || !strings.Contains(ake.Error(), "note") {
		t.Errorf("AmbiguousKindsError.Error() = %q", ake.Error())
	}

	if errors.Is(notes.ErrEmptyEdit, notes.ErrCycle) {
		t.Error("ErrEmptyEdit and ErrCycle must be distinct sentinels")
	}

	// The AmbiguousError alias keeps errors.As/Is working without importing store.
	wrapped := error(&notes.AmbiguousError{Kind: model.KindNote, Prefix: "x", Candidates: []notes.Candidate{{ID: "id", Title: "t"}}})
	var ae *notes.AmbiguousError
	if !errors.As(wrapped, &ae) {
		t.Error("errors.As on *notes.AmbiguousError failed")
	}
	if !errors.Is(wrapped, notes.ErrAmbiguous) {
		t.Error("*notes.AmbiguousError is not ErrAmbiguous")
	}
}

func isMissingContent(err error) bool {
	var e *notes.MissingContentError
	return errors.As(err, &e)
}
