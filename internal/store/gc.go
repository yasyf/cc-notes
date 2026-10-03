package store

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// GCLocal tidies local-only state: it removes fold-cache entries whose tip is
// no longer the current tip of any entity ref, orphaned by appends, compaction,
// and merges. It touches no remote and folds nothing — the cache is a pure
// accelerator derived from the object database, always safe to discard and
// rebuild — and returns the number of entries removed.
func (s *Store) GCLocal(ctx context.Context) (int, error) {
	live, err := s.liveTips(ctx)
	if err != nil {
		return 0, err
	}
	tidied := 0
	for _, tip := range s.cache.tips() {
		if live[tip] {
			continue
		}
		s.cache.delete(tip)
		tidied++
	}
	return tidied, nil
}

// PruneTombstones physically deletes tombstoned note, doc, log, runbook,
// investigation, plan, and answer refs — those folded to Deleted — locally and on
// remote via git push --delete, then drops
// their now-orphaned cache entries. Superseded notes and docs and all tasks are
// never pruned: a superseded entity keeps its supersede pointer and history, and
// there is no task tombstone. Pruning is best-effort and non-convergent — a
// stale clone that never saw the delete re-advertises the ref on its next push —
// so it continues past per-ref failures, tallying pruned (both deletes
// succeeded) and failed, and never returns a per-ref error. A binding changed
// since open is the one exception: it returns naming the ref deleted locally,
// before that ref's remote copy is touched.
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
// then deletes it on remote, tallying per-ref git failures.
func (s *Store) prune(ctx context.Context, remote, ref string, head model.SHA, tally *pruneTally) error {
	if err := s.RecordsGit.DeleteRef(ctx, ref, head); err != nil {
		tally.failed++
		return nil
	}
	s.cache.delete(head)
	if err := s.CheckRecords(); err != nil {
		return fmt.Errorf("deleted %s: %w", ref, err)
	}
	if err := s.RecordsGit.DeleteRemoteRef(ctx, remote, ref); err != nil {
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
