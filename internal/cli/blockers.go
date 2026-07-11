package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// allTasks folds every task in the repository keyed by entity id. It backs
// the global blocker lookups and the derived blocks index.
func allTasks(ctx context.Context, s *store.Store) (map[model.EntityID]model.Task, error) {
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[model.EntityID]model.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return m, nil
}

// resolveBlocker expands a task id prefix against every task in the
// repository. It returns the task map so callers can reuse it for cycle
// checks.
func resolveBlocker(ctx context.Context, s *store.Store, prefix string) (model.EntityID, map[model.EntityID]model.Task, error) {
	live, err := allTasks(ctx, s)
	if err != nil {
		return "", nil, err
	}
	lowered := strings.ToLower(prefix)
	var matches []model.EntityID
	for id := range live {
		if strings.HasPrefix(string(id), lowered) {
			matches = append(matches, id)
		}
	}
	slices.Sort(matches)
	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("%w: no task matches %q", store.ErrNotFound, prefix)
	case 1:
		return matches[0], live, nil
	default:
		candidates := make([]store.Candidate, len(matches))
		for i, id := range matches {
			candidates[i] = store.Candidate{ID: id, Title: live[id].Title}
		}
		return "", nil, &store.AmbiguousError{Kind: model.KindTask, Prefix: prefix, Candidates: candidates}
	}
}

// blocksFor derives the reverse dependency index: the ids of live tasks
// whose blocked_by contains id, sorted.
func blocksFor(live map[model.EntityID]model.Task, id model.EntityID) []model.EntityID {
	var blocks []model.EntityID
	for _, t := range live {
		if slices.Contains(t.BlockedBy, id) {
			blocks = append(blocks, t.ID)
		}
	}
	slices.Sort(blocks)
	return blocks
}

// hasPath reports whether target is reachable from start (inclusive)
// through the blocked_by closure over live tasks.
func hasPath(live map[model.EntityID]model.Task, start, target model.EntityID) bool {
	seen := map[model.EntityID]bool{}
	stack := []model.EntityID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, live[id].BlockedBy...)
	}
	return false
}
