package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func withCheckpointInterval(t *testing.T, interval model.Lamport) {
	t.Helper()
	old := checkpointInterval
	checkpointInterval = interval
	t.Cleanup(func() { checkpointInterval = old })
}

func checkpointedLedger(t *testing.T, s *Store, rows int) (string, []model.SHA) {
	t.Helper()
	ledger := create(t, s, ledgerOps("desk")).(model.Ledger)
	ref := refs.For(model.KindLedger, ledger.ID)
	heads := make([]model.SHA, 0, 1+rows)
	heads = append(heads, ledger.Head)
	for i := range rows {
		row := model.UpsertRow{Key: fmt.Sprintf("r%d", i%7), Fields: map[string]string{"state": fmt.Sprintf("s%d", i)}, Position: fmt.Sprintf("p%d", i%5+1)}
		snap, err := s.Append(t.Context(), ref, []model.Op{row})
		if err != nil {
			t.Fatalf("Append row %d: %v", i, err)
		}
		heads = append(heads, snap.Meta().Head)
	}
	return ref, heads
}

func coldLoad(t *testing.T, s *Store, ref string) model.Snapshot {
	t.Helper()
	s.cache = newFoldCache(t.TempDir(), foldCacheCap)
	snap, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load %s: %v", ref, err)
	}
	return snap
}

func rootFold(t *testing.T, s *Store, tip model.SHA) model.Snapshot {
	t.Helper()
	chain, err := s.Repo.ReadChain(t.Context(), tip)
	if err != nil {
		t.Fatalf("ReadChain %s: %v", tip, err)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold %s: %v", tip, err)
	}
	return snap
}

func sideCommits(t *testing.T, s *Store, parent model.SHA, lamports ...model.Lamport) model.SHA {
	t.Helper()
	for i, lamport := range lamports {
		sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_000+int64(i), 0)}
		row := model.UpsertRow{Key: fmt.Sprintf("side%d", i), Fields: map[string]string{"state": "side"}, Position: "z"}
		sha, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{parent}, sig, "cc-notes: upsert_row", model.Pack{Lamport: lamport, Ops: []model.Op{row, model.RemoveRow{Key: "r1"}}})
		if err != nil {
			t.Fatalf("WriteOpsCommit side %d: %v", i, err)
		}
		parent = sha
	}
	return parent
}

func TestCheckpointSeedsAMergeWhoseSideForkedBelowIt(t *testing.T) {
	withCheckpointInterval(t, 8)
	s := initStore(t)
	ref, heads := checkpointedLedger(t, s, 12)
	coldLoad(t, s, ref)
	lags := checkpointLags()
	file, _, err := s.readCheckpoint(t.Context(), refs.CheckpointOf(ref))
	if err != nil || len(file.usable()) != 2 || file.Entries[0].Frontier != heads[12-lags[0]] || file.Entries[1].Frontier != heads[12-lags[1]] {
		t.Fatalf("checkpoint after a root fold of 13 commits = %+v, %v; want entries %v commits below the tip", file, err, lags)
	}
	covers := file.Entries[0].CoversLamport
	side := sideCommits(t, s, heads[4], covers+1, covers+2)
	main, err := s.Append(t.Context(), ref, []model.Op{model.AddComment{Body: "main"}})
	if err != nil {
		t.Fatalf("Append main: %v", err)
	}
	merged, err := s.Merge(t.Context(), ref, main.Meta().Head, side)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	seeded, _, ok, err := s.foldFromCheckpoint(t.Context(), ref, merged)
	if err != nil || !ok {
		t.Fatalf("foldFromCheckpoint over a side that forked below the frontier with every lamport above it = ok %v, %v; want a seeded fold", ok, err)
	}
	if want := rootFold(t, s, merged); !reflect.DeepEqual(seeded, want) {
		t.Fatalf("seeded fold of the merge =\n%+v\nwant the root fold\n%+v", seeded, want)
	}
	if got := coldLoad(t, s, ref); !reflect.DeepEqual(got, seeded) {
		t.Fatalf("cold Load of the merged ledger =\n%+v\nwant the seeded fold\n%+v", got, seeded)
	}
}

func TestCheckpointRefusesASideAtOrBelowItsLamport(t *testing.T) {
	withCheckpointInterval(t, 8)
	s := initStore(t)
	ref, heads := checkpointedLedger(t, s, 12)
	coldLoad(t, s, ref)
	side := sideCommits(t, s, heads[0], 2, 3)
	merged, err := s.Merge(t.Context(), ref, heads[12], side)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, _, ok, err := s.foldFromCheckpoint(t.Context(), ref, merged); err != nil || ok {
		t.Fatalf("foldFromCheckpoint over a side at lamports 2 and 3, under both entries' = ok %v, %v; want refused", ok, err)
	}
	if got, want := coldLoad(t, s, ref), rootFold(t, s, merged); !reflect.DeepEqual(got, want) {
		t.Fatalf("cold Load after a refused checkpoint =\n%+v\nwant the root fold\n%+v", got, want)
	}
}

