package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func parseStatus(value string) (model.Status, error) {
	switch s := model.Status(value); s {
	case model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid status %q (open|in_progress|done|cancelled)", value)
	}
}

func parseTaskType(value string) (model.TaskType, error) {
	switch t := model.TaskType(value); t {
	case model.TypeTask, model.TypeBug, model.TypeEpic, model.TypeQuestion:
		return t, nil
	default:
		return "", fmt.Errorf("invalid type %q (task|bug|epic|question)", value)
	}
}

func parseSprintStatus(value string) (model.SprintStatus, error) {
	switch s := model.SprintStatus(value); s {
	case model.SprintPlanned, model.SprintActive, model.SprintCompleted, model.SprintCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid sprint status %q (planned|active|completed|cancelled)", value)
	}
}

func parseProjectStatus(value string) (model.ProjectStatus, error) {
	switch s := model.ProjectStatus(value); s {
	case model.ProjectActive, model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid project status %q (active|completed|archived|cancelled)", value)
	}
}

func validatePriority(p int) (model.Priority, error) {
	if p < 0 || p > 3 {
		return 0, fmt.Errorf("invalid priority %d (0-3)", p)
	}
	return model.Priority(p), nil
}

// parseDate parses a YYYY-MM-DD calendar date as UTC midnight into unix
// seconds. An empty value is the caller's signal to clear the date and is
// handled before calling this.
func parseDate(value string) (int64, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q (want YYYY-MM-DD): %w", value, err)
	}
	return t.UTC().Unix(), nil
}

// resolveCriterion expands a criterion id prefix — matched case-insensitively —
// against a task's criteria. No match fails with ErrNotFound; several matches
// fail with an error listing each candidate's short id and text; one match
// returns the criterion.
func resolveCriterion(task model.Task, prefix string) (model.Criterion, error) {
	lowered := strings.ToLower(prefix)
	var matches []model.Criterion
	for _, c := range task.Criteria {
		if strings.HasPrefix(strings.ToLower(c.ID), lowered) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return model.Criterion{}, fmt.Errorf("%w: no criterion matches %q", store.ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for i, c := range matches {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", c.ID[:7], c.Text)
		}
		return model.Criterion{}, fmt.Errorf("%w: criterion prefix %q matches %d: %s", store.ErrAmbiguous, prefix, len(matches), b.String())
	}
}

// loadNote resolves a note id prefix and folds its chain.
func loadNote(ctx context.Context, s *store.Store, prefix string) (string, model.Note, error) {
	ref, err := s.Resolve(ctx, model.KindNote, prefix)
	if err != nil {
		return "", model.Note{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Note{}, err
	}
	return ref, snapshot.(model.Note), nil
}

// loadDoc resolves a doc id prefix and folds its chain.
func loadDoc(ctx context.Context, s *store.Store, prefix string) (string, model.Doc, error) {
	ref, err := s.Resolve(ctx, model.KindDoc, prefix)
	if err != nil {
		return "", model.Doc{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Doc{}, err
	}
	return ref, snapshot.(model.Doc), nil
}

// loadLog resolves a log id prefix and folds its chain.
func loadLog(ctx context.Context, s *store.Store, prefix string) (string, model.Log, error) {
	ref, err := s.Resolve(ctx, model.KindLog, prefix)
	if err != nil {
		return "", model.Log{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Log{}, err
	}
	return ref, snapshot.(model.Log), nil
}

// loadTask resolves a task id prefix globally and folds its chain.
func loadTask(ctx context.Context, s *store.Store, prefix string) (string, model.Task, error) {
	ref, err := s.Resolve(ctx, model.KindTask, prefix)
	if err != nil {
		return "", model.Task{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Task{}, err
	}
	return ref, snapshot.(model.Task), nil
}

// loadSprint resolves a sprint id prefix and folds its chain.
func loadSprint(ctx context.Context, s *store.Store, prefix string) (string, model.Sprint, error) {
	ref, err := s.Resolve(ctx, model.KindSprint, prefix)
	if err != nil {
		return "", model.Sprint{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Sprint{}, err
	}
	return ref, snapshot.(model.Sprint), nil
}

// loadProject resolves a project id prefix and folds its chain.
func loadProject(ctx context.Context, s *store.Store, prefix string) (string, model.Project, error) {
	ref, err := s.Resolve(ctx, model.KindProject, prefix)
	if err != nil {
		return "", model.Project{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Project{}, err
	}
	return ref, snapshot.(model.Project), nil
}

// loadRunbook resolves a runbook id prefix and folds its chain.
func loadRunbook(ctx context.Context, s *store.Store, prefix string) (string, model.Runbook, error) {
	ref, err := s.Resolve(ctx, model.KindRunbook, prefix)
	if err != nil {
		return "", model.Runbook{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Runbook{}, err
	}
	return ref, snapshot.(model.Runbook), nil
}

// sortNotes orders notes by updated_at descending, then id ascending.
func sortNotes(notes []model.Note) {
	slices.SortFunc(notes, func(a, b model.Note) int {
		if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// sortDocs orders docs by updated_at descending, then id ascending.
func sortDocs(docs []model.Doc) {
	slices.SortFunc(docs, func(a, b model.Doc) int {
		if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// sortLogs orders logs by updated_at descending, then id ascending.
func sortLogs(logs []model.Log) {
	slices.SortFunc(logs, func(a, b model.Log) int {
		if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// sortTasks orders tasks by priority ascending, then created_at ascending,
// then id ascending.
func sortTasks(tasks []model.Task) {
	slices.SortFunc(tasks, func(a, b model.Task) int {
		if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
			return c
		}
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// hasAll reports whether have contains every element of want.
func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
