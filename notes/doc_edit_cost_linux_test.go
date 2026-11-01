package notes_test

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/notes"
)

func TestDocEditSnapshotReadCost(t *testing.T) {
	c, _ := newClient(t)
	body := strings.Repeat("body", 1<<18)
	doc, _, err := c.CreateDoc(t.Context(), notes.DocSpec{
		Title: "before",
		Body:  body,
		When:  "before trigger",
		Tags:  []string{"keep", "drop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	warm, err := c.Doc(t.Context(), doc.ID)
	if err != nil || !reflect.DeepEqual(warm, doc) {
		t.Fatalf("warm document differs from created snapshot: %v", err)
	}
	title, when := "after", "after trigger"
	before := docEditLogicalReadBytes(t)
	edited, err := c.EditDoc(t.Context(), doc.ID, notes.DocEdit{
		Title:      &title,
		When:       &when,
		AddTags:    []string{"added", "transient"},
		RemoveTags: []string{"drop", "transient"},
	})
	readBytes := docEditLogicalReadBytes(t) - before
	if err != nil {
		t.Fatal(err)
	}
	want := doc
	want.Title = title
	want.When = when
	want.Tags = []string{"added", "keep"}
	want.Head = edited.Head
	want.UpdatedAt = edited.UpdatedAt
	if edited.Head == "" || edited.Head == doc.Head || edited.UpdatedAt < doc.UpdatedAt || !reflect.DeepEqual(edited, want) {
		t.Fatal("edited snapshot differs from the complete expected document")
	}
	persisted, err := c.Doc(t.Context(), doc.ID)
	if err != nil || !reflect.DeepEqual(persisted, want) {
		t.Fatalf("persisted snapshot differs from the complete expected document: %v", err)
	}
	limit := uint64(len(body) * 5 / 2)
	t.Logf("body_bytes=%d full_snapshot=PASS logical_rchar_bytes=%d max_logical_rchar=%d", len(body), readBytes, limit)
	if readBytes > limit {
		t.Fatalf("DOC_EDIT_SNAPSHOT_READ_REGRESSION: logical reads %d exceed %d for an unchanged %d-byte body", readBytes, limit, len(body))
	}
}

func docEditLogicalReadBytes(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "rchar:"); ok {
			count, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return count
		}
	}
	t.Fatal("/proc/self/io has no rchar counter")
	return 0
}
