package cli_test

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

// TestSelfHealingErrors drives every self-healing error path end to end through
// the real command tree: accepted-flags block, sibling-capability scan,
// synonyms, flag-shaped command hints, underscore command names, the
// version-aware unknown-command fallback, and arity shapes. Every case is a
// UsageError (exit 2); each pins the substrings that must appear and, where it
// guards a false positive, the substrings that must not.
func TestSelfHealingErrors(t *testing.T) {
	dir := initRepo(t)

	cases := []struct {
		name     string
		args     []string
		contains []string
		absent   []string
	}{
		{
			name: "accepted flags plus sibling scan names note/doc/log add",
			args: []string{"runbook", "add", "Deploy", "--attach"},
			contains: []string{
				"unknown flag: --attach",
				"runbook add takes: ", "--body", "--branch", "--step",
				"--attach exists on:", `"doc add"`, `"log add"`, `"note add"`,
			},
			absent: []string{`"note edit"`, `"doc edit"`, `"log append"`},
		},
		{
			name:     "accepted flags with no sibling when the flag is defined nowhere",
			args:     []string{"runbook", "add", "Deploy", "--bogus"},
			contains: []string{"unknown flag: --bogus", "runbook add takes: "},
			absent:   []string{"exists on:"},
		},
		{
			name: "sibling scan prefers the failing command's own noun group",
			args: []string{"task", "comment", "abc", "--branch", "main"},
			contains: []string{
				"unknown flag: --branch", "task comment takes: ",
				"--branch exists on:", `"task add"`, `"task edit"`, `"task list"`,
			},
			absent: []string{`"note add"`, `"doc add"`},
		},
		{
			name:     "synonym tags to label",
			args:     []string{"note", "add", "t", "--tags", "x"},
			contains: []string{"unknown flag: --tags", "did you mean --label?"},
		},
		{
			name:     "synonym file to path",
			args:     []string{"note", "add", "t", "--file", "y"},
			contains: []string{"unknown flag: --file", "did you mean --path?"},
		},
		{
			name:     "synonym evidence to note",
			args:     []string{"task", "criterion", "met", "T", "C", "--evidence", "x"},
			contains: []string{"unknown flag: --evidence", "did you mean --note?"},
		},
		{
			name:     "synonym body to entry on a log suppresses the sibling scan",
			args:     []string{"log", "add", "L", "--body", "x"},
			contains: []string{"unknown flag: --body", "did you mean --entry?"},
			absent:   []string{"exists on:"},
		},
		{
			name:     "grep is a command hint",
			args:     []string{"note", "list", "--grep", "x"},
			contains: []string{"unknown flag: --grep", `use "note search QUERY" or top-level "search QUERY"`},
		},
		{
			name:     "mount mode flag hints its subcommand",
			args:     []string{"mount", "--list"},
			contains: []string{"unknown flag: --list", "now subcommands: cc-notes mount list|stop|shutdown"},
		},
		{
			name:     "mount hint does not leak to other groups; a flagless group still names itself",
			args:     []string{"note", "--list"},
			contains: []string{"unknown flag: --list", "note takes no flags"},
			absent:   []string{"now subcommands"},
		},
		{
			name:     "underscore command name",
			args:     []string{"task_list"},
			contains: []string{`did you mean "task list"?`},
			absent:   []string{"brew upgrade"},
		},
		{
			name:     "multi-underscore command name",
			args:     []string{"runbook_step_add"},
			contains: []string{`did you mean "runbook step add"?`},
			absent:   []string{"brew upgrade"},
		},
		{
			name: "version-aware fallback for an unknown root command",
			args: []string{"doctor"},
			contains: []string{
				`unknown command "doctor" for "cc-notes"`,
				"this binary is cc-notes ", `a newer release may add "doctor"`,
				"upgrade: brew upgrade yasyf/tap/cc-notes, or re-run scripts/install.sh",
			},
		},
		{
			name: "root unknown flag after a bad command token still surfaces the version hint",
			args: []string{"frobnicate", "--bogus"},
			contains: []string{
				"unknown flag: --bogus", "cc-notes takes no flags",
				"this binary is cc-notes ", `a newer release may add "frobnicate"`,
			},
		},
		{
			name:     "no version hint on a subcommand-level unknown",
			args:     []string{"task", "frobnicate"},
			contains: []string{`unknown command "frobnicate" for "cc-notes task"`},
			absent:   []string{"brew upgrade", "this binary is cc-notes"},
		},
		{
			name:     "root noun verb keeps its own hint, no version spam",
			args:     []string{"list"},
			contains: []string{"noun-scoped"},
			absent:   []string{"brew upgrade", "this binary is cc-notes"},
		},
		{
			name:     "arity shape on an exact-args command",
			args:     []string{"task", "criterion", "met", "onlyone"},
			contains: []string{"accepts 2 arg(s) (TASK CRIT), received 1"},
		},
		{
			name:     "arity shape on a max-args command",
			args:     []string{"runbook", "add", "a", "b", "c"},
			contains: []string{"accepts at most 2 arg(s) (TITLE [BODY]), received 3"},
		},
		{
			name:     "no shape when Use names no positionals",
			args:     []string{"status", "extra"},
			contains: []string{"accepts 0 arg(s), received 1"},
			absent:   []string{"arg(s) ("},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, dir, tc.args...)
			if err == nil {
				t.Fatalf("%v succeeded, want a usage error", tc.args)
			}
			if got := cli.ExitCode(err); got != 2 {
				t.Fatalf("%v exit = %d, want 2 (usage); err = %q", tc.args, got, err.Error())
			}
			msg := err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Errorf("%v error = %q, want it to contain %q", tc.args, msg, want)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(msg, bad) {
					t.Errorf("%v error = %q, want it NOT to contain %q", tc.args, msg, bad)
				}
			}
		})
	}
}

// TestSelfHealingErrorExactShapes pins the fully assembled multi-line message
// for two stable commands, freezing block order (accepted flags, then remedy,
// then sibling scan) and the sorted flag lists that flagError renders.
func TestSelfHealingErrorExactShapes(t *testing.T) {
	dir := initRepo(t)
	attachUsage := "attach a file's content via git-lfs (repeatable; uploads on sync)"

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "unknown flag with a command hint elides hidden flags",
			args: []string{"mount", "--list"},
			want: "unknown flag: --list\n" +
				"mount takes: --foreground\n" +
				"now subcommands: cc-notes mount list|stop|shutdown",
		},
		{
			name: "unknown flag with a sibling scan",
			args: []string{"runbook", "add", "Deploy", "--attach"},
			want: "unknown flag: --attach\n" +
				"runbook add takes: --body --branch --commit --dir --json --label --path --step\n" +
				`--attach exists on: "doc add" (` + attachUsage + `), "log add" (` + attachUsage + `), "note add" (` + attachUsage + `)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, dir, tc.args...)
			if err == nil {
				t.Fatalf("%v succeeded, want a usage error", tc.args)
			}
			if got := cli.Message(err); got != tc.want {
				t.Fatalf("%v message =\n%q\nwant\n%q", tc.args, got, tc.want)
			}
		})
	}
}
