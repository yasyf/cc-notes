package store

import (
	"errors"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type appendScript struct {
	name    string
	create  []model.Op
	appends [][]model.Op
}

func appendScripts() []appendScript {
	return []appendScript{
		{
			name:   "ledger",
			create: ledgerOps("merge queue"),
			appends: [][]model.Op{
				{model.UpsertRow{Key: "21052", Fields: map[string]string{"state": "failure"}, Position: "a"}},
				{model.UpsertRow{Key: "21020", Fields: map[string]string{"state": "success"}, Position: "i"}},
				{model.UpsertRow{Key: "21052", Fields: map[string]string{"state": "success", "hold_reason": "waiting on 21020"}}},
				{model.RemoveRow{Key: "21020"}},
				{
					model.UpsertRow{Key: "21020", Fields: map[string]string{"state": "queued"}},
					model.UpsertRow{Key: "21077", Fields: map[string]string{"state": "failure"}, Position: "m"},
				},
				{
					model.UpsertRow{Key: "21052", Fields: map[string]string{"state": "merged"}, Replace: true},
					model.RemoveRow{Key: "21077"},
				},
			},
		},
		{
			name:   "note",
			create: noteOps("v1"),
			appends: [][]model.Op{
				{model.SetTitle{Title: "v2"}, model.AddTag{Tag: "x"}},
				{model.AddTag{Tag: "y"}, model.SetBody{Body: "body"}},
				{model.RemoveTag{Tag: "x"}},
				{model.SetTitle{Title: "v3"}},
			},
		},
	}
}

func steppedClock() func() time.Time {
	var tick int64
	return func() time.Time {
		tick++
		return time.Unix(1_700_000_000+tick, 0).UTC()
	}
}

func wipeCache(t *testing.T, s *Store) {
	t.Helper()
	if err := os.RemoveAll(s.cache.dir); err != nil {
		t.Fatalf("wipe fold cache: %v", err)
	}
}

func coldFold(t *testing.T, s *Store, tip model.SHA) (model.Snapshot, []model.PackCommit) {
	t.Helper()
	chain, err := s.Repo.ReadChain(t.Context(), tip)
	if err != nil {
		t.Fatalf("ReadChain %s: %v", tip, err)
	}
	snap, err := fold.Strict(chain)
	if err != nil {
		t.Fatalf("Strict %s: %v", tip, err)
	}
	return snap, chain
}

func commitLamport(t *testing.T, s *Store, sha model.SHA) model.Lamport {
	t.Helper()
	chain, err := s.Repo.ReadChain(t.Context(), sha)
	if err != nil {
		t.Fatalf("ReadChain %s: %v", sha, err)
	}
	if chain[0].SHA != sha {
		t.Fatalf("ReadChain %s led with %s", sha, chain[0].SHA)
	}
	return chain[0].Pack.Lamport
}

func assertLinearLamports(t *testing.T, chain []model.PackCommit) {
	t.Helper()
	for i, c := range chain {
		if want := model.Lamport(len(chain) - i); c.Pack.Lamport != want {
			t.Errorf("lamport of %s, %d from the tip = %d, want %d", c.SHA, i, c.Pack.Lamport, want)
		}
		var want []model.SHA
		if i+1 < len(chain) {
			want = []model.SHA{chain[i+1].SHA}
		}
		if !slices.Equal(c.Parents, want) {
			t.Errorf("parents of %s = %v, want %v", c.SHA, c.Parents, want)
		}
	}
}

func TestAppendSeedsFromCachedFold(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("real")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := s.Load(ctx, ref); err != nil {
		t.Fatalf("Load: %v", err)
	}
	sentinel := note
	sentinel.Title = "sentinel"
	s.cache.put(note.Head, sentinel, 41, 0)

	appended, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "x"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := appended.(model.Note)
	if got.Title != "sentinel" {
		t.Errorf("Title = %q, want the cached %q", got.Title, "sentinel")
	}
	if want := []string{"x"}; !slices.Equal(got.Tags, want) {
		t.Errorf("Tags = %v, want %v", got.Tags, want)
	}
	if lamport := commitLamport(t, s, got.Head); lamport != 42 {
		t.Errorf("appended commit lamport = %d, want 42", lamport)
	}
	if _, top, _, ok := s.cache.getTop(got.Head); !ok || top != 42 {
		t.Errorf("cached top at %s = %d ok=%v, want 42", got.Head, top, ok)
	}
}

func TestAppendWarmMatchesColdFold(t *testing.T) {
	for _, script := range appendScripts() {
		t.Run(script.name, func(t *testing.T) {
			s := initStore(t)
			snap := create(t, s, script.create)
			ref := refs.For(snap.Meta().Kind, snap.EntityID())
			for i, ops := range script.appends {
				head := snap.Meta().Head
				if _, top, _, ok := s.cache.getTop(head); !ok || top != model.Lamport(i+1) {
					t.Fatalf("append %d: cache at %s = top %d ok=%v, want a warm entry at %d", i, head, top, ok, i+1)
				}
				appended, err := s.Append(t.Context(), ref, ops)
				if err != nil {
					t.Fatalf("append %d: %v", i, err)
				}
				want, chain := coldFold(t, s, appended.Meta().Head)
				if !reflect.DeepEqual(appended, want) {
					t.Fatalf("append %d: warm snapshot = %#v, want cold fold %#v", i, appended, want)
				}
				if len(chain) != i+2 {
					t.Fatalf("append %d: chain holds %d commits, want %d", i, len(chain), i+2)
				}
				assertLinearLamports(t, chain)
				snap = appended
			}
		})
	}
}

func TestAppendColdCacheMatchesWarmCache(t *testing.T) {
	for _, script := range appendScripts() {
		t.Run(script.name, func(t *testing.T) {
			ctx := t.Context()
			warm, cold := initStore(t), initStore(t)
			warm.now, cold.now = steppedClock(), steppedClock()
			warmSnap := create(t, warm, script.create)
			coldSnap := create(t, cold, script.create)
			if !reflect.DeepEqual(coldSnap, warmSnap) {
				t.Fatalf("creates diverged: cold %#v, warm %#v", coldSnap, warmSnap)
			}
			ref := refs.For(warmSnap.Meta().Kind, warmSnap.EntityID())
			for i, ops := range script.appends {
				wipeCache(t, cold)
				coldGot, err := cold.Append(ctx, ref, ops)
				if err != nil {
					t.Fatalf("cold append %d: %v", i, err)
				}
				warmGot, err := warm.Append(ctx, ref, ops)
				if err != nil {
					t.Fatalf("warm append %d: %v", i, err)
				}
				if !reflect.DeepEqual(coldGot, warmGot) {
					t.Fatalf("append %d: cold %#v, warm %#v", i, coldGot, warmGot)
				}
				want, chain := coldFold(t, cold, coldGot.Meta().Head)
				if !reflect.DeepEqual(coldGot, want) {
					t.Fatalf("append %d: cold-cache snapshot = %#v, want cold fold %#v", i, coldGot, want)
				}
				assertLinearLamports(t, chain)
			}
		})
	}
}

func TestAppendRacesKeepLamportsIncreasing(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "config", "core.filesRefLockTimeout", "3000")
	stores := []*Store{openStore(t, dir), openStore(t, dir)}
	note := create(t, stores[0], noteOps("raced")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := stores[1].Load(t.Context(), ref); err != nil {
		t.Fatalf("Load: %v", err)
	}

	tags := []string{"a0", "b0", "a1", "b1", "a2", "b2"}
	errs := make([]error, len(tags))
	var wg sync.WaitGroup
	for i, tag := range tags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = stores[i%len(stores)].Append(t.Context(), ref, []model.Op{model.AddTag{Tag: tag}})
		}()
	}
	wg.Wait()
	var landed []string
	for i, err := range errs {
		switch {
		case err == nil:
			landed = append(landed, tags[i])
		case errors.Is(err, ErrContended):
		default:
			t.Fatalf("append %s: %v", tags[i], err)
		}
	}
	if len(landed) == 0 {
		t.Fatal("every append exhausted its retries")
	}

	snap, chain := coldFold(t, stores[0], refTip(t, dir, ref))
	if len(chain) != len(landed)+1 {
		t.Fatalf("chain holds %d commits, want %d", len(chain), len(landed)+1)
	}
	assertLinearLamports(t, chain)
	slices.Sort(landed)
	if got := slices.Sorted(slices.Values(snap.(model.Note).Tags)); !slices.Equal(got, landed) {
		t.Errorf("cold fold tags = %v, want the landed %v", got, landed)
	}
}

