package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type listProbe struct {
	s     *Store
	calls int
}

func (p *listProbe) list(t *testing.T, variant string) []model.Answer {
	t.Helper()
	got, err := CachedList(t.Context(), p.s, model.KindAnswer, []byte(variant), func(pinned *Store) ([]model.Answer, error) {
		p.calls++
		return pinned.ListAnswers(t.Context(), false, false)
	})
	if err != nil {
		t.Fatalf("CachedList(%s): %v", variant, err)
	}
	return got
}

func (p *listProbe) expect(t *testing.T, variant string, calls int, want ...model.EntityID) {
	t.Helper()
	got := p.list(t, variant)
	if p.calls != calls {
		t.Fatalf("compute ran %d times, want %d", p.calls, calls)
	}
	ids := make([]model.EntityID, len(got))
	for i, a := range got {
		ids[i] = a.ID
	}
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("listed %v, want %v", ids, want)
	}
	fresh, err := p.s.ListAnswers(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListAnswers: %v", err)
	}
	if gotJSON, freshJSON := mustJSON(t, got), mustJSON(t, fresh); gotJSON != freshJSON {
		t.Fatalf("CachedList =\n%s\nwant the uncached listing\n%s", gotJSON, freshJSON)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func answerRef(id model.EntityID) string { return refs.For(model.KindAnswer, id) }

func removeRef(t *testing.T, s *Store, ref string) {
	t.Helper()
	tips, err := s.RecordsGit.Refs(t.Context(), refs.Root(model.KindAnswer))
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: ref, Old: tips[ref]}}); err != nil {
		t.Fatalf("delete %s: %v", ref, err)
	}
}

func appendOps(t *testing.T, s *Store, ref string, ops ...model.Op) {
	t.Helper()
	if _, err := s.Append(t.Context(), ref, ops); err != nil {
		t.Fatalf("Append %s: %v", ref, err)
	}
}

func listEntries(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := os.ReadDir(s.relevant.dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	return len(slices.DeleteFunc(entries, func(e os.DirEntry) bool { return !strings.HasPrefix(e.Name(), "list-") }))
}

func TestCachedListFollowsEveryTipChange(t *testing.T) {
	s := initStore(t)
	p := &listProbe{s: s}
	step := func(calls int, want ...model.EntityID) {
		t.Helper()
		p.expect(t, "all", calls, want...)
		p.expect(t, "all", calls, want...)
		if n := listEntries(t, s); n != 1 {
			t.Fatalf("cache holds %d list entries for one variant, want the one entry rewritten in place", n)
		}
	}
	step(1)

	a := create(t, s, answerOps("a")).(model.Answer)
	b := create(t, s, answerOps("b")).(model.Answer)
	step(2, a.ID, b.ID)

	appendOps(t, s, answerRef(a.ID), model.SetBody{Body: "Memcached"})
	step(3, a.ID, b.ID)
	if got := p.list(t, "all"); p.calls != 3 || !slices.ContainsFunc(got, func(x model.Answer) bool { return x.ID == a.ID && x.Body == "Memcached" }) {
		t.Fatalf("warm listing after an edit = %+v (compute ran %d times), want the edited body served without a refold", got, p.calls)
	}

	c := create(t, s, answerOps("c")).(model.Answer)
	step(4, a.ID, b.ID, c.ID)

	appendOps(t, s, answerRef(b.ID), model.DeleteNote{})
	step(5, a.ID, c.ID)

	appendOps(t, s, answerRef(c.ID), model.AddSupersededBy{ID: a.ID})
	step(6, a.ID)

	removeRef(t, s, answerRef(a.ID))
	step(7)
	if got := p.list(t, "all"); got == nil {
		t.Fatal("warm empty listing decoded to nil, want an empty slice")
	}
}

func TestCachedListKeysEveryVariant(t *testing.T) {
	s := initStore(t)
	p := &listProbe{s: s}
	a := create(t, s, answerOps("a")).(model.Answer)
	p.expect(t, `{"Labels":["x"]}`, 1, a.ID)
	p.expect(t, `{"Labels":["y"]}`, 2, a.ID)
	p.expect(t, `{"Labels":["x"]}`, 2, a.ID)
	p.expect(t, `{"Labels":["y"]}`, 2, a.ID)
	if n := listEntries(t, s); n != 2 {
		t.Fatalf("cache holds %d list entries for two variants, want 2", n)
	}
}

func TestCachedListServesOnlyItsOwnKey(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	a := create(t, s, answerOps("a")).(model.Answer)
	pinned, err := s.PinnedKinds(ctx, model.KindAnswer)
	if err != nil {
		t.Fatalf("PinnedKinds: %v", err)
	}
	identity, err := executableIdentity()
	if err != nil {
		t.Fatalf("executableIdentity: %v", err)
	}
	elsewhere := pinned.Pinned(pinned.pins)
	elsewhere.recordsCommonDir = t.TempDir()
	variant := []byte("all")
	tips := pinned.listTips(model.KindAnswer)
	planted := []byte(tips + "\n" + mustJSON(t, []model.Answer{{ID: "planted", Title: "planted"}}))
	own := pinned.listCacheName(model.KindAnswer, variant, foldCacheGeneration, identity)
	foreign := map[string]string{
		"fold generation":    pinned.listCacheName(model.KindAnswer, variant, foldGeneration([]string{"older_op"}), identity),
		"binary":             pinned.listCacheName(model.KindAnswer, variant, foldCacheGeneration, identity+" rebuilt"),
		"records repository": elsewhere.listCacheName(model.KindAnswer, variant, foldCacheGeneration, identity),
		"variant":            pinned.listCacheName(model.KindAnswer, []byte("other"), foldCacheGeneration, identity),
		"kind":               pinned.listCacheName(model.KindNote, variant, foldCacheGeneration, identity),
	}
	for why, name := range foreign {
		if name == own {
			t.Fatalf("a different %s produced the same key %s", why, name)
		}
		s.relevant.write(name, planted)
	}
	p := &listProbe{s: s}
	p.expect(t, "all", 1, a.ID)

	s.relevant.write(own, planted)
	if got := p.list(t, "all"); p.calls != 1 || len(got) != 1 || got[0].ID != "planted" {
		t.Fatalf("listing under its own key and tips = %+v (compute ran %d times), want the stored entry served", got, p.calls)
	}

	s.relevant.write(own, []byte(strings.Repeat("0", len(tips))+"\n"+mustJSON(t, []model.Answer{{ID: "planted"}})))
	p.expect(t, "all", 2, a.ID)

	s.relevant.write(own, []byte(tips+"\n[{"))
	p.expect(t, "all", 3, a.ID)

	s.relevant.write(own, []byte(mustJSON(t, []model.Answer{{ID: "planted"}})))
	p.expect(t, "all", 4, a.ID)
}

func TestCachedListNeverStoresPartialFolds(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	a := create(t, s, answerOps("a")).(model.Answer)
	ref := answerRef(a.ID)
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	sig, _, err := s.signature(ctx)
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	pack := model.Pack{Lamport: 2, Session: session(), Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}}
	sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, "cc-notes: set_status", pack)
	if err != nil {
		t.Fatalf("WriteOpsCommit: %v", err)
	}
	if err := s.RecordsGit.UpdateRef(ctx, ref, sha, tip); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	p := &listProbe{s: s}
	for calls := 1; calls <= 2; calls++ {
		got := p.list(t, "all")
		if p.calls != calls || len(got) != 1 || got[0].SkippedOps != 1 {
			t.Fatalf("listing %d = %+v (compute ran %d times), want a fresh fold reporting 1 skipped op", calls, got, p.calls)
		}
	}
}

