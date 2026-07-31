package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// defaultPlanStatuses is the in-flight set "plan list" shows without --status or
// --all: everything short of a closed plan.
var defaultPlanStatuses = []model.PlanStatus{
	model.PlanDraft,
	model.PlanApproved,
	model.PlanExecuting,
}

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plans: approved plans recorded verbatim, from draft through execution to outcome",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newPlanAddCmd(),
		newPlanListCmd(),
		newPlanShowCmd(),
		newPlanEditCmd(),
		newPlanTransitionCmd("approve", "Approve a drafted plan", (*notes.Client).ApprovePlan),
		newPlanTransitionCmd("start", "Move a plan into executing, from approved, done, or abandoned", (*notes.Client).StartPlan),
		newPlanTransitionCmd("reopen", "Resume executing a closed plan — the same move as start", (*notes.Client).StartPlan),
		newPlanCloseCmd("done", "Close a plan as done, recording its outcome", (*notes.Client).DonePlan),
		newPlanCloseCmd("abandon", "Close a plan as abandoned, recording why", (*notes.Client).AbandonPlan),
		newPlanCommentCmd(),
		newPlanSupersedeCmd(),
		newPlanSearchCmd(),
		newPlanHistoryCmd(),
		newPlanRmCmd(),
	)
	return cmd
}

func newPlanAddCmd() *cobra.Command {
	var body, bodyFile string
	var labels []string
	var anchors anchorSets
	var approved, jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE [BODY]",
		Short: "Record a plan verbatim (positional BODY, --body, --body-file, or - for stdin)",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("plan add requires a title")}
			}
			if err := validateTitle(args[0], titleHintPlan); err != nil {
				return err
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			text, err := planBody(cmd, body, bodyFile, pos, len(args) > 1)
			if err != nil {
				return err
			}
			status := model.PlanDraft
			if approved {
				status = model.PlanApproved
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			if anchors.commits, err = resolveCommits(ctx, s.Git, anchors.commits); err != nil {
				return err
			}
			plan, reused, err := c.CreatePlan(ctx, notes.PlanSpec{
				Title:   args[0],
				Body:    text,
				Status:  status,
				Labels:  labels,
				Anchors: anchorSetsSpec(anchors),
			})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "plan", plan.ID)
			}
			return printPlan(cmd, c, plan, jsonOut, writeAck{Reused: reused})
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "the plan text, verbatim; - reads stdin")
	flags.StringVar(&bodyFile, "body-file", "", "read the plan text from this file")
	flags.BoolVar(&approved, "approved", false, "record the plan already approved instead of draft")
	bindLabels(flags, &labels, "label (repeatable)")
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	cmd.MarkFlagsMutuallyExclusive("body", "body-file")
	return cmd
}

// planBody resolves a plan's body from exactly one of --body-file, the
// positional BODY, --body, or stdin, and requires the result to be non-empty —
// a plan is its body. --body and --body-file are mutually exclusive at the flag
// layer; the positional collision is caught here, since cobra groups only flags.
func planBody(cmd *cobra.Command, flagVal, file, pos string, posGiven bool) (string, error) {
	if file == "" {
		return freeText(cmd, "body", flagVal, pos, posGiven, true)
	}
	if posGiven {
		return "", &UsageError{Err: fmt.Errorf("%s takes the plan text from exactly one of a positional argument, --body, --body-file, or - for stdin", cmd.CommandPath())}
	}
	//nolint:gosec // G304: path is the operator-supplied plan file for this CLI; reading it is the intended behavior.
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read body file %s: %w", file, err)
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return "", &UsageError{Err: fmt.Errorf("%s: body file %s is empty — a plan is its body", cmd.CommandPath(), file)}
	}
	return text, nil
}

func newPlanListCmd() *cobra.Command {
	var statusCSV string
	var labels []string
	var filters anchorFilters
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List in-flight plans (draft, approved, or executing unless --all)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			statuses := defaultPlanStatuses
			switch {
			case all:
				statuses = nil
			case cmd.Flags().Changed("status"):
				statuses = nil
				for _, part := range strings.Split(statusCSV, ",") {
					status, err := parsePlanStatus(part)
					if err != nil {
						return err
					}
					statuses = append(statuses, status)
				}
			}
			plans, err := c.Plans(cmd.Context(), notes.PlanFilter{
				Statuses: statuses,
				Labels:   labels,
				Anchors:  anchorFiltersToNotes(filters),
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, plans, jsonOut, planListDTO, leanPlanLine)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&statusCSV, "status", "", "status filter, comma-separated (default draft,approved,executing)")
	flags.BoolVar(&all, "all", false, "every status")
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	cmd.MarkFlagsMutuallyExclusive("all", "status")
	return cmd
}

func newPlanShowCmd() *cobra.Command {
	return planSpec.showVerb("Show one plan with its recorded text, outcome, and tasks", showPlan)
}

// showPlan renders one plan with the task roll-up its upward pointers invert.
func showPlan(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, plan, err := planSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	tasks, err := c.PlanTasks(ctx, plan.ID)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newPlanDTO(plan, tasks))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderPlanShow(plan, tasks))
	return err
}

