package cli

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestLeanDocLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  model.Doc
		want string
	}{
		{
			name: "when and tags present",
			doc:  model.Doc{ID: "abc1234def", UpdatedAt: 1735689600, Tags: []string{"x", "y"}, Title: "My Doc", When: "resuming the cutover"},
			want: "abc1234\t2025-01-01\tx,y\tMy Doc\tresuming the cutover",
		},
		{
			name: "empty when and tags dash",
			doc:  model.Doc{ID: "0000000aa", UpdatedAt: 1735689600, Title: "Bare"},
			want: "0000000\t2025-01-01\t-\tBare\t-",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := leanDocLine(tc.doc); got != tc.want {
				t.Fatalf("leanDocLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewDocDTO(t *testing.T) {
	anchor := model.Anchor{Kind: model.AnchorDir, Value: "internal/api"}
	d := model.Doc{
		ID:           "deadbeefcafe",
		Title:        "Auth migration handoff",
		Body:         "the long body",
		When:         "resuming the auth cutover",
		Tags:         []string{"auth"},
		Anchors:      []model.Anchor{anchor},
		Author:       "ada <ada@example.com>",
		CreatedAt:    1735689600,
		UpdatedAt:    1735689600,
		VerifiedAt:   1735689600,
		VerifiedBy:   "ada <ada@example.com>",
		Witness:      []model.AnchorWitness{{Anchor: anchor, OID: "f00ba12"}},
		SupersededBy: []model.EntityID{"newer000", "newer111"},
	}
	dto := newDocDTO(d, "STALE")

	if dto.When != "resuming the auth cutover" {
		t.Fatalf("When = %q, want the verbatim trigger", dto.When)
	}
	if dto.Drift == nil || *dto.Drift != "STALE" {
		t.Fatalf("Drift = %v, want STALE", dto.Drift)
	}
	if dto.SupersededBy == nil || *dto.SupersededBy != "newer000" {
		t.Fatalf("SupersededBy = %v, want first id newer000", dto.SupersededBy)
	}
	if len(dto.Anchors) != 1 || dto.Anchors[0].Witness == nil || *dto.Anchors[0].Witness != "f00ba12" {
		t.Fatalf("anchor witness = %+v, want oid f00ba12", dto.Anchors)
	}
	if dto.Body != "the long body" {
		t.Fatalf("Body = %q, want the long body", dto.Body)
	}

	empty := newDocDTO(model.Doc{ID: "abc1234ff"}, "")
	if empty.Drift != nil {
		t.Fatalf("Drift = %v on no-drift doc, want nil", empty.Drift)
	}
	if empty.When != "" {
		t.Fatalf("When = %q on empty doc, want empty string", empty.When)
	}
}

func TestRenderDocShow(t *testing.T) {
	d := model.Doc{
		ID:        "deadbeefcafe",
		Title:     "Auth handoff",
		When:      "resuming the cutover",
		CreatedAt: 1735689600,
		UpdatedAt: 1735689600,
		Body:      "body text",
	}
	got := renderDocShow(d, "DRIFTED", []model.EntityID{"older00"})

	if !strings.Contains(got, "title: Auth handoff\nwhen: resuming the cutover\ntags: -\n") {
		t.Fatalf("when header not directly after title:\n%s", got)
	}
	if !strings.Contains(got, "drift: DRIFTED\n") {
		t.Fatalf("drift verdict missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "\nbody text\n") {
		t.Fatalf("body not appended after a blank line:\n%s", got)
	}

	bare := renderDocShow(model.Doc{ID: "deadbeefcafe", Title: "X", CreatedAt: 1735689600, UpdatedAt: 1735689600}, "", nil)
	if !strings.Contains(bare, "when: -\n") {
		t.Fatalf("empty when not dashed:\n%s", bare)
	}
}

func TestSortDocs(t *testing.T) {
	docs := []model.Doc{
		{ID: "b", UpdatedAt: 100},
		{ID: "a", UpdatedAt: 200},
		{ID: "d", UpdatedAt: 100},
		{ID: "c", UpdatedAt: 100},
	}
	sortDocs(docs)
	got := make([]string, len(docs))
	for i, d := range docs {
		got[i] = string(d.ID)
	}
	want := []string{"a", "b", "c", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortDocs order = %v, want %v", got, want)
		}
	}
}
