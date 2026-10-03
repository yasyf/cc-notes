package kg_test

import (
	"errors"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

type sharedFixture struct {
	source *fixture
	thin   *fixture
}

func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	source := newFixture(t)
	source.commit(t, "base", "README.md")
	return &sharedFixture{source: source, thin: &fixture{dir: gittest.ShallowClone(t, source.dir, 1)}}
}

func (f *sharedFixture) bind(t *testing.T) {
	t.Helper()
	if _, err := store.Bind(t.Context(), f.thin.dir, f.source.dir); err != nil {
		t.Fatalf("bind thin to source: %v", err)
	}
}

func (f *sharedFixture) openThin(t *testing.T) {
	t.Helper()
	s, err := store.Open(f.thin.dir)
	if err != nil {
		t.Fatalf("open thin store: %v", err)
	}
	f.thin.store = s
}

func (f *sharedFixture) assertThinHoldsNoRecords(t *testing.T) {
	t.Helper()
	if out := gittest.Git(t, f.thin.dir, "for-each-ref", refs.Namespace); out != "" {
		t.Fatalf("thin repository holds cc-notes refs:\n%s", out)
	}
}

func (f *sharedFixture) replaceSource(t *testing.T) {
	t.Helper()
	if err := os.Rename(f.source.dir, f.source.dir+".replaced"); err != nil {
		t.Fatalf("move source aside: %v", err)
	}
	if err := os.Mkdir(f.source.dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", f.source.dir, err)
	}
	gittest.Git(t, f.source.dir, "init", "-q", "-b", "main")
	gittest.Git(t, f.source.dir, "config", "user.name", "Test User")
	gittest.Git(t, f.source.dir, "config", "user.email", "test@example.com")
	f.source.commit(t, "replacement", "README.md")
}

func (f *sharedFixture) removeSource(t *testing.T) {
	t.Helper()
	if err := os.Rename(f.source.dir, f.source.dir+".gone"); err != nil {
		t.Fatalf("move source away: %v", err)
	}
}

func sourceDigest(t *testing.T, s *store.Store) string {
	t.Helper()
	digest, err := kg.SourceDigest(t.Context(), s)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	return digest
}

func TestBuildSharedStorage(t *testing.T) {
	t.Run("graph", func(t *testing.T) {
		f := newSharedFixture(t)
		sourceOnly := f.source.commit(t, "source only", "internal/source/only.go")
		thinOnly := f.thin.commit(t, "thin only", "internal/thin/only.go")
		f.bind(t)
		f.openThin(t)
		task := f.source.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "shared task", Type: model.TypeTask, Branch: "main"}).EntityID()
		f.source.append(t, model.KindTask, task, model.LinkCommit{SHA: sourceOnly}, model.LinkCommit{SHA: thinOnly})

		g := f.thin.build(t)
		taskNode := kg.EntityNode(model.KindTask, task)
		requireNode(t, g, taskNode)
		if e := requireEdge(t, g, taskNode, kg.PathNode("internal/thin/only.go"), kg.EdgeAnchor); !e.Derived {
			t.Error("the thin-only commit's path anchor is not marked derived")
		}
		if _, ok := findEdge(g, taskNode, kg.PathNode("internal/source/only.go"), kg.EdgeAnchor); ok {
			t.Error("a commit absent from the thin object database contributed a path anchor")
		}
		before := sourceDigest(t, f.thin.store)
		if g.Source != before {
			t.Errorf("Build source = %s, want SourceDigest %s", g.Source, before)
		}

		f.source.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "later", Body: "b"})
		if after := sourceDigest(t, f.thin.store); after == before {
			t.Error("a source note write left the thin source digest unchanged")
		}
		gittest.Git(t, f.thin.dir, "fetch", "-q", "origin", "main")
		fetched := sourceDigest(t, f.thin.store)
		if _, ok := findEdge(f.thin.build(t), taskNode, kg.PathNode("internal/source/only.go"), kg.EdgeAnchor); !ok {
			t.Error("the rebuilt graph carries no anchor from the commit the thin checkout fetched")
		}
		if fetched == sourceDigest(t, f.source.store) {
			t.Error("the thin source digest equals the source's own, so it ignores which commits the thin checkout holds")
		}
		f.assertThinHoldsNoRecords(t)
	})

	for _, tc := range []struct {
		name      string
		openBound bool
		fault     func(*sharedFixture, *testing.T)
		want      error
	}{
		{name: "backend replaced", openBound: true, fault: (*sharedFixture).replaceSource, want: store.ErrBackendReplaced},
		{name: "backend removed", openBound: true, fault: (*sharedFixture).removeSource, want: store.ErrBackendUnavailable},
		{name: "bound after open", fault: (*sharedFixture).bind, want: store.ErrBindingChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			f.source.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "shared", Body: "a"})
			if tc.openBound {
				f.bind(t)
			}
			f.openThin(t)
			tc.fault(f, t)

			var be *store.BindingError
			_, err := kg.SourceDigest(t.Context(), f.thin.store)
			if !errors.As(err, &be) || !errors.Is(err, tc.want) {
				t.Errorf("SourceDigest error = %v, want a *store.BindingError wrapping %v", err, tc.want)
			}
			_, err = kg.Build(t.Context(), f.thin.store)
			if !errors.As(err, &be) || !errors.Is(err, tc.want) {
				t.Errorf("Build error = %v, want a *store.BindingError wrapping %v", err, tc.want)
			}
			f.assertThinHoldsNoRecords(t)
		})
	}
}