func TestAppendAfterCompactUsesCheckpointFold(t *testing.T) {
	cases := []struct {
		name string
		wipe bool
	}{
		{"warm cache", false},
		{"cold cache", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			ctx := t.Context()
			ledger := create(t, s, ledgerOps("compacted")).(model.Ledger)
			ref := refs.For(model.KindLedger, ledger.ID)
			if _, err := s.Append(ctx, ref, []model.Op{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "one"}, Position: "a"}}); err != nil {
				t.Fatalf("Append before compact: %v", err)
			}
			compacted, err := s.Compact(ctx, ref)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if _, top, _, ok := s.cache.getTop(compacted.Meta().Head); !ok || top != 3 {
				t.Fatalf("cached top at checkpoint %s = %d ok=%v, want 3", compacted.Meta().Head, top, ok)
			}
			for i, ops := range [][]model.Op{
				{model.UpsertRow{Key: "b", Fields: map[string]string{"state": "two"}, Position: "i"}},
				{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "three"}}, model.RemoveRow{Key: "b"}},
			} {
				if tc.wipe {
					wipeCache(t, s)
				}
				appended, err := s.Append(ctx, ref, ops)
				if err != nil {
					t.Fatalf("append %d after compact: %v", i, err)
				}
				want, chain := coldFold(t, s, appended.Meta().Head)
				if !reflect.DeepEqual(appended, want) {
					t.Fatalf("append %d after compact = %#v, want cold fold %#v", i, appended, want)
				}
				if len(chain) != i+4 {
					t.Fatalf("append %d after compact: chain holds %d commits, want %d", i, len(chain), i+4)
				}
				assertLinearLamports(t, chain)
			}
		})
	}
}

