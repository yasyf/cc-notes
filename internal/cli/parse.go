package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// parseEnum validates value against the ordered set of accepted values for a
// string enum, returning it typed or an "invalid <label> %q (a|b|c)" error
// whose pipe-list is the accepted values in declaration order.
func parseEnum[S ~string](value, label string, valid []S) (S, error) {
	if v := S(value); slices.Contains(valid, v) {
		return v, nil
	}
	names := make([]string, len(valid))
	for i, v := range valid {
		names[i] = string(v)
	}
	var zero S
	return zero, fmt.Errorf("invalid %s %q (%s)", label, value, strings.Join(names, "|"))
}

func parseStatus(value string) (model.Status, error) {
	return parseEnum(value, "status", []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled})
}

func parseTaskType(value string) (model.TaskType, error) {
	return parseEnum(value, "type", []model.TaskType{model.TypeTask, model.TypeBug, model.TypeEpic, model.TypeQuestion})
}

func parseSprintStatus(value string) (model.SprintStatus, error) {
	return parseEnum(value, "sprint status", []model.SprintStatus{model.SprintPlanned, model.SprintActive, model.SprintCompleted, model.SprintCancelled})
}

func parseProjectStatus(value string) (model.ProjectStatus, error) {
	return parseEnum(value, "project status", []model.ProjectStatus{model.ProjectActive, model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled})
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

// sortByUpdated orders any snapshot slice by updated_at descending, then id
// ascending, reading both through the kind-agnostic Meta header.
func sortByUpdated[T model.Snapshot](items []T) {
	slices.SortFunc(items, func(a, b T) int {
		if c := b.Meta().UpdatedAt.Compare(a.Meta().UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.EntityID(), b.EntityID())
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
