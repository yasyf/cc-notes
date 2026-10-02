package viz

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/lifecycle"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

type sharedRepos struct {
	source  *gitRepo
	thin    *gitRepo
	records *store.Store
}

func newSharedRepos(t *testing.T) *sharedRepos {
	t.Helper()
	source := newRecentGitRepo(t)
	source.commit("base")
	thin := &gitRepo{t: t, dir: gittest.ShallowClone(t, source.dir, 1), clock: source.clock}
	return &sharedRepos{source: source, thin: thin, records: source.openStore()}
}

func (f *sharedRepos) bind(t *testing.T) {
	t.Helper()
	if _, err := store.Bind(t.Context(), f.thin.dir, f.source.dir); err != nil {
		t.Fatalf("bind thin to source: %v", err)
	}
}

func (f *sharedRepos) assertThinHoldsNoRecords(t *testing.T) {
	t.Helper()
	if out := f.thin.git("for-each-ref", refs.Namespace); out != "" {
		t.Fatalf("thin repository holds cc-notes refs:\n%s", out)
	}
}

func (f *sharedRepos) sourceEntityRefs(t *testing.T) []string {
	t.Helper()
	return strings.Fields(f.source.git("for-each-ref", "--format=%(refname)", refs.Namespace))
}

func (f *sharedRepos) replaceSource(t *testing.T) {
	t.Helper()
	if err := os.Rename(f.source.dir, f.source.dir+".replaced"); err != nil {
		t.Fatalf("move source aside: %v", err)
	}
	if err := os.Mkdir(f.source.dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", f.source.dir, err)
	}
	f.source.git("init", "-q", "-b", "main")
	f.source.git("config", "user.name", fxName)
	f.source.git("config", "user.email", fxEmail)
	f.source.commit("replacement")
}

func (f *sharedRepos) removeSource(t *testing.T) {
	t.Helper()
	if err := os.Rename(f.source.dir, f.source.dir+".gone"); err != nil {
		t.Fatalf("move source away: %v", err)
	}
}

var backendFaults = []struct {
	name      string
	openBound bool
	fault     func(*sharedRepos, *testing.T)
	want      error
}{
	{name: "backend replaced", openBound: true, fault: (*sharedRepos).replaceSource, want: store.ErrBackendReplaced},
	{name: "backend removed", openBound: true, fault: (*sharedRepos).removeSource, want: store.ErrBackendUnavailable},
	{name: "bound after open", fault: (*sharedRepos).bind, want: store.ErrBindingChanged},
}

