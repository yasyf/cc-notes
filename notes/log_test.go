package notes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// attachLog writes a file named name under dir with content and ingests it into
// the local LFS store, returning the attachment.
func attachLog(ctx context.Context, t *testing.T, c *notes.Client, dir, name, content string) model.Attachment {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	att, _, err := c.AttachFile(ctx, p)
	if err != nil {
		t.Fatalf("AttachFile(%s): %v", name, err)
	}
	return att
}

func logTitles(logs []model.Log) []string {
	out := make([]string, len(logs))
	for i, l := range logs {
		out[i] = l.Title
	}
	return out
}

func TestCreateLogTwoWriteShape(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	bare, reused, err := c.CreateLog(ctx, notes.LogSpec{Title: "Bare"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if reused {
		t.Fatal("first CreateLog reported reused")
	}
	if len(bare.Entries) != 0 {
		t.Fatalf("entries = %+v, want none on a bare create", bare.Entries)
	}
	if got := gittest.Git(t, dir, "rev-list", "--count", "refs/cc-notes/logs/"+string(bare.ID)); got != "1" {
		t.Errorf("bare log chain = %s commits, want 1 (create only)", got)
	}

	withEntry, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Rollout", Entry: "flipped to 5%"})
	if err != nil {
		t.Fatalf("CreateLog with entry: %v", err)
	}
	if len(withEntry.Entries) != 1 || withEntry.Entries[0].Text != "flipped to 5%" {
		t.Fatalf("entries = %+v, want the one first entry", withEntry.Entries)
	}
	if withEntry.Entries[0].Author != model.Actor(testActor) {
		t.Errorf("entry author = %q, want %q", withEntry.Entries[0].Author, testActor)
	}
	if got := gittest.Git(t, dir, "rev-list", "--count", "refs/cc-notes/logs/"+string(withEntry.ID)); got != "2" {
		t.Errorf("entry log chain = %s commits, want 2 (create + separate entry write)", got)
	}
}

func TestCreateLogDedupeReused(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	first, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Dup", Tags: []string{"ops"}})
	if err != nil {
		t.Fatalf("first CreateLog: %v", err)
	}
	again, reused, err := c.CreateLog(ctx, notes.LogSpec{Title: "Dup", Tags: []string{"ops"}})
	if err != nil {
		t.Fatalf("second CreateLog: %v", err)
	}
	if !reused || again.ID != first.ID {
		t.Fatalf("second CreateLog = reused %v id %s, want reused onto %s", reused, again.ID, first.ID)
	}
}

func TestAppendLogTextEmptyAndCollision(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()

	first := attachLog(ctx, t, c, dir, "trace.log", "one")
	log, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Incident", Attachments: []model.Attachment{first}})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	// Empty append (no text, no attachment) is refused before any write.
	if _, err := c.AppendLog(ctx, log.ID, notes.LogAppend{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("empty AppendLog = %v, want ErrEmptyEdit", err)
	}

	// Text append carries the model identity.
	appended, err := c.AppendLog(ctx, log.ID, notes.LogAppend{Text: "escalated", Model: "claude"})
	if err != nil {
		t.Fatalf("AppendLog text: %v", err)
	}
	if len(appended.Entries) != 1 || appended.Entries[0].Text != "escalated" || appended.Entries[0].Model != "claude" {
		t.Fatalf("entries = %+v, want one entry 'escalated' with model claude", appended.Entries)
	}

	// A same-named attachment collides unless --replace.
	second := attachLog(ctx, t, c, dir, "trace.log", "two, longer")
	_, err = c.AppendLog(ctx, log.ID, notes.LogAppend{Attachments: []model.Attachment{second}})
	var exists *notes.AttachmentExistsError
	if !errors.As(err, &exists) || exists.Name != "trace.log" {
		t.Fatalf("colliding AppendLog = %v, want *AttachmentExistsError naming trace.log", err)
	}
	// The rejected append changed nothing.
	reloaded, err := c.Log(ctx, log.ID)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(reloaded.Attachments) != 1 || reloaded.Attachments[0].OID != first.OID {
		t.Fatalf("attachments after rejected append = %+v, want the original untouched", reloaded.Attachments)
	}

	// ReplaceAttachments overwrites the name; an attach-only append adds no entry.
	replaced, err := c.AppendLog(ctx, log.ID, notes.LogAppend{Attachments: []model.Attachment{second}, ReplaceAttachments: true})
	if err != nil {
		t.Fatalf("AppendLog replace: %v", err)
	}
	if len(replaced.Attachments) != 1 || replaced.Attachments[0].OID != second.OID {
		t.Fatalf("attachments after replace = %+v, want oid %s", replaced.Attachments, second.OID)
	}
	if len(replaced.Entries) != 1 {
		t.Fatalf("entries after attach-only append = %d, want the one prior text entry (no new entry)", len(replaced.Entries))
	}
}

func TestEditLogMaskEmptyAndEntriesPreserved(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	log, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "First", Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if _, err := c.AppendLog(ctx, log.ID, notes.LogAppend{Text: "an entry"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if _, err := c.EditLog(ctx, log.ID, notes.LogEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("empty EditLog = %v, want ErrEmptyEdit", err)
	}

	title := "Second"
	edited, err := c.EditLog(ctx, log.ID, notes.LogEdit{
		Title:         &title,
		AddTags:       []string{"ops"},
		RemoveAnchors: notes.AnchorSpec{Paths: []string{"a.go"}},
	})
	if err != nil {
		t.Fatalf("EditLog: %v", err)
	}
	if edited.Title != "Second" || !slices.Contains(edited.Tags, "ops") {
		t.Fatalf("edited title/tags = %q/%v, want Second/[ops]", edited.Title, edited.Tags)
	}
	if hasAnchorValue(edited.Anchors, model.AnchorPath, "a.go") {
		t.Errorf("anchors = %+v, want the a.go path anchor removed verbatim", edited.Anchors)
	}
	if len(edited.Entries) != 1 || edited.Entries[0].Text != "an entry" {
		t.Fatalf("entries after metadata edit = %+v, want the append preserved untouched", edited.Entries)
	}
}

func TestRemoveLogSoftTombstone(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	log, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Doomed"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	removed, err := c.RemoveLog(ctx, log.ID)
	if err != nil {
		t.Fatalf("RemoveLog: %v", err)
	}
	if !removed.Deleted {
		t.Fatalf("removed log = %+v, want Deleted", removed)
	}
	// A soft tombstone still resolves and still accepts an append.
	appended, err := c.AppendLog(ctx, log.ID, notes.LogAppend{Text: "post-mortem"})
	if err != nil {
		t.Fatalf("AppendLog to tombstoned log: %v", err)
	}
	if len(appended.Entries) != 1 || appended.Entries[0].Text != "post-mortem" {
		t.Fatalf("entries after append to deleted = %+v, want the appended entry", appended.Entries)
	}
}

func TestLogsFilterOrderAndDeleted(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	keep, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Kept", Tags: []string{"keep"}, Anchors: notes.AnchorSpec{Dirs: []string{"internal/api"}}})
	if err != nil {
		t.Fatalf("CreateLog keep: %v", err)
	}
	drop, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Dropped", Tags: []string{"skip"}})
	if err != nil {
		t.Fatalf("CreateLog drop: %v", err)
	}

	byTag, err := c.Logs(ctx, notes.LogFilter{Labels: []string{"keep"}})
	if err != nil {
		t.Fatalf("Logs by label: %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != keep.ID {
		t.Fatalf("Logs --label keep = %v, want only Kept", logTitles(byTag))
	}

	byDir, err := c.Logs(ctx, notes.LogFilter{Anchors: notes.AnchorFilter{Dir: "internal/api"}})
	if err != nil {
		t.Fatalf("Logs by dir: %v", err)
	}
	if len(byDir) != 1 || byDir[0].ID != keep.ID {
		t.Fatalf("Logs --dir internal/api = %v, want only Kept", logTitles(byDir))
	}

	all, err := c.Logs(ctx, notes.LogFilter{})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Logs = %v, want both live logs", logTitles(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].UpdatedAt < all[i].UpdatedAt {
			t.Errorf("Logs not UpdatedAt-desc: [%d]=%d before [%d]=%d", i-1, all[i-1].UpdatedAt, i, all[i].UpdatedAt)
		}
	}

	if _, err := c.RemoveLog(ctx, drop.ID); err != nil {
		t.Fatalf("RemoveLog: %v", err)
	}
	live, err := c.Logs(ctx, notes.LogFilter{})
	if err != nil {
		t.Fatalf("Logs live: %v", err)
	}
	if len(live) != 1 || live[0].ID != keep.ID {
		t.Fatalf("Logs after rm = %v, want only Kept", logTitles(live))
	}
	withDeleted, err := c.Logs(ctx, notes.LogFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Logs IncludeDeleted: %v", err)
	}
	if len(withDeleted) != 2 {
		t.Fatalf("Logs IncludeDeleted = %v, want both including the tombstone", logTitles(withDeleted))
	}
}

func TestSearchLogs(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rollout, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Rollout log", Tags: []string{"ops"}, Entry: "the Tokenizer panicked at noon"})
	if err != nil {
		t.Fatalf("CreateLog rollout: %v", err)
	}
	if _, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "Other", Tags: []string{"misc"}}); err != nil {
		t.Fatalf("CreateLog other: %v", err)
	}

	for _, tc := range []struct{ query, want string }{
		{"ROLLOUT", "Rollout log"},
		{"ops", "Rollout log"},
		{"tokenizer", "Rollout log"},
		{"misc", "Other"},
	} {
		got, err := c.SearchLogs(ctx, tc.query, notes.SearchFilter{Limit: 20})
		if err != nil {
			t.Fatalf("SearchLogs(%q): %v", tc.query, err)
		}
		if len(got) != 1 || got[0].Title != tc.want {
			t.Errorf("SearchLogs(%q) = %v, want one [%s]", tc.query, logTitles(got), tc.want)
		}
	}

	// A tombstoned log drops out of search.
	if _, err := c.RemoveLog(ctx, rollout.ID); err != nil {
		t.Fatalf("RemoveLog: %v", err)
	}
	got, err := c.SearchLogs(ctx, "tokenizer", notes.SearchFilter{Limit: 20})
	if err != nil {
		t.Fatalf("SearchLogs after rm: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SearchLogs after rm = %v, want empty (tombstoned excluded)", logTitles(got))
	}
}
