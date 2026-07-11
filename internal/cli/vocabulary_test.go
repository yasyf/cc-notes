package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// updateGolden regenerates the cli golden files instead of asserting against
// them: go test ./internal/cli -run Golden -update. Shared by the vocabulary
// and DTO-shape golden tests.
var updateGolden = flag.Bool("update", false, "regenerate cli golden files")

// TestVocabularyGolden freezes the entire cobra command tree: every command's
// full path, Use line, aliases, Short, hidden flag, and the deterministically
// sorted local, own-persistent, and inherited flags (name, shorthand, value
// type, default, usage). The CLI vocabulary is a frozen contract; a byte
// mismatch here is the intended tripwire for any command, flag, or help-text
// drift. The tree is walked without executing, so cobra's lazily injected
// --help/--version flags never enter the snapshot — only our declared surface.
func TestVocabularyGolden(t *testing.T) {
	got := renderVocabulary(NewRootCmd())

	golden := filepath.Join("testdata", "vocabulary.golden")
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("vocabulary mismatch (regenerate with -update):\n%s", got)
	}
}

// renderVocabulary walks the command tree depth-first with children sorted by
// name and renders each command's frozen text block.
func renderVocabulary(root *cobra.Command) string {
	var b strings.Builder
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		writeCommandVocabulary(&b, c)
		children := append([]*cobra.Command(nil), c.Commands()...)
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	return b.String()
}

// writeCommandVocabulary renders one command's identity and flag surface.
func writeCommandVocabulary(b *strings.Builder, c *cobra.Command) {
	fmt.Fprintf(b, "command: %s\n", c.CommandPath())
	fmt.Fprintf(b, "  use: %s\n", c.Use)
	fmt.Fprintf(b, "  aliases: %s\n", csvOrDash(c.Aliases))
	fmt.Fprintf(b, "  short: %s\n", c.Short)
	fmt.Fprintf(b, "  hidden: %t\n", c.Hidden)
	writeFlagSection(b, "flags", c.LocalFlags(), c)
	writeFlagSection(b, "inherited", c.InheritedFlags(), nil)
	b.WriteByte('\n')
}

// writeFlagSection renders a flag set sorted by name. When own is non-nil each
// flag is tagged persistent if it is one of own's persistent flags.
func writeFlagSection(b *strings.Builder, label string, fs *pflag.FlagSet, own *cobra.Command) {
	var flags []*pflag.Flag
	fs.VisitAll(func(f *pflag.Flag) { flags = append(flags, f) })
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })

	fmt.Fprintf(b, "  %s:\n", label)
	if len(flags) == 0 {
		b.WriteString("    (none)\n")
		return
	}
	for _, f := range flags {
		shorthand := "none"
		if f.Shorthand != "" {
			shorthand = "-" + f.Shorthand
		}
		persistent := ""
		if own != nil && own.PersistentFlags().Lookup(f.Name) != nil {
			persistent = " persistent"
		}
		fmt.Fprintf(b, "    --%s shorthand=%s type=%s default=%q usage=%q%s\n",
			f.Name, shorthand, f.Value.Type(), f.DefValue, f.Usage, persistent)
	}
}
