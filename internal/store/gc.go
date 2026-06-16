package store

import (
	"context"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
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

// PruneTombstones physically deletes tombstoned note refs — notes folded to
// Deleted — locally and on remote via git push --delete, then drops their
// now-orphaned cache entries. Superseded notes and tasks are never pruned: a
// superseded note keeps its supersede pointer and history, and there is no task
// tombstone. Pruning is best-effort and non-convergent — a stale clone that
// never saw the delete re-advertises the ref on its next push — so it continues
// past per-note failures, tallying pruned (both deletes succeeded) and failed,
// and never returns a per-note error.
func (s *Store) PruneTombstones(ctx context.Context, remote string) (pruned, failed int, err error) {
	notes, err := s.ListNotes(ctx, true, true)
	if err != nil {
		return 0, 0, err
	}
	for _, n := range notes {
		if !n.Deleted {
			continue
		}
		ref := refs.Note(n.ID)
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
	return pruned, failed, nil
}

// liveTips returns the set of commit shas that are the current tip of some
// entity ref — every note and every task.
func (s *Store) liveTips(ctx context.Context) (map[model.SHA]bool, error) {
	notes, err := s.children(ctx, refs.NotesPrefix)
	if err != nil {
		return nil, err
	}
	tasks, err := s.children(ctx, refs.TasksRoot)
	if err != nil {
		return nil, err
	}
	live := make(map[model.SHA]bool, len(notes)+len(tasks))
	for _, e := range notes {
		live[e.tip] = true
	}
	for _, e := range tasks {
		live[e.tip] = true
	}
	return live, nil
}