func assertBindingFailure(t *testing.T, op string, err, want error) {
	t.Helper()
	var be *store.BindingError
	if !errors.As(err, &be) || !errors.Is(err, want) {
		t.Fatalf("%s error = %v, want a *store.BindingError wrapping %v", op, err, want)
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return resolved
}

func TestBuilderSharedStorage(t *testing.T) {
	t.Run("graph", func(t *testing.T) {
		f := newSharedRepos(t)
		f.bind(t)
		f.source.git("checkout", "-q", "-b", "source-only")
		f.source.commit("source branch work")
		f.source.git("checkout", "-q", "main")
		fork := f.thin.commit("thin fork point")
		f.thin.git("checkout", "-q", "-b", "feature")
		feature := f.thin.commit("thin feature work")
		f.thin.git("checkout", "-q", "main")
		trunk := f.thin.commit("thin trunk work")
		f.thin.git("checkout", "-q", "feature")
		note := createNote(t, f.records, "shared note")

		g, err := NewBuilder(f.thin.openStore()).Graph(t.Context(), 0)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if got := laneByName(t, g, "main").Tip.SHA; got != trunk.sha {
			t.Errorf("main tip = %s, want the thin trunk %s", got, trunk.sha)
		}
		lane := laneByName(t, g, "feature")
		if lane.Tip.SHA != feature.sha {
			t.Errorf("feature tip = %s, want %s", lane.Tip.SHA, feature.sha)
		}
		if lane.Fork == nil || lane.Fork.SHA != fork.sha || lane.Fork.Time != fork.time {
			t.Errorf("feature fork = %+v, want the thin-only commit %s at %d", lane.Fork, fork.sha, fork.time)
		}
		for _, lane := range g.Lanes {
			if strings.Contains(lane.Name, "source-only") {
				t.Errorf("lane %q comes from the source checkout's branches", lane.Name)
			}
		}
		if g.Repo.Head != "feature" {
			t.Errorf("head = %q, want the thin checkout's feature", g.Repo.Head)
		}
		if got, want := canonicalPath(t, g.Repo.Root), canonicalPath(t, f.thin.dir); got != want {
			t.Errorf("root = %q, want the thin checkout %q", got, want)
		}
		if got, want := shapesFor(g, note), []evShape{{typ: lifecycle.TypeCreated}}; !reflect.DeepEqual(got, want) {
			t.Errorf("source note events = %+v, want %+v", got, want)
		}
		f.assertThinHoldsNoRecords(t)
	})

	t.Run("digest", func(t *testing.T) {
		f := newSharedRepos(t)
		f.bind(t)
		f.thin.git("checkout", "-q", "-b", "feature")
		f.thin.git("branch", "spare")
		b := NewBuilder(f.thin.openStore())
		var note model.EntityID
		steps := []struct {
			name    string
			mutate  func()
			changed bool
		}{
			{name: "source trunk commit", mutate: func() { f.source.commit("source trunk work") }},
			{name: "source branch created", mutate: func() { f.source.git("branch", "source-only") }},
			{name: "source note created", mutate: func() { note = createNote(t, f.records, "shared note") }, changed: true},
			{name: "source note edited", mutate: func() {
				appendOps(t, f.records, refs.For(model.KindNote, note), model.SetBody{Body: "edited"})
			}, changed: true},
			{name: "thin feature commit", mutate: func() { f.thin.commit("thin feature work") }, changed: true},
			{name: "thin checkout of a same-tip branch", mutate: func() { f.thin.git("checkout", "-q", "spare") }, changed: true},
		}
		prev, err := b.digest(t.Context(), 0)
		if err != nil {
			t.Fatalf("baseline digest: %v", err)
		}
		for _, step := range steps {
			step.mutate()
			got, err := b.digest(t.Context(), 0)
			if err != nil {
				t.Fatalf("%s: digest: %v", step.name, err)
			}
			if changed := got != prev; changed != step.changed {
				t.Errorf("%s: digest changed = %v, want %v", step.name, changed, step.changed)
			}
			prev = got
		}
	})

	t.Run("commits", func(t *testing.T) {
		f := newSharedRepos(t)
		f.bind(t)
		base := model.SHA(f.thin.git("rev-parse", "HEAD"))
		trunk := f.thin.commit("thin trunk work")
		f.thin.git("checkout", "-q", "-b", "feature")
		feature := f.thin.commit("thin feature work")
		f.source.commit("source trunk work")
		ts, _, _ := newVizServer(t, f.thin)

		resp := getCommits(t, ts.URL, "")
		branches := make(map[model.SHA]string, len(resp.Commits))
		for _, c := range resp.Commits {
			if c.Branch != nil {
				branches[c.SHA] = *c.Branch
			}
		}
		want := map[model.SHA]string{
			feature.sha: "feature",
			trunk.sha:   "main",
			base:        "main",
		}
		if !reflect.DeepEqual(branches, want) {
			t.Errorf("attributed commits = %v, want the thin history %v", branches, want)
		}
	})

	t.Run("trunk probe", func(t *testing.T) {
		f := newSharedRepos(t)
		f.bind(t)
		f.thin.git("branch", "-m", "main", "master")
		f.thin.git("remote", "set-head", "origin", "-d")
		f.thin.git("checkout", "-q", "--detach")

		g, err := NewBuilder(f.thin.openStore()).Graph(t.Context(), 0)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if g.Repo.Trunk != "master" {
			t.Errorf("trunk = %q, want the thin checkout's master over the source's main", g.Repo.Trunk)
		}
	})

	for _, tc := range backendFaults {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedRepos(t)
			createNote(t, f.records, "shared note")
			if tc.openBound {
				f.bind(t)
			}
			b := NewBuilder(f.thin.openStore())
			tc.fault(f, t)

			_, err := b.digest(t.Context(), 0)
			assertBindingFailure(t, "digest", err, tc.want)
			_, err = b.entityRefs(t.Context())
			assertBindingFailure(t, "entityRefs", err, tc.want)
			_, err = b.Graph(t.Context(), 0)
			assertBindingFailure(t, "Graph", err, tc.want)
			f.assertThinHoldsNoRecords(t)
		})
	}
}

