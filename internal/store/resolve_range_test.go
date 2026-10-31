package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestResolveRangePreservesIDWidthsAndPinnedAuthority(t *testing.T) {
	s := initStore(t)
	a := create(t, s, noteOps("forty")).(model.Note)
	b := create(t, s, noteOps("sixty-four")).(model.Note)
	short := strings.Repeat("a", 40)
	long := short + strings.Repeat("b", 24)
	shortRef := refs.For(model.KindNote, model.EntityID(short))
	longRef := refs.For(model.KindNote, model.EntityID(long))
	foreignRef := refs.Root(model.KindNote) + "abcdz"
	nestedRef := refs.Root(model.KindNote) + "abcd/nested"
	for ref, tip := range map[string]model.SHA{shortRef: a.Head, longRef: b.Head, foreignRef: a.Head, nestedRef: b.Head} {
		if err := s.RecordsGit.UpdateRef(t.Context(), ref, tip, ""); err != nil {
			t.Fatal(err)
		}
	}
	pinned := s.Pinned(map[string]model.SHA{shortRef: a.Head})
	for _, tc := range []struct {
		name   string
		view   *Store
		prefix string
		want   string
	}{
		{"long", s, long, longRef},
		{"uppercase long", s, strings.ToUpper(long), longRef},
		{"pinned forty", pinned, short, shortRef},
		{"pinned short", pinned, "A", shortRef},
		{"foreign direct child", s, "abcdz", foreignRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.view.Resolve(t.Context(), model.KindNote, tc.prefix)
			if err != nil || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.prefix, got, err, tc.want)
			}
		})
	}
	for _, prefix := range []string{short, strings.ToUpper(short)} {
		_, err := s.Resolve(t.Context(), model.KindNote, prefix)
		var ambiguous *AmbiguousError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("Resolve(%q) = %v; want ambiguity", prefix, err)
		}
		want := []Candidate{{ID: a.ID, Title: a.Title}, {ID: b.ID, Title: b.Title}}
		if ambiguous.Kind != model.KindNote || ambiguous.Prefix != prefix || !reflect.DeepEqual(ambiguous.Candidates, want) {
			t.Fatalf("ambiguity = %+v; want %+v", ambiguous, want)
		}
	}
	for _, tc := range []struct {
		view   *Store
		prefix string
	}{
		{s, "abcd/"}, {s, "../"}, {s, "zzz"}, {s, long + "c"}, {pinned, long},
	} {
		if _, err := tc.view.Resolve(t.Context(), model.KindNote, tc.prefix); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Resolve(%q) = %v; want not found", tc.prefix, err)
		}
	}
	_, err := pinned.Resolve(t.Context(), model.KindNote, "")
	if err != nil {
		t.Fatalf("empty pinned prefix: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Resolve(ctx, model.KindNote, long); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Resolve = %v", err)
	}
}
