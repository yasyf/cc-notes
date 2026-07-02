package fold_test

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

const (
	oidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oidC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestFoldNoteAttachmentsLWWByName(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddAttachment{Name: "z.txt", OID: oidA, Size: 1},
			model.AddAttachment{Name: "trace.png", OID: oidB, Size: 2},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.AddAttachment{Name: "trace.png", OID: oidC, Size: 3},
			model.AddAttachment{Name: "gone.bin", OID: oidA, Size: 4},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.RemoveAttachment{Name: "gone.bin"},
		),
	}
	want := model.Note{
		ID:           "aaa",
		Title:        "T",
		Tags:         []string{},
		Anchors:      []model.Anchor{},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    400,
		SupersededBy: []model.EntityID{},
		Head:         "ddd",
		Attachments: []model.Attachment{
			{Name: "trace.png", OID: oidC, Size: 3},
			{Name: "z.txt", OID: oidA, Size: 1},
		},
	}
	got, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Note() = %+v, want %+v", got, want)
	}
}

func TestFoldDocAttachmentsLWWByName(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateDoc{Nonce: "n", Title: "T", When: "W"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddAttachment{Name: "diagram.svg", OID: oidA, Size: 10},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.AddAttachment{Name: "diagram.svg", OID: oidB, Size: 20},
		),
	}
	want := model.Doc{
		ID:           "aaa",
		Title:        "T",
		When:         "W",
		Tags:         []string{},
		Anchors:      []model.Anchor{},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    300,
		SupersededBy: []model.EntityID{},
		Head:         "ccc",
		Attachments:  []model.Attachment{{Name: "diagram.svg", OID: oidB, Size: 20}},
	}
	got, err := fold.Doc(chain)
	if err != nil {
		t.Fatalf("Doc() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Doc() = %+v, want %+v", got, want)
	}
}

func TestFoldLogAttachmentsLWWByName(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLog{Nonce: "n", Title: "T"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AppendEntry{Text: "captured trace"},
			model.AddAttachment{Name: "trace.pcap", OID: oidA, Size: 5},
			model.AddAttachment{Name: "boot.log", OID: oidB, Size: 6},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.RemoveAttachment{Name: "boot.log"},
		),
	}
	want := model.Log{
		ID:          "aaa",
		Title:       "T",
		Entries:     []model.LogEntry{{Author: "bob", TS: 200, Text: "captured trace"}},
		Tags:        []string{},
		Anchors:     []model.Anchor{},
		Author:      "alice",
		CreatedAt:   100,
		UpdatedAt:   300,
		Head:        "ccc",
		Attachments: []model.Attachment{{Name: "trace.pcap", OID: oidA, Size: 5}},
	}
	got, err := fold.Log(chain)
	if err != nil {
		t.Fatalf("Log() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Log() = %+v, want %+v", got, want)
	}
}

// TestFoldNoteRemoveLastAttachmentIsNil proves removing the final attachment
// folds back to a nil Attachments slice — not an empty one — so the snapshot
// re-marshals without the field and byte-matches a never-attached snapshot.
func TestFoldNoteRemoveLastAttachmentIsNil(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.AddAttachment{Name: "a.png", OID: oidA, Size: 1}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.RemoveAttachment{Name: "a.png"}),
	}
	got, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if got.Attachments != nil {
		t.Fatalf("Attachments = %#v, want nil", got.Attachments)
	}
}

// TestFoldNoteAttachmentsCompactedEqualsFull seeds a fold from a checkpoint
// whose State carries attachments and replays a suffix that replaces one and
// removes another. The compacted fold must equal the full fold exactly —
// proving checkpoint seeds carry Attachments and covered attachment ops
// replay as no-ops.
func TestFoldNoteAttachmentsCompactedEqualsFull(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
		model.AddAttachment{Name: "keep.png", OID: oidA, Size: 1},
		model.AddAttachment{Name: "drop.bin", OID: oidB, Size: 2},
	)
	state, err := fold.Note([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("fold prefix: %v", err)
	}
	if len(state.Attachments) != 2 {
		t.Fatalf("seed Attachments = %+v, want 2 entries", state.Attachments)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
	cKempty := mk("cK", []string{"c1"}, "compactor", 250, 3)
	c2 := mk("c2", []string{"cK"}, "carol", 300, 4,
		model.AddAttachment{Name: "keep.png", OID: oidC, Size: 3},
		model.RemoveAttachment{Name: "drop.bin"},
	)
	compacted := []model.PackCommit{c0, c1, cK, c2}
	full := []model.PackCommit{c0, c1, cKempty, c2}

	gotFull, err := fold.Note(full)
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	gotCompact, err := fold.Note(compacted)
	if err != nil {
		t.Fatalf("fold compacted: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, gotFull) {
		t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
	}
	want := []model.Attachment{{Name: "keep.png", OID: oidC, Size: 3}}
	if !reflect.DeepEqual(gotCompact.Attachments, want) {
		t.Fatalf("Attachments = %+v, want %+v", gotCompact.Attachments, want)
	}
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible fold fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(3, 4))
	for i := range 30 {
		got, err := fold.Note(shuffled(compacted, r))
		if err != nil {
			t.Fatalf("shuffle %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, gotFull) {
			t.Fatalf("shuffle %d = %+v, want %+v", i, got, gotFull)
		}
	}
}
