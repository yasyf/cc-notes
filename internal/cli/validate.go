package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
)

// newTaskValidateCmd runs a task's acceptance-criteria validation scripts and
// records the verdicts.
//
// Threat model: criterion scripts are stored content that arrives over git sync
// from other agents and remotes, so executing one runs untrusted code in the
// repository working tree. This is the only place cc-notes execs stored content
// — it must never be reachable from sync, list, fold, done, or render. Two
// guards bound the blast radius: every script is printed to stderr before
// anything runs, and execution is refused unless the operator opts in with
// --yes or answers an interactive prompt. A non-terminal stdin without --yes is
// a hard error, so a piped or automated invocation can never run a script
// silently.
func newTaskValidateCmd() *cobra.Command {
	var yes, jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "validate TASK",
		Short: "Run a task's validation scripts (explicit, confirmation-gated)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			ref, task, err := loadTask(ctx, s, args[0])
			if err != nil {
				return err
			}
			var scripted []model.Criterion
			for _, c := range task.Criteria {
				if c.Script != "" {
					scripted = append(scripted, c)
				}
			}
			out := cmd.OutOrStdout()
			stderr := cmd.ErrOrStderr()
			if len(scripted) == 0 {
				_, err := fmt.Fprintln(out, "no criteria have validation scripts")
				return err
			}
			for _, c := range scripted {
				if _, err := fmt.Fprintf(stderr, "criterion %s %s:\n%s\n", c.ID[:7], sanitizeDisplay(c.Text, false), sanitizeDisplay(c.Script, true)); err != nil {
					return err
				}
			}
			if !yes {
				if err := confirmScripts(cmd, len(scripted)); err != nil {
					return err
				}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ops := make([]model.Op, len(scripted))
			for i, c := range scripted {
				status := runScript(ctx, s.Git.Dir, c.Script, timeout)
				if _, err := fmt.Fprintf(stderr, "%s %s %s\n", c.ID[:7], status, sanitizeDisplay(c.Text, false)); err != nil {
					return err
				}
				ops[i] = model.SetCriterionStatus{ID: c.ID, Status: status}
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&yes, "yes", false, "run the scripts without the interactive confirmation prompt")
	flags.DurationVar(&timeout, "timeout", 5*time.Minute, "per-script timeout")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// confirmScripts gates execution behind operator consent. With a terminal on
// stdin it prompts and proceeds only on y/yes; without one it refuses, so a
// non-interactive invocation never runs a script unless it passed --yes.
func confirmScripts(cmd *cobra.Command, n int) error {
	fi, err := os.Stdin.Stat()
	interactive := err == nil && fi.Mode()&os.ModeCharDevice != 0
	if !interactive {
		return errors.New("refusing to run validation scripts without --yes (stdin is not a terminal)")
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Run %d validation script(s)? [y/N] ", n); err != nil {
		return err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("aborted: validation not confirmed")
	}
}

// runScript executes one criterion's check command under sh in dir, bounded by
// timeout and ctx cancellation. Exit 0 is met; a non-zero exit or a timeout is
// failed.
func runScript(ctx context.Context, dir, script string, timeout time.Duration) model.CriterionStatus {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, "sh", "-c", script)
	cmd.Dir = dir
	// WaitDelay force-closes the script's inherited pipes shortly after the
	// timeout fires, so a child that outlives sh (holding stdout open) cannot
	// hang CombinedOutput past the bound.
	cmd.WaitDelay = 5 * time.Second
	if _, err := cmd.CombinedOutput(); err != nil {
		return model.CriterionFailed
	}
	return model.CriterionMet
}

// sanitizeDisplay neutralizes control bytes in untrusted criterion content
// before it is echoed to the terminal, so a synced script or text cannot emit
// terminal escape sequences or forge a "criterion ...:" header line. With
// keepNewlines a multi-line script keeps its real line breaks; single-line
// fields escape newlines too. Only the displayed copy is sanitized — sh -c
// still runs the raw script bytes.
func sanitizeDisplay(s string, keepNewlines bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines, r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