func newPlanEditCmd() *cobra.Command {
	var title, body, outcome string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a plan's title, recorded text, outcome, labels, or anchors",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			var edit notes.PlanEdit
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintPlan); err != nil {
					return err
				}
				edit.Title = &title
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Body = &text
			}
			if flags.Changed("outcome") {
				text, err := bodyArg(cmd, outcome)
				if err != nil {
					return err
				}
				edit.Outcome = &text
			}
			edit.AddLabels, edit.RemoveLabels = labels.add, labels.rm
			edit.AddAnchors = notes.AnchorSpec{Commits: anchors.addCommits, Paths: anchors.addPaths, Dirs: anchors.addDirs, Branches: anchors.addBranches}
			edit.RemoveAnchors = notes.AnchorSpec{Commits: anchors.rmCommits, Paths: anchors.rmPaths, Dirs: anchors.rmDirs, Branches: anchors.rmBranches}
			if planEditEmpty(edit) {
				return &UsageError{Err: errors.New("plan edit requires at least one flag")}
			}
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolvePlan(ctx, args[0])
			if err != nil {
				return err
			}
			plan, err := c.EditPlan(ctx, id, edit)
			if err != nil {
				return err
			}
			return printPlan(cmd, c, plan, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new plan text, verbatim; - reads stdin")
	flags.StringVar(&outcome, "outcome", "", "what executing the plan produced; - reads stdin")
	labels.bind(flags)
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

// newPlanTransitionCmd builds an outcome-free lifecycle verb — approve, start,
// or reopen — over the notes-client method that moves the plan.
func newPlanTransitionCmd(use, short string, transition func(*notes.Client, context.Context, model.EntityID) (model.Plan, error)) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlanTransition(cmd, args[0], jsonOut, transition)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// newPlanCloseCmd builds a terminal verb — done or abandon — whose optional
// --outcome records what executing the plan produced, written in the same pack
// commit as the status.
func newPlanCloseCmd(use, short string, closeVerb func(*notes.Client, context.Context, model.EntityID, string) (model.Plan, error)) *cobra.Command {
	var outcome string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := bodyArg(cmd, outcome)
			if err != nil {
				return err
			}
			return runPlanTransition(cmd, args[0], jsonOut, func(c *notes.Client, ctx context.Context, id model.EntityID) (model.Plan, error) {
				return closeVerb(c, ctx, id, text)
			})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&outcome, "outcome", "", "what executing the plan produced; - reads stdin")
	bindJSON(flags, &jsonOut)
	return cmd
}

// runPlanTransition is the shared body of every plan lifecycle verb: resolve the
// id, apply the transition, and echo the acknowledgement.
func runPlanTransition(cmd *cobra.Command, prefix string, jsonOut bool, transition func(*notes.Client, context.Context, model.EntityID) (model.Plan, error)) error {
	ctx := cmd.Context()
	s, c, err := openStoreClient(cmd)
	if err != nil {
		return err
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return err
	}
	id, err := c.ResolvePlan(ctx, prefix)
	if err != nil {
		return err
	}
	plan, err := transition(c, ctx, id)
	if err != nil {
		return err
	}
	return printPlan(cmd, c, plan, jsonOut)
}

func newPlanCommentCmd() *cobra.Command {
	var body string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID [BODY]",
		Short: "Append a comment (positional BODY, --body, or - for stdin)",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("plan comment requires a plan ID")}
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			text, err := freeText(cmd, "body", body, pos, len(args) > 1, true)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolvePlan(ctx, args[0])
			if err != nil {
				return err
			}
			plan, err := c.CommentPlan(ctx, id, text)
			if err != nil {
				return err
			}
			return printPlan(cmd, c, plan, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "comment body; - reads stdin")
	bindJSON(flags, &jsonOut)
	return cmd
}

// newPlanSupersedeCmd builds "plan supersede OLD --by NEW": a replan approved
// once execution has started replaces the old plan through an edge, never a
// status. A revision re-approved before work starts is a body edit instead.
func newPlanSupersedeCmd() *cobra.Command {
	var by string
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "supersede OLD --by NEW",
		Short: "Record that plan NEW replaces OLD (--clear undoes the edge)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if by == "" {
				return &UsageError{Err: errors.New("plan supersede requires --by NEW")}
			}
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolvePlan(ctx, args[0])
			if err != nil {
				return err
			}
			byID, err := c.ResolvePlan(ctx, by)
			if err != nil {
				return err
			}
			write := c.SupersedePlan
			if clearFlag {
				write = c.UnsupersedePlan
			}
			plan, err := write(ctx, id, byID)
			if err != nil {
				return err
			}
			return printPlan(cmd, c, plan, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&by, "by", "", "the plan that replaces OLD")
	flags.BoolVar(&clearFlag, "clear", false, "remove the supersede edge instead of adding it")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newPlanSearchCmd() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across plan titles, recorded text, and outcomes",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			kindLimit := limit
			if kindLimit == 0 {
				kindLimit = -1
			}
			plans, err := c.SearchPlans(cmd.Context(), args[0], notes.SearchFilter{
				Labels:  labels,
				Author:  author,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   kindLimit,
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, plans, jsonOut, planListDTO, leanPlanLine)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	bindLimit(flags, &limit, 20)
	flags.StringVar(&author, "author", "", "require author")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newPlanHistoryCmd() *cobra.Command { return kindHistoryCmd(model.KindPlan, "plan") }

func newPlanRmCmd() *cobra.Command {
	return planSpec.rmCmd("Tombstone a plan", (*notes.Client).ResolvePlan, (*notes.Client).RemovePlan)
}

func planEditEmpty(edit notes.PlanEdit) bool {
	return edit.Title == nil && edit.Body == nil && edit.Outcome == nil &&
		len(edit.AddLabels) == 0 && len(edit.RemoveLabels) == 0 &&
		anchorSpecEmpty(edit.AddAnchors) && anchorSpecEmpty(edit.RemoveAnchors)
}

func planListDTO(p model.Plan) any {
	return newPlanSummaryDTO(p)
}
