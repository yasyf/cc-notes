package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/version"
)

// mustFind resolves a full command path in the live tree, failing when it does
// not resolve exactly (a stale path would silently return the root).
func mustFind(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd, rest, err := root.Find(path)
	if err != nil || len(rest) > 0 {
		t.Fatalf("Find(%v) = (%v, rest=%v, err=%v), want an exact match", path, cmd.CommandPath(), rest, err)
	}
	if want := "cc-notes " + strings.Join(path, " "); cmd.CommandPath() != want {
		t.Fatalf("Find(%v) resolved to %q, want %q", path, cmd.CommandPath(), want)
	}
	return cmd
}

// TestAcceptedFlagsLine pins the sorted, root-stripped accepted-flags rendering
// against a real command; --attach (a sibling-only flag) is absent because
// runbook add does not define it.
func TestAcceptedFlagsLine(t *testing.T) {
	root := NewRootCmd()
	got := acceptedFlagsLine(mustFind(t, root, "runbook", "add"))
	want := "runbook add takes: --body --branch --commit --dir --json --label --path --step"
	if got != want {
		t.Fatalf("acceptedFlagsLine(runbook add) = %q, want %q", got, want)
	}
}

// TestAcceptedFlagsLineWraps checks a flag-dense command wraps to indented
// continuation lines under the first flag rather than overrunning ~100 columns.
func TestAcceptedFlagsLineWraps(t *testing.T) {
	root := NewRootCmd()
	got := acceptedFlagsLine(mustFind(t, root, "note", "add"))
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("note add accepted-flags line did not wrap: %q", got)
	}
	prefix := "note add takes: "
	for _, l := range lines {
		if len(l) > 100 {
			t.Errorf("wrapped line exceeds 100 cols (%d): %q", len(l), l)
		}
	}
	if !strings.HasPrefix(lines[0], prefix) {
		t.Fatalf("first line = %q, want prefix %q", lines[0], prefix)
	}
	if want := strings.Repeat(" ", len(prefix)); !strings.HasPrefix(lines[1], want+"--") {
		t.Fatalf("continuation = %q, want it indented %d spaces under the first flag", lines[1], len(prefix))
	}
}

// TestAcceptedFlagsLineNoFlags: a bare command group (no visible flags) still
// names itself with "<path> takes no flags", so every unknown-flag error carries
// the failing command path.
func TestAcceptedFlagsLineNoFlags(t *testing.T) {
	root := NewRootCmd()
	if got := acceptedFlagsLine(mustFind(t, root, "note")); got != "note takes no flags" {
		t.Fatalf("acceptedFlagsLine(note group) = %q, want %q", got, "note takes no flags")
	}
}

// TestAcceptedFlagsLineHidden: a hidden flag is elided from the accepted-flags
// line, so mount advertises only its visible --foreground, not --auto/--socket.
func TestAcceptedFlagsLineHidden(t *testing.T) {
	root := NewRootCmd()
	if got := acceptedFlagsLine(mustFind(t, root, "mount")); got != "mount takes: --foreground" {
		t.Fatalf("acceptedFlagsLine(mount) = %q, want %q", got, "mount takes: --foreground")
	}
}

// TestSiblingFlagScan pins the deterministic selection and format: the top three
// commands defining the flag, add-verb siblings first then lexicographic, each
// with its usage; a flag defined nowhere else yields "".
func TestSiblingFlagScan(t *testing.T) {
	root := NewRootCmd()
	attachUsage := "attach a file's content via git-lfs (repeatable; uploads on sync)"
	branchScope := "task branch (default: current branch)"

	cases := []struct {
		name string
		path []string
		flag string
		want string
	}{
		{
			name: "attach names note/doc/log add, edit/append capped out",
			path: []string{"runbook", "add"},
			flag: "attach",
			want: `--attach exists on: "doc add" (` + attachUsage + `), "log add" (` + attachUsage + `), "note add" (` + attachUsage + `)`,
		},
		{
			name: "branch prefers the failing command's own noun group",
			path: []string{"task", "comment"},
			flag: "branch",
			want: `--branch exists on: "task add" (` + branchScope + `), "task edit" (` + branchScope + `), "task list" (filter to branch (default: current branch))`,
		},
		{
			name: "flag defined nowhere else",
			path: []string{"runbook", "add"},
			flag: "bogus",
			want: "",
		},
		{
			name: "a flag defined only on a hidden binder yields no scan",
			path: []string{"note", "list"},
			flag: "auto",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := siblingFlagScan(mustFind(t, root, tc.path...), tc.flag)
			if got != tc.want {
				t.Fatalf("siblingFlagScan(%v, %q) =\n  %q\nwant\n  %q", tc.path, tc.flag, got, tc.want)
			}
		})
	}
}

// TestUnderscoreHint resolves MCP-style underscore names against the live tree,
// including a multi-underscore noun, and rejects tokens that do not fully
// resolve.
func TestUnderscoreHint(t *testing.T) {
	root := NewRootCmd()
	cases := []struct {
		token string
		want  string
	}{
		{"task_list", `did you mean "task list"?`},
		{"runbook_step_add", `did you mean "runbook step add"?`},
		{"papercut_list", `did you mean "papercut list"?`},
		{"task_frob", ""},
		{"foo_bar", ""},
		{"nounderscore", ""},
		{"task_", ""},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			if got := underscoreHint(root, tc.token); got != tc.want {
				t.Fatalf("underscoreHint(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}

// TestFlagCommandHint: --grep is templated with the failing command's noun;
// mount's mode flags map to its subcommands; everything else is "".
func TestFlagCommandHint(t *testing.T) {
	root := NewRootCmd()
	noteList := mustFind(t, root, "note", "list")
	mount := mustFind(t, root, "mount")

	cases := []struct {
		name string
		cmd  *cobra.Command
		flag string
		want string
	}{
		{"grep on note list", noteList, "grep", `use "note search QUERY" or top-level "search QUERY"`},
		{"list on mount", mount, "list", mountSubcmdHint},
		{"stop on mount", mount, "stop", mountSubcmdHint},
		{"shutdown on mount", mount, "shutdown", mountSubcmdHint},
		{"list off mount", noteList, "list", ""},
		{"unknown flag no hint", noteList, "zzz", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := flagCommandHint(tc.cmd, tc.flag); got != tc.want {
				t.Fatalf("flagCommandHint(%s, %q) = %q, want %q", tc.cmd.CommandPath(), tc.flag, got, tc.want)
			}
		})
	}
}

// TestVersionUnknownHint pins the stale-binary fallback's format around the
// build-injected version, without hard-coding the version itself.
func TestVersionUnknownHint(t *testing.T) {
	got := versionUnknownHint("doctor")
	for _, want := range []string{
		"this binary is cc-notes " + version.String(),
		`a newer release may add "doctor"`,
		"upgrade: brew upgrade yasyf/tap/cc-notes, or re-run scripts/install.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("versionUnknownHint(doctor) = %q, want it to contain %q", got, want)
		}
	}
}
