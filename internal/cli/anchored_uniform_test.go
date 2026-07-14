package cli_test

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/cli"
)

// anchorQuartet is the four add-time anchor flags every anchored kind binds via
// the shared anchorSets binder.
var anchorQuartet = []string{"commit", "path", "dir", "branch"}

// anchorOctet is the eight edit-time anchor flags every anchored kind binds via
// the shared anchorEdits binder.
var anchorOctet = []string{
	"add-commit", "rm-commit",
	"add-path", "rm-path",
	"add-dir", "rm-dir",
	"add-branch", "rm-branch",
}

// TestAnchoredKindsUniform walks the built cobra tree and asserts note, doc,
// log, and runbook expose an identical anchor surface: the add quartet, the edit
// octet, the scalar anchor filters plus --label on list, and those plus --limit
// on search. Each shared flag must carry a byte-identical usage string and value
// type across all four kinds, measured against the note command as the
// reference. A missing flag or drifted wording is a red test naming the
// divergent command path, so it passes only because the surface is uniform.
func TestAnchoredKindsUniform(t *testing.T) {
	root := cli.NewRootCmd()
	kinds := []string{"note", "doc", "log", "runbook"}

	listFlags := append(append([]string{}, anchorQuartet...), "label")
	searchFlags := append(append([]string{}, anchorQuartet...), "label", "limit")

	for _, g := range []struct {
		verb  string
		flags []string
	}{
		{"add", anchorQuartet},
		{"edit", anchorOctet},
		{"list", listFlags},
		{"search", searchFlags},
	} {
		t.Run(g.verb, func(t *testing.T) {
			ref := commandFlags(t, root, "note", g.verb)
			for _, name := range g.flags {
				refFlag := ref.Lookup(name)
				if refFlag == nil {
					t.Fatalf("reference command `note %s` is missing --%s", g.verb, name)
				}
				for _, kind := range kinds {
					fs := commandFlags(t, root, kind, g.verb)
					f := fs.Lookup(name)
					if f == nil {
						t.Errorf("`%s %s` is missing --%s (present on `note %s`)", kind, g.verb, name, g.verb)
						continue
					}
					if f.Usage != refFlag.Usage {
						t.Errorf("`%s %s --%s` usage = %q, want %q (must match `note %s`)", kind, g.verb, name, f.Usage, refFlag.Usage, g.verb)
					}
					if f.Value.Type() != refFlag.Value.Type() {
						t.Errorf("`%s %s --%s` type = %q, want %q (must match `note %s`)", kind, g.verb, name, f.Value.Type(), refFlag.Value.Type(), g.verb)
					}
				}
			}
		})
	}
}

// commandFlags resolves "<kind> <verb>" in the built tree and returns its full
// flag set, failing the test when the path does not resolve to that subcommand.
func commandFlags(t *testing.T, root *cobra.Command, kind, verb string) *pflag.FlagSet {
	t.Helper()
	cmd, _, err := root.Find([]string{kind, verb})
	if err != nil {
		t.Fatalf("find `%s %s`: %v", kind, verb, err)
	}
	if cmd.Name() != verb {
		t.Fatalf("find `%s %s` resolved to %q, want the %s subcommand", kind, verb, cmd.CommandPath(), verb)
	}
	return cmd.Flags()
}
