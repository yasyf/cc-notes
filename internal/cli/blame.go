package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func newBlameCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "blame SHA",
		Short: "List the task(s) a commit implemented",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			full, err := s.Git.CommitSHA(ctx, args[0])
			if errors.Is(err, gitcmd.ErrRevNotFound) {
				return fmt.Errorf("%w: no commit %s", store.ErrNotFound, args[0])
			}
			if err != nil {
				return err
			}
			all, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			seen := map[model.EntityID]bool{}
			var tasks []model.Task
			for _, t := range all {
				if slices.Contains(t.Commits, full) {
					seen[t.ID] = true
					tasks = append(tasks, t)
				}
			}
			trailers, err := s.Git.TaskTrailers(ctx, string(full))
			if err != nil {
				return err
			}
			for _, val := range trailers {
				_, task, err := taskSpec.load(ctx, s, val)
				if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrAmbiguous) {
					continue
				}
				if err != nil {
					return err
				}
				if seen[task.ID] {
					continue
				}
				seen[task.ID] = true
				tasks = append(tasks, task)
			}
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}