func TestCheckpointRefusesAFrontierOutsideTheHistory(t *testing.T) {
	withCheckpointInterval(t, 8)
	s := initStore(t)
	ref, heads := checkpointedLedger(t, s, 12)
	side := sideCommits(t, s, heads[4], 6, 7, 8)
	sideState := rootFold(t, s, side)
	state, err := json.Marshal(sideState)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data, err := json.Marshal(checkpointFile{Format: checkpointFormat, Entries: []checkpointEntry{{Generation: checkpointGeneration, Frontier: side, CoversLamport: 8, StateKind: model.KindLedger, State: state}}})
	if err != nil {
		t.Fatalf("marshal file: %v", err)
	}
	sha, err := s.Repo.WriteCheckpointCommit(t.Context(), data)
	if err != nil {
		t.Fatalf("WriteCheckpointCommit: %v", err)
	}
	if err := s.RecordsGit.UpdateRef(t.Context(), refs.CheckpointOf(ref), sha, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	if _, _, ok, err := s.foldFromCheckpoint(t.Context(), ref, heads[12]); err != nil || ok {
		t.Fatalf("foldFromCheckpoint from a frontier the tip does not descend from = ok %v, %v; want refused", ok, err)
	}
	if got, want := coldLoad(t, s, ref), rootFold(t, s, heads[12]); !reflect.DeepEqual(got, want) {
		t.Fatalf("cold Load past a foreign frontier =\n%+v\nwant the root fold\n%+v", got, want)
	}
}

func TestColdFoldReadsUnderOneCheckpointIntervalAtAnyDepth(t *testing.T) {
	const interval = 10
	withCheckpointInterval(t, interval)
	decodes := map[int]int64{}
	for _, intervals := range []int{1, 50} {
		s := initStore(t)
		ref, _ := checkpointedLedger(t, s, intervals*interval)
		coldLoad(t, s, ref)
		for i := range interval/2 - interval/4 {
			if _, err := s.Append(t.Context(), ref, []model.Op{model.AddComment{Body: fmt.Sprintf("after %d", i)}}); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		s.cache = newFoldCache(t.TempDir(), foldCacheCap)
		ctx, work := gitobj.WithReadWork(t.Context())
		got, err := s.Load(ctx, ref)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		_, _, decodes[intervals] = work.Snapshot()
		if want := rootFold(t, s, got.Meta().Head); !reflect.DeepEqual(got, want) {
			t.Fatalf("cold Load over %d intervals =\n%+v\nwant the root fold\n%+v", intervals, got, want)
		}
	}
	if decodes[1] != decodes[50] || decodes[50] >= interval {
		t.Fatalf("a cold fold decoded %d commits over 1 interval of history and %d over 50, want the same count, under one interval (%d)", decodes[1], decodes[50], interval)
	}
}

func TestAdoptCheckpointsKeepsOnlyALocalOneThatCoversMore(t *testing.T) {
	s := initStore(t)
	write := func(ref, frontier string, covers model.Lamport) model.SHA {
		data, err := json.Marshal(checkpointFile{Format: checkpointFormat, Entries: []checkpointEntry{{Generation: checkpointGeneration, Frontier: model.SHA(frontier), CoversLamport: covers}}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		sha, err := s.Repo.WriteCheckpointCommit(t.Context(), data)
		if err != nil {
			t.Fatalf("WriteCheckpointCommit: %v", err)
		}
		gittest.Git(t, s.Git.Dir, "update-ref", ref, string(sha))
		return sha
	}
	cases := []struct {
		name         string
		ours, theirs model.Lamport
		absent       bool
		keepOurs     bool
	}{
		{name: "ours covers more", ours: 20, theirs: 10, keepOurs: true},
		{name: "theirs covers more", ours: 10, theirs: 20},
		{name: "a tie takes theirs", ours: 10, theirs: 10},
		{name: "none of ours", theirs: 10, absent: true},
	}
	for i, tc := range cases {
		local := fmt.Sprintf("%sledgers/%040d", refs.CheckpointNamespace, i)
		tracking := refs.CheckpointTracking("origin", local)
		var ours model.SHA
		if !tc.absent {
			ours = write(local, "ours", tc.ours)
		}
		theirs := write(tracking, "theirs", tc.theirs)
		if err := s.AdoptCheckpoints(t.Context(), map[string]string{local: tracking}); err != nil {
			t.Fatalf("%s: AdoptCheckpoints: %v", tc.name, err)
		}
		want := theirs
		if tc.keepOurs {
			want = ours
		}
		if got := model.SHA(gittest.Git(t, s.Git.Dir, "rev-parse", local)); got != want {
			t.Errorf("%s: local checkpoint = %s, want %s", tc.name, got, want)
		}
	}
}
