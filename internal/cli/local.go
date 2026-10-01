package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
)

func newLocalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Keep entities on this clone: never synced, never pushed by a plain git push",
		Long: "An entity is local when it carries the local label, or when it lacks the synced label and\n" +
			"carries a cc-notes.localLabel label (default lane-brief, raw-capture) or an attachment over\n" +
			"cc-notes.localAttachBytes (default 10 MiB) or matching a cc-notes.localAttachGlob (default\n" +
			"*.log, *.out, *.jsonl). Sync withholds local entities and their attachment content, and every\n" +
			"write keeps a negative push refspec for each one in .git/cc-notes/local-push.config.",
		Args: noUnknownSubcommand,
		RunE: runHelp,
	}
	cmd.AddCommand(newLocalListCmd(), newLocalMarkCmd(true), newLocalMarkCmd(false))
	return cmd
}

func newLocalListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the entities kept on this clone and why",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			entities, err := c.LocalEntities(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				dtos := make([]localDTO, len(entities))
				for i, e := range entities {
					dtos[i] = localDTO{ID: string(e.ID), Kind: e.Kind, Title: e.Title, Reason: e.Reason, Secluded: e.Secluded}
				}
				return printJSON(out, dtos)
			}
			for _, e := range entities {
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", shortID(e.ID), e.Kind, e.Reason, e.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newLocalMarkCmd(local bool) *cobra.Command {
	use, short := "mark ID...", "Keep entities on this clone (adds the local label)"
	if !local {
		use, short = "unmark ID...", "Publish entities again (removes the local label, adds synced)"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			for _, prefix := range args {
				kind, id, err := c.ResolveEntity(ctx, prefix)
				if err != nil {
					return err
				}
				reason, err := c.SetLocal(ctx, kind, id, local)
				if err != nil {
					return err
				}
				state := "synced"
				if reason != "" {
					state = "local: " + reason
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", shortID(id), kind, state); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

type localDTO struct {
	ID       string     `json:"id"`
	Kind     model.Kind `json:"kind"`
	Title    string     `json:"title"`
	Reason   string     `json:"reason"`
	Secluded bool       `json:"secluded"`
}
