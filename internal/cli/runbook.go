package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newRunbookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runbook",
		Short: "Runbooks: repeatable operational procedures whose runs are tracked per-execution",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newRunbookAddCmd(),
		newRunbookListCmd(),
		newRunbookShowCmd(),
		newRunbookStatusCmd("activate", model.RunbookActive),
		newRunbookStatusCmd("archive", model.RunbookArchived),
		newRunbookEditCmd(),
		newRunbookCommentCmd(),
		newRunbookHistoryCmd(),
		newRunbookStepCmd(),
		newRunbookRunCmd(),
	)
	return cmd
}

func newRunbookAddCmd() *cobra.Command {
	var body string
	var labels, steps []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a runbook, optionally with its first steps",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTitle(args[0], titleHintDesc); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			rb, reused, err := c.CreateRunbook(ctx, notes.RunbookSpec{
				Title:       args[0],
				Description: text,
				Labels:      labels,
				Steps:       steps,
			})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "runbook", rb.ID)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "runbook description; - reads stdin")
	bindLabels(flags, &labels, "label (repeatable)")
	flags.StringArrayVar(&steps, "step", nil, "initial step text, in order (repeatable)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRunbookListCmd() *cobra.Command {
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runbooks (active only unless --all)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			runbooks, err := c.Runbooks(cmd.Context(), all)
			if err != nil {
				return err
			}
			return printRunbookList(cmd, runbooks, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&all, "all", false, "include archived runbooks")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRunbookShowCmd() *cobra.Command {
	return runbookSpec.showVerb("Show one runbook with its steps and runs", showRunbook)
}

func newRunbookStatusCmd(use string, status model.RunbookStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a runbook " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			var rb model.Runbook
			switch status {
			case model.RunbookActive:
				rb, err = c.ActivateRunbook(ctx, id)
			case model.RunbookArchived:
				rb, err = c.ArchiveRunbook(ctx, id)
			}
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunbookEditCmd() *cobra.Command {
	var title, body string
	var labels labelEdits
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a runbook's title, description, or labels",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			var edit notes.RunbookEdit
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
				edit.Title = &title
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Description = &text
			}
			edit.AddLabels, edit.RemoveLabels = labels.add, labels.rm
			if runbookEditEmpty(edit) {
				return &UsageError{Err: errors.New("runbook edit requires at least one flag")}
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.EditRunbook(ctx, id, edit)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new description; - reads stdin")
	labels.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRunbookCommentCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID BODY",
		Short: "Append a comment; BODY - reads stdin",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.CommentRunbook(ctx, id, body)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunbookHistoryCmd() *cobra.Command { return kindHistoryCmd(model.KindRunbook, "runbook") }

func newRunbookStepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "step",
		Short: "Author the ordered steps of a runbook",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newStepAddCmd(),
		newStepRemoveCmd(),
		newStepEditCmd(),
		newStepMoveCmd(),
		newStepListCmd(),
	)
	return cmd
}

func newStepAddCmd() *cobra.Command {
	var command string
	var place stepPlacement
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add RUNBOOK TEXT",
		Short: "Add a step to a runbook (default --last)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := place.validate(cmd.Flags(), false); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.AddStep(ctx, id, args[1], command, place.toPlacement())
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&command, "command", "", "shell command for the step")
	place.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newStepRemoveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm RUNBOOK STEP",
		Short: "Remove a step from a runbook",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.RemoveStep(ctx, id, args[1])
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newStepEditCmd() *cobra.Command {
	var text, command string
	var noCommand, jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit RUNBOOK STEP",
		Short: "Edit a step's text or command",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if flags.Changed("command") && noCommand {
				return &UsageError{Err: errors.New("--command and --no-command are mutually exclusive")}
			}
			if !flags.Changed("text") && !flags.Changed("command") && !noCommand {
				return &UsageError{Err: errors.New("step edit requires --text, --command, or --no-command")}
			}
			var edit notes.StepEdit
			if flags.Changed("text") {
				edit.Text = &text
			}
			switch {
			case flags.Changed("command"):
				edit.Command = &command
			case noCommand:
				empty := ""
				edit.Command = &empty
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.EditStep(ctx, id, args[1], edit)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&text, "text", "", "new step text")
	flags.StringVar(&command, "command", "", "new step command")
	flags.BoolVar(&noCommand, "no-command", false, "clear the step command")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newStepMoveCmd() *cobra.Command {
	var place stepPlacement
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "move RUNBOOK STEP",
		Short: "Reorder a step within a runbook",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := place.validate(cmd.Flags(), true); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.MoveStep(ctx, id, args[1], place.toPlacement())
			if err != nil {
				if errors.Is(err, notes.ErrSelfRelative) {
					return &UsageError{Err: err}
				}
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	place.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newStepListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list RUNBOOK",
		Short: "List a runbook's steps",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.Runbook(ctx, id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, runbookStepDTOs(rb.Steps))
			}
			for i, st := range rb.Steps {
				if _, err := fmt.Fprintf(out, "%s\t%d\t%s\t%s\n", render.ShortWireID(st.ID), i+1, st.Text, orDash(st.Command)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunbookRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start and record tracked executions of a runbook",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newRunStartCmd(),
		newRunListCmd(),
		newRunShowCmd(),
		newRunStepStatusCmd("done", model.StepDone),
		newRunStepStatusCmd("skip", model.StepSkipped),
		newRunStepStatusCmd("fail", model.StepFailed),
		newRunFinishCmd(),
	)
	return cmd
}

func newRunStartCmd() *cobra.Command {
	var task string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "start RUNBOOK",
		Short: "Start a tracked run of a runbook",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.Runbook(ctx, id)
			if err != nil {
				return err
			}
			if err := notes.EnsureRunbookActive(rb); err != nil {
				return runbookErr(err)
			}
			var taskID model.EntityID
			if task != "" {
				if taskID, err = c.ResolveTask(ctx, task); err != nil {
					return err
				}
			}
			rb, err = c.StartRun(ctx, id, taskID)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&task, "task", "", "task id prefix this run serves")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRunListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list RUNBOOK",
		Short: "List a runbook's runs",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.Runbook(ctx, id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				runs := make([]runbookRunDTO, len(rb.Runs))
				for i, r := range rb.Runs {
					runs[i] = newRunbookRunDTO(rb, r)
				}
				return printJSON(out, runs)
			}
			for _, r := range rb.Runs {
				if _, err := fmt.Fprintln(out, leanRunLine(rb, r)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show RUNBOOK RUN",
		Short: "Show one run's per-step results",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.Runbook(ctx, id)
			if err != nil {
				return err
			}
			run, err := resolveRun(rb, args[1])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, newRunbookRunDTO(rb, run))
			}
			_, err = fmt.Fprint(out, renderRunShow(rb, run))
			return err
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunStepStatusCmd(use string, status model.StepResultStatus) *cobra.Command {
	var note, run string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " RUNBOOK STEP",
		Short: "Record a step " + string(status) + " in a run",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.SetRunStep(ctx, id, run, args[1], status, note)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&note, "note", "", "context note (error output, skip reason)")
	flags.StringVar(&run, "run", "", "run id prefix (default: the sole running run)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRunFinishCmd() *cobra.Command {
	var run string
	var failed, abandoned, jsonOut bool
	cmd := &cobra.Command{
		Use:   "finish RUNBOOK",
		Short: "Finish a run (default: succeeded, or failed if any step failed)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if failed && abandoned {
				return &UsageError{Err: errors.New("--failed and --abandoned are mutually exclusive")}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveRunbook(ctx, args[0])
			if err != nil {
				return err
			}
			rb, err := c.Runbook(ctx, id)
			if err != nil {
				return err
			}
			if err := notes.EnsureRunbookActive(rb); err != nil {
				return runbookErr(err)
			}
			target, err := resolveTargetRun(rb, run)
			if err != nil {
				return err
			}
			status := finishStatus(target, failed, abandoned)
			rb, err = c.FinishRun(ctx, id, target.ID, status)
			if err != nil {
				return runbookErr(err)
			}
			return printRunbook(cmd, rb, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&run, "run", "", "run id prefix (default: the sole running run)")
	flags.BoolVar(&failed, "failed", false, "finish as failed")
	flags.BoolVar(&abandoned, "abandoned", false, "finish as abandoned")
	bindJSON(flags, &jsonOut)
	return cmd
}

// runbookErr maps a notes-layer *ConflictError to the CLI's *ConflictError (its
// Error() drops the "cc-notes: " layer prefix) so runbook conflict exit codes and
// stderr bytes match the pre-migration CLI; every other error passes through.
func runbookErr(err error) error {
	var conflict *notes.ConflictError
	if errors.As(err, &conflict) {
		return &ConflictError{Msg: strings.TrimPrefix(conflict.Error(), "cc-notes: ")}
	}
	return err
}

// runbookEditEmpty reports whether a runbook edit mask sets nothing, the CLI's
// "at least one flag" guard raised as a UsageError a pinned test asserts.
func runbookEditEmpty(e notes.RunbookEdit) bool {
	return e.Title == nil && e.Description == nil && len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// finishStatus resolves a finishing run's terminal status: the explicit
// --failed/--abandoned flag, else the run's derived default (failed when any step
// failed, else succeeded).
func finishStatus(run model.RunbookRun, failed, abandoned bool) model.RunStatus {
	switch {
	case failed:
		return model.RunFailed
	case abandoned:
		return model.RunAbandoned
	}
	return notes.DerivedRunStatus(run)
}

// placementFlags are the four mutually exclusive step-placement flags.
var placementFlags = []string{"first", "last", "before", "after"}

// stepPlacement binds the --first/--last/--before/--after flags that place a
// step within a runbook's ordered steps and projects them onto a notes.Placement.
type stepPlacement struct {
	first, last   bool
	before, after string
}

func (p *stepPlacement) bind(f *pflag.FlagSet) {
	f.BoolVar(&p.first, "first", false, "place before all steps")
	f.BoolVar(&p.last, "last", false, "place after all steps (default)")
	f.StringVar(&p.before, "before", "", "place before this step (id prefix)")
	f.StringVar(&p.after, "after", "", "place after this step (id prefix)")
}

// validate enforces the placement flags' mutual exclusion; requireOne demands
// exactly one (step move), otherwise none is allowed and defaults to --last
// (step add).
func (p *stepPlacement) validate(f *pflag.FlagSet, requireOne bool) error {
	n := 0
	for _, name := range placementFlags {
		if f.Changed(name) {
			n++
		}
	}
	if n > 1 {
		return &UsageError{Err: errors.New("--first, --last, --before, and --after are mutually exclusive")}
	}
	if requireOne && n == 0 {
		return &UsageError{Err: errors.New("step move requires one of --first, --last, --before, --after")}
	}
	return nil
}

// toPlacement projects the placement flags onto a notes.Placement; no flag
// defaults to placing the step last.
func (p *stepPlacement) toPlacement() notes.Placement {
	switch {
	case p.first:
		return notes.Placement{Anchor: notes.PlaceFirst}
	case p.before != "":
		return notes.Placement{Anchor: notes.PlaceBefore, Step: p.before}
	case p.after != "":
		return notes.Placement{Anchor: notes.PlaceAfter, Step: p.after}
	default:
		return notes.Placement{Anchor: notes.PlaceLast}
	}
}

// printRunbookList writes runbooks as a JSON array of their DTOs or one lean
// line per runbook.
func printRunbookList(cmd *cobra.Command, runbooks []model.Runbook, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]runbookDTO, len(runbooks))
		for i, rb := range runbooks {
			dtos[i] = newRunbookDTO(rb)
		}
		return printJSON(out, dtos)
	}
	for _, rb := range runbooks {
		if _, err := fmt.Fprintln(out, leanRunbookLine(rb)); err != nil {
			return err
		}
	}
	return nil
}

// resolveStep resolves a step id prefix against rb, delegating to the notes-layer
// resolver so CLI candidate rendering matches the domain resolution.
func resolveStep(rb model.Runbook, prefix string) (model.RunbookStep, error) {
	return notes.ResolveStep(rb, prefix)
}

// resolveRun resolves a run id prefix against rb via the notes-layer resolver.
func resolveRun(rb model.Runbook, prefix string) (model.RunbookRun, error) {
	return notes.ResolveRun(rb, prefix)
}

// resolveTargetRun picks the run a step-status or finish verb targets via the
// notes-layer resolver, mapping its *ConflictError to the CLI's so the no-running
// exit code and stderr match.
func resolveTargetRun(rb model.Runbook, runPrefix string) (model.RunbookRun, error) {
	run, err := notes.ResolveTargetRun(rb, runPrefix)
	return run, runbookErr(err)
}
