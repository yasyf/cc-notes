package store

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// TestRefMovesSupersedeTheReplacedTip pins supersede-on-publish: every path
// that moves a ref and caches the new tip erases the replaced tip's entry in the
// same step, so a hot entity leaves one live snapshot on disk rather than one per
// append.
func TestRefMovesSupersedeTheReplacedTip(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA)
	}{
		{"append", func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA) {
			snapshot, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "next"}})
			if err != nil {
				t.Fatal(err)
			}
			return snapshot.Meta().Head, []model.SHA{tip}
		}},
		{"compact", func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA) {
			snapshot, err := s.Compact(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			return snapshot.Meta().Head, []model.SHA{tip}
		}},
		{"merge", func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA) {
			theirs := loadParentCommit(t, s, []model.SHA{tip}, 2, model.AddTag{Tag: "theirs"})
			ours, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "ours"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadAt(t.Context(), theirs); err != nil {
				t.Fatal(err)
			}
			for _, head := range []model.SHA{ours.Meta().Head, theirs} {
				if _, hit := s.cache.get(head); !hit {
					t.Fatalf("merge input %s was not cached", head)
				}
			}
			merged, err := s.Merge(t.Context(), ref, ours.Meta().Head, theirs)
			if err != nil {
				t.Fatal(err)
			}
			return merged, []model.SHA{tip, ours.Meta().Head, theirs}
		}},
		{"remember prepared", func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA) {
			prepared, err := s.PrepareAppendAt(t.Context(), ref, tip, []model.Op{model.AddTag{Tag: "prepared"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PublishRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
				t.Fatal(err)
			}
			s.RememberPrepared(prepared)
			return prepared.New, []model.SHA{tip}
		}},
		{"load extends the cached parent", func(t *testing.T, s *Store, ref string, tip model.SHA) (model.SHA, []model.SHA) {
			prepared, err := s.PrepareAppendAt(t.Context(), ref, tip, []model.Op{model.AddTag{Tag: "published elsewhere"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PublishRef(t.Context(), ref, prepared.New, tip); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(t.Context(), ref); err != nil {
				t.Fatal(err)
			}
			return prepared.New, []model.SHA{tip}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			s.now = steppedClock()
			note := create(t, s, noteOps("superseded")).(model.Note)
			ref := refs.For(model.KindNote, note.ID)
			if _, hit := s.cache.get(note.Head); !hit {
				t.Fatal("create did not cache its tip")
			}
			replaced := cacheEntryFrame(t, &s.cache.lruDir, string(note.Head))
			head, superseded := tc.move(t, s, ref, note.Head)
			if tip, err := s.Repo.Tip(t.Context(), ref); err != nil || tip != head {
				t.Fatalf("ref tip = %s, %v; want %s", tip, err, head)
			}
			if _, hit := s.cache.get(head); !hit {
				t.Fatal("the new tip was not cached")
			}
			for _, old := range superseded {
				if _, hit := s.cache.get(old); hit {
					t.Errorf("superseded tip %s is still cached", old)
				}
			}
			replaced.assertErased(t)
			if names := s.cache.names(); len(names) != 1 || names[0] != string(head) {
				t.Fatalf("cache holds %v, want only the live tip %s", names, head)
			}
		})
	}
}