func TestAppendCheckpointOpMatchesStrict(t *testing.T) {
	cases := []struct {
		name string
		wipe bool
	}{
		{"warm cache", false},
		{"cold cache", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			note := create(t, s, noteOps("compacted by hand")).(model.Note)
			ref := refs.For(model.KindNote, note.ID)
			if tc.wipe {
				wipeCache(t, s)
			}
			checkpoint := model.Checkpoint{EntityID: note.ID, State: note, CoversLamport: 1, CoversShas: []model.SHA{note.Head}}
			got, err := s.Append(t.Context(), ref, []model.Op{checkpoint})
			if err != nil {
				t.Fatalf("Append checkpoint: %v", err)
			}
			tip := refTip(t, s.Git.Dir, ref)
			if got.Meta().Head != tip {
				t.Fatalf("Append returned head %s, ref at %s", got.Meta().Head, tip)
			}
			want, chain := coldFold(t, s, tip)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Append = %#v, want Strict over the chain %#v", got, want)
			}
			assertLinearLamports(t, chain)
			if _, top, _, ok := s.cache.getTop(tip); !ok || top != 2 {
				t.Errorf("cached top for %s = %d (hit %v), want 2", tip, top, ok)
			}
		})
	}
}

func TestAppendColdCacheSurfacesIncompleteHistory(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("grafted")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	missing := model.SHA(strings.Repeat("beefcafe", 5))
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_000, 0)}
	stranded, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{missing}, sig, "cc-notes: add_tag", model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "stranded"}}})
	if err != nil {
		t.Fatalf("WriteOpsCommit: %v", err)
	}
	if err := s.PublishRef(ctx, ref, stranded, note.Head); err != nil {
		t.Fatalf("PublishRef: %v", err)
	}
	wipeCache(t, s)

	if _, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "x"}}); !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("Append = %v, want ErrIncompleteChain", err)
	}
	if tip := refTip(t, s.Git.Dir, ref); tip != stranded {
		t.Errorf("failed append moved %s to %s, want %s", ref, tip, stranded)
	}
}

