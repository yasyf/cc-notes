package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

type blameDTO struct {
	Kind          string            `json:"kind"`
	Task          *taskDTO          `json:"task,omitempty"`
	Investigation *investigationDTO `json:"investigation,omitempty"`
}

func newBlameCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "blame SHA",
		Short: "List the tasks and investigations attributed to a commit",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			_, tasks, err := c.Blame(ctx, args[0])
			if err != nil {
				return err
			}
			_, invs, err := c.BlameInvestigations(ctx, args[0])
			if err != nil {
				return err
			}
			return printBlame(cmd, c, tasks, invs, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func printBlame(cmd *cobra.Command, c *notes.Client, tasks []model.Task, invs []model.Investigation, jsonOut bool) error {
	if jsonOut {
		blocking, err := c.TasksBlockingIndex(cmd.Context())
		if err != nil {
			return err
		}
		dtos := make([]blameDTO, 0, len(tasks)+len(invs))
		for _, task := range tasks {
			t := newTaskDTO(task, blocking[task.ID])
			dtos = append(dtos, blameDTO{Kind: string(model.KindTask), Task: &t})
		}
		for _, inv := range invs {
			infos, err := c.AttachmentInfos(cmd.Context(), inv.Attachments)
			if err != nil {
				return err
			}
			i := newInvestigationDTO(inv, attachmentInfoDTOs(infos))
			dtos = append(dtos, blameDTO{Kind: string(model.KindInvestigation), Investigation: &i})
		}
		return printJSON(cmd.OutOrStdout(), dtos)
	}
	if err := printTaskList(cmd, c, tasks, false); err != nil {
		return err
	}
	for _, inv := range invs {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), leanInvestigationLine(inv)); err != nil {
			return err
		}
	}
	return nil
}
