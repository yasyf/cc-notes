package store

import (
	"context"

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

// PruneTombstones physically deletes tombstoned note, doc, and log refs — those
// folded to Deleted — locally and on remote via git push --delete, then drops
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
	docs, err := s.ListDocs(ctx, true, true)
	if err != nil {
		return pruned, failed, err
	}
	for _, d := range docs {
		if !d.Deleted {
			continue
		}
		ref := refs.Doc(d.ID)
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
		ref := refs.Log(l.ID)
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
	return pruned, failed, nil
}

// liveTips returns the set of commit shas that are the current tip of some
// entity ref — every note, every task, every doc, and every log.
func (s *Store) liveTips(ctx context.Context) (map[model.SHA]bool, error) {
	notes, err := s.children(ctx, refs.NotesPrefix)
	if err != nil {
		return nil, err
	}
	tasks, err := s.children(ctx, refs.TasksRoot)
	if err != nil {
		return nil, err
	}
	docs, err := s.children(ctx, refs.DocsRoot)
	if err != nil {
		return nil, err
	}
	logs, err := s.children(ctx, refs.LogsRoot)
	if err != nil {
		return nil, err
	}
	live := make(map[model.SHA]bool, len(notes)+len(tasks)+len(docs)+len(logs))
	for _, e := range notes {
		live[e.tip] = true
	}
	for _, e := range tasks {
		live[e.tip] = true
	}
	for _, e := range docs {
		live[e.tip] = true
	}
	for _, e := range logs {
		live[e.tip] = true
	}
	return live, nil
}
