package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestListUsesNativeRecordsNamespace(t *testing.T) {
	for _, mode := range []string{"context", "bound", "explicit routing"} {
		t.Run(mode, func(t *testing.T) {
			source := initSourceRepo(t)
			src := openStore(t, source)
			a := create(t, src, docOps("first")).(model.Doc)
			b := create(t, src, docOps("second")).(model.Doc)
			s := src
			switch mode {
			case "bound":
				f := sharedFixtureOf(t, source)
				f.bind(t)
				s = openStore(t, f.worktreeA)
			case "explicit routing":
				context := gittest.InitRepo(t)
				t.Setenv("GIT_DIR", filepath.Join(source, ".git"))
				t.Setenv("GIT_WORK_TREE", source)
				s = openStore(t, context)
			}
			pinned := s.Pinned(map[string]model.SHA{refs.For(model.KindDoc, a.ID): a.Head})
			c := create(t, src, docOps("third")).(model.Doc)
			want := map[model.EntityID]model.Doc{a.ID: a, b.ID: b, c.ID: c}
			t.Setenv("PATH", t.TempDir())
			if mode == "bound" {
				t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated.git"))
				t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "unrelated-common.git"))
			}
			view, err := s.PinnedKinds(t.Context(), model.KindDoc, model.KindAnswer, model.KindDoc)
			if err != nil {
				t.Fatal(err)
			}
			wantPins := map[string]model.SHA{
				refs.For(model.KindDoc, a.ID): a.Head,
				refs.For(model.KindDoc, b.ID): b.Head,
				refs.For(model.KindDoc, c.ID): c.Head,
			}
			if !reflect.DeepEqual(view.pins, wantPins) {
				t.Fatalf("native pinned kinds = %v, want %v", view.pins, wantPins)
			}
			got, err := s.ListDocs(t.Context(), true, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("native listing returned %d docs, want %d", len(got), len(want))
			}
			for _, doc := range got {
				if !reflect.DeepEqual(doc, want[doc.ID]) {
					t.Fatalf("native doc %s = %+v, want %+v", doc.ID, doc, want[doc.ID])
				}
			}
			got, err = pinned.ListDocs(t.Context(), true, true)
			if err != nil || !reflect.DeepEqual(got, []model.Doc{a}) {
				t.Fatalf("pinned listing = %+v, %v; want only %+v", got, err, a)
			}
		})
	}
}
