package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
)

// Resolve expands an entity id prefix — matched case-insensitively against
// the lowercase hex ids in kind's namespace — into the full ref name. Ids
// are globally unique, so a task resolves regardless of its folded branch.
// No match fails with ErrNotFound; several matches fail with an
// *AmbiguousError carrying each candidate's id and title.
func (s *Store) Resolve(ctx context.Context, kind refs.Kind, prefix string) (string, error) {
	var namespace string
	switch kind {
	case refs.KindNote:
		namespace = refs.NotesPrefix
	case refs.KindTask:
		namespace = refs.TasksRoot
	case refs.KindSprint:
		namespace = refs.SprintsRoot
	case refs.KindProject:
		namespace = refs.ProjectsRoot
	default:
		return "", fmt.Errorf("resolve: unknown kind %q", kind)
	}
	entries, err := s.children(ctx, namespace)
	if err != nil {
		return "", err
	}
	lowered := strings.ToLower(prefix)
	var matches []tipEntry
	for _, e := range entries {
		if strings.HasPrefix(strings.TrimPrefix(e.ref, namespace), lowered) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: no %s matches %q", ErrNotFound, kind, prefix)
	case 1:
		return matches[0].ref, nil
	}
	slices.SortFunc(matches, func(a, b tipEntry) int { return cmp.Compare(a.ref, b.ref) })
	candidates := make([]Candidate, len(matches))
	for i, m := range matches {
		chain, err := s.Repo.ReadChain(ctx, m.tip)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", prefix, err)
		}
		snapshot, err := fold.Fold(chain)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", prefix, err)
		}
		candidates[i] = Candidate{ID: snapshot.EntityID(), Title: titleOf(snapshot)}
	}
	return "", &AmbiguousError{Kind: kind, Prefix: prefix, Candidates: candidates}
}

func titleOf(snapshot model.Snapshot) string {
	switch v := snapshot.(type) {
	case model.Note:
		return v.Title
	case model.Task:
		return v.Title
	case model.Sprint:
		return v.Title
	case model.Project:
		return v.Title
	default:
		panic(fmt.Sprintf("unknown snapshot type %T", snapshot))
	}
}
