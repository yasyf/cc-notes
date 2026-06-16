package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/store"
)

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"board"},
		Short:   "Orient: the backlog, your branch, in-progress across branches, and notes",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			now := time.Now()
			s, err := openStore()
			if err != nil {
				return err
			}
			ttl, err := leaseTTL(ctx, s.Git)
			if err != nil {
				return err
			}
			branch, err := s.Git.HeadBranch(ctx)
			switch {
			case errors.Is(err, gitcmd.ErrDetachedHead):
				branch = ""
			case err != nil:
				return err
			}
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			notes, err := s.ListNotes(ctx, false, false)
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			staleAfter, err := noteStaleAfter(ctx, s.Git)
			if err != nil {
				return err
			}
			reviewCount, err := noteReviewCount(ctx, s, head, now, staleAfter)
			if err != nil {
				return err
			}

			var backlog, yourBranch, inProgress []model.Task
			for _, t := range tasks {
				if t.Branch == "" && openOrInProgress(t.Status) {
					backlog = append(backlog, t)
				}
				if branch != "" && t.Branch == branch && openOrInProgress(t.Status) {
					yourBranch = append(yourBranch, t)
				}
				if t.Status == model.StatusInProgress {
					inProgress = append(inProgress, t)
				}
			}
			sortTasks(backlog)
			sortTasks(yourBranch)

			groups := map[model.Actor][]model.Task{}
			for _, t := range inProgress {
				groups[t.Assignee] = append(groups[t.Assignee], t)
			}
			assignees := make([]model.Actor, 0, len(groups))
			for a := range groups {
				assignees = append(assignees, a)
			}
			slices.Sort(assignees)
			for _, a := range assignees {
				sortTasks(groups[a])
			}

			if jsonOut {
				return printStatusJSON(cmd, s, branch, backlog, yourBranch, assignees, groups, notes, reviewCount, now, ttl)
			}
			return printStatusText(cmd, branch, backlog, yourBranch, assignees, groups, notes, reviewCount, now, ttl)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func openOrInProgress(s model.Status) bool {
	return s == model.StatusOpen || s == model.StatusInProgress
}

func printStatusText(cmd *cobra.Command, branch model.Branch, backlog, yourBranch []model.Task, assignees []model.Actor, groups map[model.Actor][]model.Task, notes []model.Note, reviewCount int, now time.Time, ttl time.Duration) error {
	var b strings.Builder
	b.WriteString("backlog\n")
	for _, t := range backlog {
		fmt.Fprintf(&b, "  %s\n", leanTaskLine(t))
	}
	if branch != "" {
		fmt.Fprintf(&b, "your branch (%s)\n", branch)
		for _, t := range yourBranch {
			fmt.Fprintf(&b, "  %s\n", leanTaskLine(t))
		}
	}
	b.WriteString("in progress across branches\n")
	for _, a := range assignees {
		for _, t := range groups[a] {
			flag := "fresh"
			if isStale(t, now, ttl) {
				flag = "STALE"
			}
			fmt.Fprintf(&b, "  %s\t%s\t%s\n", a, t.ID.Short(), flag)
		}
	}
	fmt.Fprintf(&b, "notes: %d total, %d need review\n", len(notes), reviewCount)
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

func printStatusJSON(cmd *cobra.Command, s *store.Store, branch model.Branch, backlog, yourBranch []model.Task, assignees []model.Actor, groups map[model.Actor][]model.Task, notes []model.Note, reviewCount int, now time.Time, ttl time.Duration) error {
	live, err := allTasks(cmd.Context(), s)
	if err != nil {
		return err
	}
	dto := statusDTO{
		Branch:     string(branch),
		Backlog:    taskDTOs(backlog, live),
		YourBranch: taskDTOs(yourBranch, live),
		InProgress: make([]statusAssigneeDTO, 0, len(assignees)),
		Notes:      statusNotesDTO{Total: len(notes), NeedsReview: reviewCount},
	}
	for _, a := range assignees {
		grp := groups[a]
		staleDTOs := make([]statusStaleDTO, len(grp))
		for i, t := range grp {
			staleDTOs[i] = statusStaleDTO{taskDTO: newTaskDTO(t, blocksFor(live, t.ID)), Stale: isStale(t, now, ttl)}
		}
		dto.InProgress = append(dto.InProgress, statusAssigneeDTO{Assignee: string(a), Tasks: staleDTOs})
	}
	return printJSON(cmd.OutOrStdout(), dto)
}

// taskDTOs maps tasks to their JSON DTOs, resolving the derived blocks index
// against live.
func taskDTOs(tasks []model.Task, live map[model.EntityID]model.Task) []taskDTO {
	dtos := make([]taskDTO, len(tasks))
	for i, t := range tasks {
		dtos[i] = newTaskDTO(t, blocksFor(live, t.ID))
	}
	return dtos
}
