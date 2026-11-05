package store

import (
	"errors"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/model"
)

func TestPublicationAdoptsSourceHeadAnotherAuthorityAdvanced(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("adopted source head"))
	ref := refs.For(model.KindNote, note.EntityID())
	path := managed.Path(s.recordsCommonDir)
	behind, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "written through another authority"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, behind, 0o600); err != nil {
		t.Fatal(err)
	}
	other := create(t, s, noteOps("written after the split"))
	requireManagedProjection(t, s, refs.For(model.KindNote, other.EntityID()), other)
	requireManagedProjection(t, s, ref, foreign)
	updated, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "written after adoption"}})
	if err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, ref, updated)
	head, err := s.SourceHead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tip, err := s.Repo.Tip(t.Context(), sourceindex.Ref); err != nil || tip != head.Token {
		t.Fatalf("source ref=%s, %v; managed head=%s", tip, err, head.Token)
	}
}

func TestPublicationRefusesSourceHeadThatDoesNotDescend(t *testing.T) {
	s := initStore(t)
	create(t, s, noteOps("first"))
	earlier, err := s.Repo.Tip(t.Context(), sourceindex.Ref)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, noteOps("second"))
	later, err := s.Repo.Tip(t.Context(), sourceindex.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: sourceindex.Ref, Old: later, New: earlier}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), noteOps("third")); !errors.Is(err, managed.ErrReindex) {
		t.Fatalf("publish over a rewound source head=%v; want ErrReindex", err)
	}
	if tip, err := s.Repo.Tip(t.Context(), sourceindex.Ref); err != nil || tip != earlier {
		t.Fatalf("refused publish moved the source ref to %s, %v; want %s", tip, err, earlier)
	}
}