func TestRememberPreparedCachesPreparedLamport(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("real")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	sentinel := note
	sentinel.Title = "sentinel"
	s.cache.put(note.Head, sentinel, 41, 0)

	prepared, err := s.PrepareAppendAt(ctx, ref, note.Head, []model.Op{model.AddTag{Tag: "x"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	if got := prepared.Snapshot.(model.Note).Title; got != "sentinel" {
		t.Errorf("prepared Title = %q, want the cached %q", got, "sentinel")
	}
	if lamport := commitLamport(t, s, prepared.New); lamport != 42 {
		t.Errorf("prepared commit lamport = %d, want 42", lamport)
	}
	if tip := refTip(t, s.Git.Dir, ref); tip != note.Head {
		t.Errorf("PrepareAppendAt moved %s to %s, want %s", ref, tip, note.Head)
	}

	created, err := s.PrepareCreateExact(ctx, noteOps("exact"))
	if err != nil {
		t.Fatalf("PrepareCreateExact: %v", err)
	}
	s.RememberPrepared(prepared, created)
	cached, top, _, ok := s.cache.getTop(prepared.New)
	if !ok {
		t.Fatalf("prepared tip %s missed the cache", prepared.New)
	}
	if top != 42 {
		t.Errorf("cached top at %s = %d, want 42", prepared.New, top)
	}
	if !reflect.DeepEqual(cached, prepared.Snapshot) {
		t.Errorf("cached snapshot = %#v, want %#v", cached, prepared.Snapshot)
	}
	if _, top, _, ok := s.cache.getTop(created.New); !ok || top != 1 {
		t.Errorf("cached top at created %s = %d ok=%v, want 1", created.New, top, ok)
	}
}

func peakedNote(t *testing.T, s *Store) (string, model.SHA) {
	t.Helper()
	ctx := t.Context()
	note := create(t, s, noteOps("wrapped")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_000, 0)}
	peak, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{note.Head}, sig, "cc-notes: add_tag", model.Pack{Lamport: math.MaxUint64, Ops: []model.Op{model.AddTag{Tag: "peak"}}})
	if err != nil {
		t.Fatalf("WriteOpsCommit peak: %v", err)
	}
	if err := s.PublishRef(ctx, ref, peak, note.Head); err != nil {
		t.Fatalf("PublishRef peak: %v", err)
	}
	return ref, note.Head
}

func assertWrapped(t *testing.T, s *Store, step string, tip model.SHA) {
	t.Helper()
	if lamport := commitLamport(t, s, tip); lamport != 0 {
		t.Errorf("%s: lamport of %s = %d, want 0", step, tip, lamport)
	}
	if _, top, _, ok := s.cache.getTop(tip); !ok || top != math.MaxUint64 {
		t.Errorf("%s: cached top for %s = %d (hit %v), want %d", step, tip, top, ok, uint64(math.MaxUint64))
	}
}

func TestAppendLamportWrapKeepsChainMax(t *testing.T) {
	s := initStore(t)
	ref, _ := peakedNote(t, s)
	steps := []struct {
		name string
		wipe bool
	}{
		{"cold over the peak", true},
		{"warm after the wrap", false},
		{"cold after the wrap", true},
		{"warm again", false},
	}
	for _, step := range steps {
		if step.wipe {
			wipeCache(t, s)
		}
		got, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: strings.ReplaceAll(step.name, " ", "-")}})
		if err != nil {
			t.Fatalf("%s: Append: %v", step.name, err)
		}
		tip := got.Meta().Head
		assertWrapped(t, s, step.name, tip)
		want, _ := coldFold(t, s, tip)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Append = %#v, want %#v", step.name, got, want)
		}
	}
}

func TestPrepareAppendLamportWrapKeepsChainMax(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	ref, _ := peakedNote(t, s)
	peak := refTip(t, s.Git.Dir, ref)
	prepared, err := s.PrepareAppendAt(ctx, ref, peak, []model.Op{model.AddTag{Tag: "prepared"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	if err := s.PublishRef(ctx, ref, prepared.New, peak); err != nil {
		t.Fatalf("PublishRef prepared: %v", err)
	}
	s.RememberPrepared(prepared)
	assertWrapped(t, s, "prepared", prepared.New)
	got, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "after"}})
	if err != nil {
		t.Fatalf("Append after prepared: %v", err)
	}
	assertWrapped(t, s, "append after prepared", got.Meta().Head)
}