func TestCachedListReturnsComputeErrors(t *testing.T) {
	s := initStore(t)
	create(t, s, answerOps("a"))
	boom := errors.New("boom")
	calls := 0
	for range 2 {
		_, err := CachedList(t.Context(), s, model.KindAnswer, []byte("all"), func(*Store) ([]model.Answer, error) {
			calls++
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("CachedList = %v, want %v", err, boom)
		}
	}
	if calls != 2 {
		t.Fatalf("compute ran %d times, want 2", calls)
	}
}

func TestCachedListChecksTheBindingBeforeAHit(t *testing.T) {
	refuse := func(t *testing.T) func(*Store) ([]model.Answer, error) {
		return func(*Store) ([]model.Answer, error) {
			t.Fatal("compute ran over a records backend that failed its check")
			return nil, nil
		}
	}
	rows := []struct {
		name     string
		mutate   func(t *testing.T, f *sharedFixture)
		sentinel error
	}{
		{
			name: "remove backend",
			mutate: func(t *testing.T, f *sharedFixture) {
				if err := os.RemoveAll(f.source); err != nil {
					t.Fatalf("remove source: %v", err)
				}
			},
			sentinel: ErrBackendUnavailable,
		},
		{
			name:     "replace backend",
			mutate:   func(t *testing.T, f *sharedFixture) { replaceRepo(t, f.source) },
			sentinel: ErrBackendReplaced,
		},
		{
			name: "rebind to another backend",
			mutate: func(t *testing.T, f *sharedFixture) {
				setBindingValue(t, f.config(), bindingFor(t, filepath.Join(initSourceRepo(t), ".git")).String())
			},
			sentinel: ErrBindingChanged,
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			f.bind(t)
			s, err := Open(f.thin)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			a := create(t, s, answerOps("a")).(model.Answer)
			p := &listProbe{s: s}
			p.expect(t, "all", 1, a.ID)
			p.expect(t, "all", 1, a.ID)

			tc.mutate(t, f)
			if _, err := CachedList(t.Context(), s, model.KindAnswer, []byte("all"), refuse(t)); !errors.Is(err, tc.sentinel) {
				t.Fatalf("warm CachedList = %v, want %v", err, tc.sentinel)
			}
		})
	}

	t.Run("a reopened store over a new backend lists that backend", func(t *testing.T) {
		f := newSharedFixture(t)
		f.bind(t)
		s, err := Open(f.thin)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		a := create(t, s, answerOps("a")).(model.Answer)
		(&listProbe{s: s}).expect(t, "all", 1, a.ID)

		setBindingValue(t, f.config(), bindingFor(t, filepath.Join(initSourceRepo(t), ".git")).String())
		reopened, err := Open(f.thin)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		p := &listProbe{s: reopened}
		p.expect(t, "all", 1)
		p.expect(t, "all", 1)
	})
}
