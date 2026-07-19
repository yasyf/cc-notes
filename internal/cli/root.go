// Package cli wires the cobra command tree for cc-notes: the note, doc, log,
// task, sprint, project, runbook, investigation, attachment, skills, hooks, and
// workflows noun groups plus init, sync, and version. Output is agents-first — lean
// deterministic lines or compact JSON on stdout, one labeled error line on
// stderr, exit codes mapped from typed errors via ExitCode.
package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/version"
)

func init() {
	cobra.AddTemplateFunc("constraints", constraintsBlock)
}

// NewRootCmd builds the root command with every subcommand attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cc-notes",
		Short:         "Git-native notes and tasks for agents",
		Version:       version.String(),
		Args:          noUnknownSubcommand,
		RunE:          runHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().StringP("repo", "R", "", "operate on the repository at this path (any path inside it) instead of the working directory")
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(flagError)
	// Cobra renders no help for flag groups; append a Constraints: block that
	// recovers them. Subcommands inherit this template via getUsageTemplateFunc.
	root.SetUsageTemplate(root.UsageTemplate() + "{{constraints .}}")
	root.AddCommand(newInitCmd(), newSyncCmd(), newStatusCmd(), newReconcileCmd(), newBlameCmd(), newHistoryCmd(), newShowCmd(), newSearchCmd(), newRelevantCmd(), newCompactCmd(), newGCCmd(), newVizCmd(), newMCPCmd(), newVersionCmd(), newNoteCmd(), newDocCmd(), newLogCmd(), newPapercutCmd(), newTaskCmd(), newSprintCmd(), newProjectCmd(), newRunbookCmd(), newInvestigationCmd(), newAttachmentCmd(), newSkillsCmd(), newHooksCmd(), newWorkflowsCmd())
	return root
}

// Flag-group annotation keys mirror cobra's unexported constants in
// flag_groups.go; MarkFlags* set them and cobra reads them at validation, but it
// renders no help for them.
const (
	annRequiredTogether  = "cobra_annotation_required_if_others_set"
	annOneRequired       = "cobra_annotation_one_required"
	annMutuallyExclusive = "cobra_annotation_mutually_exclusive"
)

// constraintsBlock renders cmd's flag-group constraints as a "Constraints:"
// section for the usage template, or "" when the command declares none. The
// leading blank line matches the template's other sections.
func constraintsBlock(cmd *cobra.Command) string {
	lines := flagGroupConstraints(cmd)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nConstraints:\n")
	for _, line := range lines {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// flagGroupConstraints renders cmd's flag-group annotations as human lines, in a
// fixed kind order with each group's flags de-duplicated and sorted.
func flagGroupConstraints(cmd *cobra.Command) []string {
	kinds := []struct {
		ann    string
		render func(flags string) string
	}{
		{annMutuallyExclusive, func(f string) string { return f + " are mutually exclusive" }},
		{annRequiredTogether, func(f string) string { return f + " must be used together" }},
		{annOneRequired, func(f string) string { return "one of " + f + " is required" }},
	}
	lines := make([]string, 0, len(kinds))
	for _, k := range kinds {
		groups := map[string]bool{}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			for _, g := range f.Annotations[k.ann] {
				groups[g] = true
			}
		})
		uniq := make([]string, 0, len(groups))
		for g := range groups {
			uniq = append(uniq, g)
		}
		sort.Strings(uniq)
		for _, g := range uniq {
			names := strings.Split(g, " ")
			for i, n := range names {
				names[i] = "--" + n
			}
			lines = append(lines, k.render(strings.Join(names, ", ")))
		}
	}
	return lines
}