func TestCompactLamportWrapKeepsChainMax(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	ref, _ := peakedNote(t, s)
	compacted, err := s.Compact(ctx, ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertWrapped(t, s, "compact", compacted.Meta().Head)
	got, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "after"}})
	if err != nil {
		t.Fatalf("Append after compact: %v", err)
	}
	assertWrapped(t, s, "append after compact", got.Meta().Head)
	want, _ := coldFold(t, s, got.Meta().Head)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Append after compact = %#v, want %#v", got, want)
	}
}

func TestMergeLamportWrapKeepsChainMax(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	ref, root := peakedNote(t, s)
	ours := refTip(t, s.Git.Dir, ref)
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_001, 0)}
	theirs, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{root}, sig, "cc-notes: add_tag", model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "theirs"}}})
	if err != nil {
		t.Fatalf("WriteOpsCommit theirs: %v", err)
	}
	merged, err := s.Merge(ctx, ref, ours, theirs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	assertWrapped(t, s, "merge", merged)
	got, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "after"}})
	if err != nil {
		t.Fatalf("Append after merge: %v", err)
	}
	assertWrapped(t, s, "append after merge", got.Meta().Head)
	want, _ := coldFold(t, s, got.Meta().Head)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Append after merge = %#v, want %#v", got, want)
	}
}

func publishCheckpoint(t *testing.T, s *Store, ref string, parent, old model.SHA, cp model.Checkpoint) model.SHA {
	t.Helper()
	ctx := t.Context()
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_002, 0)}
	sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{parent}, sig, "cc-notes: compact", model.Pack{Lamport: 2, Ops: []model.Op{cp}})
	if err != nil {
		t.Fatalf("WriteOpsCommit checkpoint: %v", err)
	}
	if err := s.PublishRef(ctx, ref, sha, old); err != nil {
		t.Fatalf("PublishRef checkpoint: %v", err)
	}
	return sha
}

func TestAppendPastForgedCheckpointFloorMatchesStrict(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("a")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	forged := note
	forged.Title = "forged"
	checkpoint := publishCheckpoint(t, s, ref, note.Head, note.Head, model.Checkpoint{EntityID: note.ID, State: forged, CoversLamport: 10, CoversShas: []model.SHA{note.Head}})
	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.(model.Note).Title; got != "forged" {
		t.Fatalf("Load title = %q, want the seed-safe forged State", got)
	}
	if _, top, floor, ok := s.cache.getTop(checkpoint); !ok || top != 2 || floor != 10 {
		t.Fatalf("cached top/floor for %s = %d/%d (hit %v), want 2/10", checkpoint, top, floor, ok)
	}
	for _, body := range []string{"x", "y"} {
		got, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: body}})
		if err != nil {
			t.Fatalf("Append %s: %v", body, err)
		}
		want, _ := coldFold(t, s, got.Meta().Head)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Append %s = %#v, want Strict over the chain %#v", body, got, want)
		}
		if title := got.(model.Note).Title; title != "a" {
			t.Errorf("Append %s title = %q, want the full-history fold %q", body, title, "a")
		}
	}
}

func TestAppendRefusesCachedForeignCheckpointKind(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	task := create(t, s, taskOps("rooted as a task", "main")).(model.Task)
	decoy := create(t, s, noteOps("decoy")).(model.Note)
	decoy.ID = task.ID
	ref := refs.For(model.KindNote, task.ID)
	checkpoint := publishCheckpoint(t, s, ref, task.Head, "", model.Checkpoint{EntityID: task.ID, State: decoy, CoversLamport: 1, CoversShas: []model.SHA{task.Head}})
	chain, err := s.Repo.ReadChain(ctx, checkpoint)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	listed, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("fold.Note over a note-seeded task chain: %v", err)
	}
	s.cache.putChain(checkpoint, listed, chain)
	if _, _, floor, ok := s.cache.getTop(checkpoint); !ok || floor != math.MaxUint64 {
		t.Fatalf("cached floor for %s = %d (hit %v), want %d", checkpoint, floor, ok, uint64(math.MaxUint64))
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "x"}}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("Append = %v, want Strict's ErrKindMismatch", err)
	}
	if tip := refTip(t, s.Git.Dir, ref); tip != checkpoint {
		t.Errorf("refused append moved %s to %s, want %s", ref, tip, checkpoint)
	}
}
