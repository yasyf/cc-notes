package store

import (
	"context"

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
// investigation, and plan refs — those folded to Deleted — locally and on
// remote via git push --delete, then drops
// their now-orphaned cache entries. Superseded notes and docs and all tasks are
// never pruned: a superseded entity keeps its supersede pointer and history, and
// there is no task tombstone. Pruning is best-effort and non-convergent — a
// stale clone that never saw the delete re-advertises the ref on its next push —
// so it continues past per-ref failures, tallying pruned (both deletes
// succeeded) and failed, and never returns a per-ref error.
func (s *Store) PruneTombstones(ctx context.Context, remote string) (pruned, failed int, err error) {
	notes, err := s.ListNotes(ctx, true, true)
	if err != nil {
		return 0, 0, err
	}
	for _, n := range notes {
		if !n.Deleted {
			continue
		}
		ref := refs.For(model.KindNote, n.ID)
		if err := s.Git.DeleteRef(ctx, ref, n.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(n.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	docs, err := s.ListDocs(ctx, true, true)
	if err != nil {
		return pruned, failed, err
	}
	for _, d := range docs {
		if !d.Deleted {
			continue
		}
		ref := refs.For(model.KindDoc, d.ID)
		if err := s.Git.DeleteRef(ctx, ref, d.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(d.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	logs, err := s.ListLogs(ctx, true)
	if err != nil {
		return pruned, failed, err
	}
	for _, l := range logs {
		if !l.Deleted {
			continue
		}
		ref := refs.For(model.KindLog, l.ID)
		if err := s.Git.DeleteRef(ctx, ref, l.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(l.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	runbooks, err := listOf(ctx, s, model.KindRunbook, fold.Runbook, ListOpts{IncludeDeleted: true})
	if err != nil {
		return pruned, failed, err
	}
	for _, rb := range runbooks {
		if !rb.Deleted {
			continue
		}
		ref := refs.For(model.KindRunbook, rb.ID)
		if err := s.Git.DeleteRef(ctx, ref, rb.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(rb.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	investigations, err := listOf(ctx, s, model.KindInvestigation, fold.Investigation, ListOpts{IncludeDeleted: true})
	if err != nil {
		return pruned, failed, err
	}
	for _, inv := range investigations {
		if !inv.Deleted {
			continue
		}
		ref := refs.For(model.KindInvestigation, inv.ID)
		if err := s.Git.DeleteRef(ctx, ref, inv.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(inv.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	plans, err := listOf(ctx, s, model.KindPlan, fold.Plan, ListOpts{IncludeDeleted: true})
	if err != nil {
		return pruned, failed, err
	}
	for _, p := range plans {
		if !p.Deleted {
			continue
		}
		ref := refs.For(model.KindPlan, p.ID)
		if err := s.Git.DeleteRef(ctx, ref, p.Head); err != nil {
			failed++
			continue
		}
		s.cache.delete(p.Head)
		if err := s.Git.DeleteRemoteRef(ctx, remote, ref); err != nil {
			failed++
			continue
		}
		pruned++
	}
	return pruned, failed, nil
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
