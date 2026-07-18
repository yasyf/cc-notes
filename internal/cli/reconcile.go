package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newReconcileCmd() *cobra.Command {
	var into string
	var from []string
	var force, dryRun, jsonOut bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Carry a merged branch's open tasks onto the target branch",
		Long: "Carry the open and in-progress tasks of each merged source branch onto the\n" +
			"target branch, then stop. A source branch counts as merged when its tip is an\n" +
			"ancestor of the target — squash and rebase merges break that test, so name the\n" +
			"source with --from --force to carry anyway. The step is idempotent: a carried\n" +
			"task is not carried again.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			intoBranch, err := resolveBranch(ctx, s, "into", into, cmd.Flags().Changed("into"))
			if err != nil {
				return err
			}
			if force && len(from) == 0 {
				return &UsageError{Err: errors.New("--force requires --from")}
			}
			fromBranches := make([]model.Branch, 0, len(from))
			for _, f := range from {
				if err := s.Git.CheckRefFormat(ctx, f); err != nil {
					return &UsageError{Err: err}
				}
				b := model.Branch(f)
				if b == intoBranch {
					return &UsageError{Err: fmt.Errorf("--from %q is the same branch as --into", f)}
				}
				fromBranches = append(fromBranches, b)
			}
			if !dryRun {
				if err := autoInstall(ctx, cmd, s.Git); err != nil {
					return err
				}
			}
			report, err := c.Reconcile(ctx, notes.ReconcileOptions{Into: intoBranch, From: fromBranches, Force: force, DryRun: dryRun})
			if err != nil {
				return err
			}
			return printReconcile(cmd, report, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&into, "into", "", "target branch (default: current branch)")
	flags.StringArrayVar(&from, "from", nil, "source branch to reconcile (repeatable; default: auto-discover)")
	flags.BoolVar(&force, "force", false, "skip the merge-ancestry test (requires --from)")
	flags.BoolVar(&dryRun, "dry-run", false, "Report what would change without writing. Reads canonical refs only: data staged by a plain fetch is folded only by a real run.")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// printReconcile writes report as its JSON DTO or its lean view: the
// verb:count tally skipping zeros, the target branch, then for each carried
// branch a header and one lean task line per carried task.
func printReconcile(cmd *cobra.Command, report notes.ReconcileReport, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		return printJSON(out, newReconcileDTO(report))
	}
	for _, line := range []struct {
		verb  string
		count int
	}{
		{"scanned", report.Scanned()},
		{"merged", report.Merged()},
		{"carried", report.Carried()},
	} {
		if line.count == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s: %d\n", line.verb, line.count); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "into: %s\n", report.Into); err != nil {
		return err
	}
	for _, b := range report.Branches {
		if !b.Merged {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s:\n", b.Branch); err != nil {
			return err
		}
		for _, t := range b.Tasks {
			if _, err := fmt.Fprintln(out, leanTaskLine(t)); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileDTO fixes the JSON field order for a reconcile report: the target
// branch, the scanned/merged/carried tallies, and one nested entry per
// scanned source branch.
type reconcileDTO struct {
	Into     string               `json:"into"`
	Scanned  int                  `json:"scanned"`
	Merged   int                  `json:"merged"`
	Carried  int                  `json:"carried"`
	Branches []reconcileBranchDTO `json:"branches"`
}

// reconcileBranchDTO is one source branch in a reconcile report: its merged
// verdict, the skip reason (empty when carried), and the full-hex ids of the
// open and in-progress tasks it carried.
type reconcileBranchDTO struct {
	Branch string   `json:"branch"`
	Merged bool     `json:"merged"`
	Reason string   `json:"reason"`
	Tasks  []string `json:"tasks"`
}

func newReconcileDTO(r notes.ReconcileReport) reconcileDTO {
	branches := make([]reconcileBranchDTO, len(r.Branches))
	for i, b := range r.Branches {
		ids := make([]string, len(b.Tasks))
		for j, t := range b.Tasks {
			ids[j] = string(t.ID)
		}
		branches[i] = reconcileBranchDTO{
			Branch: string(b.Branch),
			Merged: b.Merged,
			Reason: b.Reason,
			Tasks:  ids,
		}
	}
	return reconcileDTO{
		Into:     string(r.Into),
		Scanned:  r.Scanned(),
		Merged:   r.Merged(),
		Carried:  r.Carried(),
		Branches: branches,
	}
}