func TestWatcherSharedStorage(t *testing.T) {
	t.Run("ticks", func(t *testing.T) {
		f := newSharedRepos(t)
		f.bind(t)
		f.thin.git("checkout", "-q", "-b", "feature")
		s := f.thin.openStore()
		hub := NewHub()
		w := NewWatcher(s, NewBuilder(s), hub, time.Hour)
		ch, _ := hub.Subscribe()
		ctx := t.Context()
		if err := w.scan(ctx); err != nil {
			t.Fatalf("baseline scan: %v", err)
		}

		var note model.EntityID
		noteRef := func() string { return refs.For(model.KindNote, note) }
		steps := []struct {
			name   string
			mutate func()
			want   *refsEvent
		}{
			{name: "source trunk commit", mutate: func() { f.source.commit("source trunk work") }},
			{name: "source branch created", mutate: func() { f.source.git("branch", "source-only") }},
			{name: "source note created", mutate: func() { note = createNote(t, f.records, "shared note") }, want: &refsEvent{Heads: []string{}}},
			{name: "source note edited", mutate: func() {
				appendOps(t, f.records, noteRef(), model.SetBody{Body: "edited"})
			}, want: &refsEvent{Heads: []string{}}},
			{name: "thin feature commit", mutate: func() { f.thin.commit("thin feature work") }, want: &refsEvent{Heads: []string{"refs/heads/feature"}, Entities: []string{}}},
		}
		var gen uint64
		for _, step := range steps {
			step.mutate()
			if err := w.scan(ctx); err != nil {
				t.Fatalf("%s: scan: %v", step.name, err)
			}
			if step.want == nil {
				assertSilent(t, ch)
				continue
			}
			want := *step.want
			if want.Entities == nil {
				want.Entities = []string{noteRef()}
			}
			gen++
			want.Gen, want.Head = gen, "feature"
			if got := recvEvent(t, ch); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: event = %+v, want %+v", step.name, got, want)
			}
		}

		tips, head, err := w.snapshot(ctx)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if want := model.SHA(f.thin.git("rev-parse", "HEAD")); tips[headKey] != want {
			t.Errorf("snapshot HEAD = %s, want the thin HEAD %s", tips[headKey], want)
		}
		if head != "feature" {
			t.Errorf("snapshot head branch = %q, want feature", head)
		}
		gotEntities := withPrefix(slices.Sorted(maps.Keys(tips)), refs.Namespace)
		if want := f.sourceEntityRefs(t); !reflect.DeepEqual(gotEntities, want) {
			t.Errorf("snapshot entities = %v, want the source records %v", gotEntities, want)
		}
		if _, ok := tips["refs/heads/source-only"]; ok {
			t.Error("snapshot carries the source checkout's branch")
		}
		f.assertThinHoldsNoRecords(t)
	})

	for _, tc := range backendFaults {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedRepos(t)
			createNote(t, f.records, "shared note")
			if tc.openBound {
				f.bind(t)
			}
			s := f.thin.openStore()
			hub := NewHub()
			w := NewWatcher(s, NewBuilder(s), hub, time.Hour)
			ch, _ := hub.Subscribe()
			if err := w.scan(t.Context()); err != nil {
				t.Fatalf("baseline scan: %v", err)
			}
			tc.fault(f, t)

			assertBindingFailure(t, "scan", w.scan(t.Context()), tc.want)
			assertBindingFailure(t, "Run", w.Run(t.Context()), tc.want)
			assertSilent(t, ch)
			f.assertThinHoldsNoRecords(t)
		})
	}
}
