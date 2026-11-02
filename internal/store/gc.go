package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	foldCacheBudget     = 512 << 20
	relevantCacheBudget = 256 << 20
	retireAfter         = 24 * time.Hour
)

var retiredObjectIndexes = []string{"objects-v1.sqlite", "objects-v2.sqlite"}

// GCLocal tidies the derived caches and returns the number of entries it
// erased. One pass over the fold cache's hint index erases every entry whose
// name tag matches no current entity ref tip and that was written before the
// refs were read, so a snapshot cached by a concurrent append survives; then the
// fold and relevance caches are each trimmed to a byte budget, oldest write
// first. It touches no remote and folds nothing — both caches are pure
// accelerators derived from the object database, always safe to discard and
// rebuild.
func (s *Store) GCLocal(ctx context.Context) (int, error) {
	floor := s.cache.watermark()
	live, err := s.liveTips(ctx)
	if err != nil {
		return 0, err
	}
	tags := make(map[uint64]bool, len(live))
	for tip := range live {
		tags[cacheNameTag(string(tip))] = true
	}
	tidied := s.cache.sweep(func(hint cacheHint) bool { return hint.rank <= floor && !tags[hint.tag] }, foldCacheBudget)
	return tidied + s.relevant.sweep(nil, relevantCacheBudget), nil
}

// RetireLocal deletes derived state no current binary reads once it has gone a
// day untouched: the fold- and relevance-cache generations beside the current
// ones, with their lock files, and the repository's retired SQLite object
// indexes under the per-user root. A generation an older binary still writes
// keeps a fresh mtime and survives. It returns the number of generations and
// indexes deleted.
func (s *Store) RetireLocal() (int, error) {
	cutoff := time.Now().Add(-retireAfter)
	current := []string{filepath.Base(s.cache.dir), filepath.Base(s.relevant.dir)}
	retired := 0
	for _, parent := range distinct(filepath.Dir(s.cache.dir), filepath.Dir(s.relevant.dir)) {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		generations := map[string]bool{}
		for _, entry := range entries {
			if name := strings.TrimSuffix(entry.Name(), ".lock"); supersededGeneration(name, current) {
				generations[name] = true
			}
		}
		for name := range generations {
			dir := filepath.Join(parent, name)
			if retire(cutoff, dir, filepath.Join(dir, cacheHintFile), filepath.Join(dir, cacheInlineFile), dir+".lock") {
				retired++
			}
		}
	}
	for _, common := range distinct(s.commonDir, s.recordsCommonDir) {
		home, err := ccnhome.ForRepo(common)
		if err != nil {
			return retired, err
		}
		for _, index := range retiredObjectIndexes {
			path := filepath.Join(home.Dir, index)
			if retire(cutoff, path, path+"-wal", path+"-shm", path+"-journal") {
				retired++
			}
		}
	}
	return retired, nil
}

func distinct(values ...string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func supersededGeneration(name string, current []string) bool {
	return slices.ContainsFunc(current, func(generation string) bool {
		version, ok := strings.CutPrefix(name, strings.TrimRight(generation, "0123456789"))
		return ok && name != generation && version != "" && strings.Trim(version, "0123456789") == ""
	})
}

func retire(cutoff time.Time, paths ...string) bool {
	found := false
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			return false
		}
		found = true
	}
	if !found {
		return false
	}
	for _, path := range paths {
		_ = os.RemoveAll(path)
	}
	return true
}

// PruneTombstones physically deletes tombstoned note, doc, log, runbook,
// investigation, plan, and answer refs — those folded to Deleted — locally and on
// remote via git push --delete, then drops
// their now-orphaned cache entries. Superseded notes and docs and all tasks are
// never pruned: a superseded entity keeps its supersede pointer and history, and
// there is no task tombstone. Pruning is best-effort and non-convergent — a
// stale clone that never saw the delete re-advertises the ref on its next push —
// so it continues past per-ref failures, tallying pruned (both deletes
// succeeded) and failed, and never returns a per-ref error. Two exceptions
// return naming the ref deleted locally, before that ref's remote copy is
// touched: a binding changed since open, and a failed wait for the remote's
// tracking lock.
func (s *Store) PruneTombstones(ctx context.Context, remote string) (int, int, error) {
	var tally pruneTally
	notes, err := s.ListNotes(ctx, true, true)
	if err != nil {
		return 0, 0, err
	}
	for _, n := range notes {
		if !n.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindNote, n.ID), n.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	docs, err := s.ListDocs(ctx, true, true)
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, d := range docs {
		if !d.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindDoc, d.ID), d.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	logs, err := s.ListLogs(ctx, true)
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, l := range logs {
		if !l.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindLog, l.ID), l.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	runbooks, err := listOf(ctx, s, model.KindRunbook, fold.Runbook, ListOpts{IncludeDeleted: true})
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, rb := range runbooks {
		if !rb.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindRunbook, rb.ID), rb.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	ledgers, err := listOf(ctx, s, model.KindLedger, fold.Ledger, ListOpts{IncludeDeleted: true})
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, l := range ledgers {
		if !l.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindLedger, l.ID), l.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	investigations, err := listOf(ctx, s, model.KindInvestigation, fold.Investigation, ListOpts{IncludeDeleted: true})
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, inv := range investigations {
		if !inv.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindInvestigation, inv.ID), inv.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	plans, err := listOf(ctx, s, model.KindPlan, fold.Plan, ListOpts{IncludeDeleted: true})
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, p := range plans {
		if !p.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindPlan, p.ID), p.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	answers, err := s.ListAnswers(ctx, true, true)
	if err != nil {
		return tally.pruned, tally.failed, err
	}
	for _, a := range answers {
		if !a.Deleted {
			continue
		}
		if err := s.prune(ctx, remote, refs.For(model.KindAnswer, a.ID), a.Head, &tally); err != nil {
			return tally.pruned, tally.failed, err
		}
	}
	return tally.pruned, tally.failed, nil
}

type pruneTally struct{ pruned, failed int }

// prune deletes one tombstoned ref locally, rechecks the binding so a context
// rebound meanwhile fails naming the ref before its remote copy is touched,
// then deletes it on remote under the remote's tracking lock, tallying per-ref
// git failures.
func (s *Store) prune(ctx context.Context, remote, ref string, head model.SHA, tally *pruneTally) error {
	if err := s.RecordsGit.DeleteRef(ctx, ref, head); err != nil {
		tally.failed++
		return nil
	}
	s.cache.delete(head)
	if err := s.CheckRecords(); err != nil {
		return fmt.Errorf("deleted %s: %w", ref, err)
	}
	lock, err := s.TrackingLock(ctx, remote)
	if err != nil {
		return fmt.Errorf("deleted %s: %w", ref, err)
	}
	err = s.RecordsGit.Holding(lock).DeleteRemoteRef(ctx, remote, ref)
	if closeErr := lock.Close(); closeErr != nil {
		return fmt.Errorf("deleted %s: %w", ref, closeErr)
	}
	if err != nil {
		tally.failed++
		return nil
	}
	tally.pruned++
	return nil
}

// liveTips returns the set of commit shas that are the current tip of some
// entity ref, over every kind in model.Kinds(). Deriving the kind list rather
// than spelling it out is load-bearing: a kind missing here is a kind whose
// cache entries GCLocal evicts on every run, silently costing a re-fold.
func (s *Store) liveTips(ctx context.Context) (map[model.SHA]bool, error) {
	live := map[model.SHA]bool{}
	for _, kind := range model.Kinds() {
		entries, err := s.children(ctx, refs.Root(kind))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			live[e.tip] = true
		}
	}
	return live, nil
}
