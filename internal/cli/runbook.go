package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
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
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			ops := []model.Op{model.CreateRunbook{
				Nonce:       model.NewNonce(),
				Title:       args[0],
				Description: text,
				Labels:      labels,
			}}
			last := ""
			for _, stepText := range steps {
				pos := model.PositionBetween(last, "")
				ops = append(ops, model.AddStep{ID: model.NewNonce(), Text: stepText, Position: pos})
				last = pos
			}
			snapshot, err := createEntity(ctx, cmd, s, ops)
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			runbooks, err := s.ListRunbooks(ctx)
			if err != nil {
				return err
			}
			if !all {
				runbooks = slices.DeleteFunc(runbooks, func(rb model.Runbook) bool {
					return rb.Status != model.RunbookActive
				})
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one runbook with its steps and runs",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return showRunbook(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunbookStatusCmd(use string, status model.RunbookStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a runbook " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if rb.Status == status {
				return &ConflictError{Msg: fmt.Sprintf("%s already %s", rb.ID.Short(), rb.Status)}
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetRunbookStatus{Status: status}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			var ops []model.Op
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
				ops = append(ops, model.SetTitle{Title: title})
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetDescription{Description: text})
			}
			for _, label := range labels.add {
				ops = append(ops, model.AddLabel{Label: label})
			}
			for _, label := range labels.rm {
				ops = append(ops, model.RemoveLabel{Label: label})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("runbook edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.AddComment{Body: body}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newRunbookHistoryCmd() *cobra.Command { return kindHistoryCmd(refs.KindRunbook, "runbook") }

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
			flags := cmd.Flags()
			if err := place.validate(flags, false); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			pos, err := place.position(rb, "")
			if err != nil {
				return err
			}
			op := model.AddStep{ID: model.NewNonce(), Text: args[1], Command: command, Position: pos}
			snapshot, err := s.Append(ctx, ref, []model.Op{op})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			step, err := resolveStep(rb, args[1])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.RemoveStep{ID: step.ID}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			ctx := cmd.Context()
			flags := cmd.Flags()
			if flags.Changed("command") && noCommand {
				return &UsageError{Err: errors.New("--command and --no-command are mutually exclusive")}
			}
			if !flags.Changed("text") && !flags.Changed("command") && !noCommand {
				return &UsageError{Err: errors.New("step edit requires --text, --command, or --no-command")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			step, err := resolveStep(rb, args[1])
			if err != nil {
				return err
			}
			var ops []model.Op
			if flags.Changed("text") {
				ops = append(ops, model.SetStepText{ID: step.ID, Text: text})
			}
			if flags.Changed("command") {
				ops = append(ops, model.SetStepCommand{ID: step.ID, Command: command})
			}
			if noCommand {
				ops = append(ops, model.SetStepCommand{ID: step.ID})
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			flags := cmd.Flags()
			if err := place.validate(flags, true); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			step, err := resolveStep(rb, args[1])
			if err != nil {
				return err
			}
			pos, err := place.position(rb, step.ID)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetStepPosition{ID: step.ID, Position: pos}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			s, err := openStore()
			if err != nil {
				return err
			}
			_, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, runbookStepDTOs(rb.Steps))
			}
			for i, st := range rb.Steps {
				if _, err := fmt.Fprintf(out, "%s\t%d\t%s\t%s\n", shortWireID(st.ID), i+1, st.Text, orDash(st.Command)); err != nil {
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
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			var taskID model.EntityID
			if task != "" {
				_, t, err := loadTask(ctx, s, task)
				if err != nil {
					return err
				}
				taskID = t.ID
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.StartRun{ID: model.NewNonce(), Task: taskID}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			s, err := openStore()
			if err != nil {
				return err
			}
			_, rb, err := loadRunbook(ctx, s, args[0])
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
			s, err := openStore()
			if err != nil {
				return err
			}
			_, rb, err := loadRunbook(ctx, s, args[0])
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
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			step, err := resolveStep(rb, args[1])
			if err != nil {
				return err
			}
			target, err := resolveTargetRun(rb, run)
			if err != nil {
				return err
			}
			op := model.SetRunStepStatus{RunID: target.ID, StepID: step.ID, Status: status, Note: note}
			snapshot, err := s.Append(ctx, ref, []model.Op{op})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
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
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, rb, err := loadRunbook(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := ensureRunbookActive(rb); err != nil {
				return err
			}
			target, err := resolveTargetRun(rb, run)
			if err != nil {
				return err
			}
			if target.Status != model.RunRunning {
				return &ConflictError{Msg: fmt.Sprintf("run %s already %s", shortWireID(target.ID), target.Status)}
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.FinishRun{ID: target.ID, Status: finishStatus(target, failed, abandoned)}})
			if err != nil {
				return err
			}
			return printRunbook(cmd, snapshot.(model.Runbook), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&run, "run", "", "run id prefix (default: the sole running run)")
	flags.BoolVar(&failed, "failed", false, "finish as failed")
	flags.BoolVar(&abandoned, "abandoned", false, "finish as abandoned")
	bindJSON(flags, &jsonOut)
	return cmd
}

// ensureRunbookActive rejects a write to an archived runbook with a
// ConflictError; activate the runbook first to resume editing or running it.
func ensureRunbookActive(rb model.Runbook) error {
	if rb.Status == model.RunbookArchived {
		return &ConflictError{Msg: fmt.Sprintf("runbook %s is archived", rb.ID.Short())}
	}
	return nil
}

// finishStatus resolves a finishing run's terminal status: the explicit
// --failed/--abandoned flag, else failed when any step result failed, else
// succeeded.
func finishStatus(run model.RunbookRun, failed, abandoned bool) model.RunStatus {
	switch {
	case failed:
		return model.RunFailed
	case abandoned:
		return model.RunAbandoned
	}
	for _, r := range run.Results {
		if r.Status == model.StepFailed {
			return model.RunFailed
		}
	}
	return model.RunSucceeded
}

// placementFlags are the four mutually exclusive step-placement flags.
var placementFlags = []string{"first", "last", "before", "after"}

// stepPlacement binds the --first/--last/--before/--after flags that place a
// step within a runbook's ordered steps and resolves them to a fractional-index
// position via model.PositionBetween.
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

// position resolves the placement to a step position within rb, excluding the
// step being moved (movingID, empty for an add) from the neighbor computation.
func (p *stepPlacement) position(rb model.Runbook, movingID string) (string, error) {
	steps := stepsExcluding(rb.Steps, movingID)
	switch {
	case p.first:
		next := ""
		if len(steps) > 0 {
			next = steps[0].Position
		}
		return model.PositionBetween("", next), nil
	case p.before != "":
		target, err := resolveStep(rb, p.before)
		if err != nil {
			return "", err
		}
		if target.ID == movingID {
			return "", &UsageError{Err: errors.New("cannot place a step relative to itself")}
		}
		idx := stepIndex(steps, target.ID)
		prev := ""
		if idx > 0 {
			prev = steps[idx-1].Position
		}
		return model.PositionBetween(prev, target.Position), nil
	case p.after != "":
		target, err := resolveStep(rb, p.after)
		if err != nil {
			return "", err
		}
		if target.ID == movingID {
			return "", &UsageError{Err: errors.New("cannot place a step relative to itself")}
		}
		idx := stepIndex(steps, target.ID)
		next := ""
		if idx < len(steps)-1 {
			next = steps[idx+1].Position
		}
		return model.PositionBetween(target.Position, next), nil
	default:
		prev := ""
		if len(steps) > 0 {
			prev = steps[len(steps)-1].Position
		}
		return model.PositionBetween(prev, ""), nil
	}
}

// stepsExcluding returns rb's steps with excludeID dropped, preserving the
// folded (Position, ID) order; an empty excludeID returns the steps unchanged.
func stepsExcluding(steps []model.RunbookStep, excludeID string) []model.RunbookStep {
	if excludeID == "" {
		return steps
	}
	out := make([]model.RunbookStep, 0, len(steps))
	for _, st := range steps {
		if st.ID != excludeID {
			out = append(out, st)
		}
	}
	return out
}

// stepIndex returns the position of the step with id in steps, or -1.
func stepIndex(steps []model.RunbookStep, id string) int {
	for i, st := range steps {
		if st.ID == id {
			return i
		}
	}
	return -1
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
