package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestFlagGroupConstraints pins the rendered lines for each cobra flag-group
// kind, in the fixed kind order, and that a command with no groups renders none.
func TestFlagGroupConstraints(t *testing.T) {
	multi := &cobra.Command{Use: "multi"}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		multi.Flags().Bool(n, false, "")
	}
	multi.MarkFlagsMutuallyExclusive("a", "b")
	multi.MarkFlagsRequiredTogether("c", "d")
	multi.MarkFlagsOneRequired("e", "f")

	got := flagGroupConstraints(multi)
	want := []string{
		"--a, --b are mutually exclusive",
		"--c, --d must be used together",
		"one of --e, --f is required",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("flagGroupConstraints = %q, want %q", got, want)
	}

	plain := &cobra.Command{Use: "plain"}
	plain.Flags().Bool("x", false, "")
	if got := flagGroupConstraints(plain); len(got) != 0 {
		t.Fatalf("flagGroupConstraints(no groups) = %q, want none", got)
	}
	if got := constraintsBlock(plain); got != "" {
		t.Fatalf("constraintsBlock(no groups) = %q, want empty", got)
	}
}

// TestConvertedCommandExclusions pins each command's cobra mutual-exclusion flag
// groups: flagGroupConstraints surfaces the annotation AND --help renders it
// under Constraints. Losing any MarkFlagsMutuallyExclusive drops the expected
// line from both, failing the case.
func TestConvertedCommandExclusions(t *testing.T) {
	cases := []struct {
		path []string
		want []string
	}{
		{[]string{"task", "add"}, []string{"--branch, --backlog are mutually exclusive"}},
		{[]string{"task", "edit"}, []string{
			"--assignee, --no-assignee are mutually exclusive",
			"--parent, --no-parent are mutually exclusive",
			"--sprint, --no-sprint are mutually exclusive",
			"--project, --no-project are mutually exclusive",
			"--branch, --backlog are mutually exclusive",
		}},
		{[]string{"task", "claim"}, []string{"--steal, --sync are mutually exclusive"}},
		{[]string{"note", "expire"}, []string{"--reason, --clear are mutually exclusive"}},
		{[]string{"doc", "expire"}, []string{"--reason, --clear are mutually exclusive"}},
		{[]string{"sprint", "edit"}, []string{
			"--project, --no-project are mutually exclusive",
			"--start, --no-start are mutually exclusive",
			"--end, --no-end are mutually exclusive",
		}},
		{[]string{"runbook", "step", "add"}, []string{"--first, --last, --before, --after are mutually exclusive"}},
		{[]string{"runbook", "step", "move"}, []string{"--first, --last, --before, --after are mutually exclusive"}},
		{[]string{"runbook", "step", "edit"}, []string{"--command, --no-command are mutually exclusive"}},
		{[]string{"runbook", "run", "finish"}, []string{"--failed, --abandoned are mutually exclusive"}},
	}
	for _, tc := range cases {
		name := strings.Join(tc.path, " ")
		t.Run(name, func(t *testing.T) {
			cmd, _, err := NewRootCmd().Find(tc.path)
			if err != nil {
				t.Fatalf("find %q: %v", name, err)
			}
			got := flagGroupConstraints(cmd)
			for _, want := range tc.want {
				if !slices.Contains(got, want) {
					t.Errorf("flagGroupConstraints(%q) = %q, missing %q", name, got, want)
				}
			}

			help := helpOutput(t, tc.path)
			if !strings.Contains(help, "Constraints:\n") {
				t.Errorf("%q --help missing Constraints section:\n%s", name, help)
			}
			for _, want := range tc.want {
				if !strings.Contains(help, want) {
					t.Errorf("%q --help missing %q:\n%s", name, want, help)
				}
			}
		})
	}
}

// helpOutput runs `<path> --help` on a fresh root and returns its rendered text.
func helpOutput(t *testing.T, path []string) string {
	t.Helper()
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append(slices.Clone(path), "--help"))
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %q --help: %v", strings.Join(path, " "), err)
	}
	return buf.String()
}

// TestConstraintsBlockInHelp proves the usage template installed by NewRootCmd
// renders the Constraints section in a subcommand's --help, since cobra renders
// no help for flag groups on its own.
func TestConstraintsBlockInHelp(t *testing.T) {
	root := NewRootCmd()
	synth := &cobra.Command{
		Use:   "synth",
		Short: "synthetic",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	synth.Flags().Bool("a", false, "flag a")
	synth.Flags().Bool("b", false, "flag b")
	synth.MarkFlagsMutuallyExclusive("a", "b")
	root.AddCommand(synth)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"synth", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute synth --help: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "Constraints:\n  --a, --b are mutually exclusive\n") {
		t.Fatalf("--help missing Constraints block:\n%s", out)
	}
}
