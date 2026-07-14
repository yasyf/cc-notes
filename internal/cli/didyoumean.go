package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/version"
)

// flagSynonyms maps a mistyped flag (a long name, or a shorthand character) to
// the canonical flags that replace it, best candidate first. A hint fires only
// for the first candidate the failing command actually defines, so an entry
// stays dormant on commands that lack the canonical flag and disambiguates the
// three body spellings by which noun the failing command uses.
var flagSynonyms = map[string][]string{
	"desc":          {"body"},
	"description":   {"body"},
	"tag":           {"label"},
	"tags":          {"label"},
	"add-tag":       {"add-label"},
	"rm-tag":        {"rm-label"},
	"message":       {"entry", "body"},
	"m":             {"entry", "body"},
	"text":          {"body", "entry"},
	"content":       {"body", "entry"},
	"body":          {"entry", "text"},
	"file":          {"path", "attach"},
	"evidence":      {"note"},
	"anchor-path":   {"path"},
	"anchor-dir":    {"dir"},
	"anchor-branch": {"branch"},
	"anchor-commit": {"commit"},
	"to":            {"branch"},
	"unassign":      {"no-assignee"},
	"remove":        {"clear"},
}

// flagError wraps a pflag parse error as a UsageError (exit 2). When the failing
// command leads with a removed or misplaced subcommand token, it reports that
// unknown command with its migration hint instead: cobra parses flags before
// validating positionals, so a legacy verb carrying an unknown flag (e.g.
// "task move ID --to X") would otherwise surface as a bare unknown-flag error,
// hiding the hint noUnknownSubcommand gives the flagless form. Otherwise it makes
// the error self-healing: the failing command's accepted flags (always, so every
// error names the command), then either a did-you-mean synonym / command hint or,
// only when neither lands, a scan of sibling commands that define the flag —
// remedy and relocation advice are alternatives, not layers. A root-level flag
// whose leading non-flag token names no subcommand also gets the unknown-command
// hint chain, so an interspersed flag surfaces it. It never rewrites the exit
// class, and a pflag wording change degrades to the plain error, never a wrong hint.
func flagError(cmd *cobra.Command, err error) error {
	if pos := cmd.Flags().Args(); len(pos) > 0 {
		if hint := subcommandHint(cmd, pos[0]); hint != "" {
			return unknownSubcommandErr(cmd, pos[0], hint)
		}
	}
	name, ok := unknownFlagName(err.Error())
	if !ok {
		return &UsageError{Err: err}
	}
	extra := []string{acceptedFlagsLine(cmd)}
	if remedy := flagRemedy(cmd, name); remedy != "" {
		extra = append(extra, remedy)
	} else if sibling := siblingFlagScan(cmd, name); sibling != "" {
		extra = append(extra, sibling)
	}
	if cmdHint := rootUnknownCommandHint(cmd); cmdHint != "" {
		extra = append(extra, cmdHint)
	}
	return &UsageError{Err: fmt.Errorf("%w\n%s", err, strings.Join(extra, "\n"))}
}

// rootUnknownCommandHint appends the unknown-command hint chain to a root-level
// unknown-flag error whose leading non-flag token names no subcommand, so an
// interspersed flag ("cc-notes --bogus task_list") surfaces the same underscore,
// migration, or version guidance the flagless form gives. It is "" for a non-root
// command or when the invocation carries no non-flag token.
func rootUnknownCommandHint(cmd *cobra.Command) string {
	if cmd != cmd.Root() {
		return ""
	}
	arg := firstNonFlagArg(cmd)
	if arg == "" {
		return ""
	}
	if hint := subcommandHint(cmd, arg); hint != "" {
		return hint
	}
	return versionUnknownHint(arg)
}

// firstNonFlagArg returns the leading non-flag token of the failing invocation:
// the positionals pflag salvaged before the parse error, else — when the error
// tripped before any positional was seen (the flag led) — a scan of os.Args. It
// is "" when the invocation carries no non-flag token.
func firstNonFlagArg(cmd *cobra.Command) string {
	for _, a := range cmd.Flags().Args() {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// unknownFlagName extracts the offending flag from pflag's two unknown-flag
// messages, "unknown flag: --NAME" and "unknown shorthand flag: 'C' in -C",
// returning the long name or the shorthand character. Both prefixes are pinned
// here so a pflag wording change trips one test.
func unknownFlagName(msg string) (string, bool) {
	if rest, ok := strings.CutPrefix(msg, "unknown flag: --"); ok {
		return rest, true
	}
	if rest, ok := strings.CutPrefix(msg, "unknown shorthand flag: '"); ok {
		if name, _, ok := strings.Cut(rest, "'"); ok {
			return name, true
		}
	}
	return "", false
}

// flagRemedy returns the single-line remediation for an unknown flag: a
// did-you-mean pointing at the canonical flag the failing command defines when
// name has a synonym, else a flagCommandHint when the fix is a different
// command. Empty when neither applies.
func flagRemedy(cmd *cobra.Command, name string) string {
	for _, cand := range flagSynonyms[name] {
		if cmd.Flags().Lookup(cand) != nil {
			return fmt.Sprintf("did you mean --%s?", cand)
		}
	}
	return flagCommandHint(cmd, name)
}

// acceptedFlagsLine renders the failing command's accepted flags as a compact,
// sorted, ~100-column-wrapped list ("<path> takes: --a --b …"), so an agent sees
// the real vocabulary the moment a flag misses. Inherited persistent flags are
// included; hidden flags and cobra's auto-injected --help/--version are elided. A
// command with no visible flags renders "<path> takes no flags", so every
// unknown-flag error names the failing command.
func acceptedFlagsLine(cmd *cobra.Command) string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || f.Name == "version" {
			return
		}
		names = append(names, "--"+f.Name)
	})
	path := commandPathNoRoot(cmd)
	if len(names) == 0 {
		return path + " takes no flags"
	}
	sort.Strings(names)
	return wrapFlagList(path+" takes: ", names, 100)
}

// wrapFlagList joins names after prefix, wrapping to a new line indented under
// the first name whenever appending the next would pass width.
func wrapFlagList(prefix string, names []string, width int) string {
	indent := strings.Repeat(" ", len(prefix))
	var b strings.Builder
	b.WriteString(prefix)
	col := len(prefix)
	for i, n := range names {
		if i > 0 {
			if col+1+len(n) > width {
				b.WriteByte('\n')
				b.WriteString(indent)
				col = len(indent)
			} else {
				b.WriteByte(' ')
				col++
			}
		}
		b.WriteString(n)
		col += len(n)
	}
	return b.String()
}

// flagSite is one command that defines a flag the failing command lacks.
type flagSite struct {
	path  string
	usage string
}

// siblingFlagScan walks the live command tree for other commands defining the
// unknown flag and names up to three ("--X exists on: \"note add\" (usage), …").
// The registry is the binders, so the scan cannot drift from them. Ordering is
// deterministic: the failing command's own noun group first, then add/edit/list
// verbs, then lexicographic. Hidden flags do not count as definitions. Empty when
// no other command defines the flag (so a shorthand character, which no long flag
// matches, never yields a line).
func siblingFlagScan(cmd *cobra.Command, name string) string {
	var sites []flagSite
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != cmd && !c.Hidden && c.Name() != "help" {
			if f := c.Flags().Lookup(name); f != nil && !f.Hidden {
				sites = append(sites, flagSite{path: commandPathNoRoot(c), usage: f.Usage})
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(cmd.Root())
	if len(sites) == 0 {
		return ""
	}
	noun := nounOf(cmd)
	sort.Slice(sites, func(i, j int) bool {
		si, sj := sites[i], sites[j]
		if gi, gj := siblingGroup(si.path, noun), siblingGroup(sj.path, noun); gi != gj {
			return gi < gj
		}
		if vi, vj := verbRank(si.path), verbRank(sj.path); vi != vj {
			return vi < vj
		}
		return si.path < sj.path
	})
	if len(sites) > 3 {
		sites = sites[:3]
	}
	parts := make([]string, 0, len(sites))
	for _, s := range sites {
		parts = append(parts, fmt.Sprintf("%q (%s)", s.path, s.usage))
	}
	return fmt.Sprintf("--%s exists on: %s", name, strings.Join(parts, ", "))
}

// siblingGroup ranks a command path by whether its noun matches the failing
// command's, so same-group siblings sort first.
func siblingGroup(path, noun string) int {
	if strings.Fields(path)[0] == noun {
		return 0
	}
	return 1
}

// verbRank ranks a command path's terminal verb: add, then edit, then list, then
// everything else, so the canonical create/modify/read siblings surface first.
func verbRank(path string) int {
	fields := strings.Fields(path)
	switch fields[len(fields)-1] {
	case "add":
		return 0
	case "edit":
		return 1
	case "list":
		return 2
	default:
		return 3
	}
}

// nounOf returns cmd's top-level noun group — its ancestor that is a direct
// child of the root (the command itself when it already is one).
func nounOf(cmd *cobra.Command) string {
	root := cmd.Root()
	c := cmd
	for c.Parent() != nil && c.Parent() != root {
		c = c.Parent()
	}
	return c.Name()
}

// commandPathNoRoot is cmd's command path with the invoked binary name dropped,
// so messages read "runbook add", not "cc-notes runbook add".
func commandPathNoRoot(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

// mountSubcmdHint redirects a --list/--stop/--shutdown flag to the mount
// subcommands that carry those operations.
const mountSubcmdHint = "now subcommands: cc-notes mount list|stop|shutdown"

// flagCommandHints maps a command name to flag-shaped mistakes whose real fix is
// one of that command's subcommands. --grep is not here: its guidance is
// templated with the failing command's noun, so flagCommandHint builds it.
var flagCommandHints = map[string]map[string]string{
	"mount": {
		"list":     mountSubcmdHint,
		"stop":     mountSubcmdHint,
		"shutdown": mountSubcmdHint,
	},
}

// flagCommandHint returns guidance for a flag-shaped mistake whose fix is a
// command rather than a flag, or "" when none applies. --grep is universal
// (every noun's search verb, and the top-level search); the rest are scoped to
// the command they appear under.
func flagCommandHint(cmd *cobra.Command, name string) string {
	if name == "grep" {
		return fmt.Sprintf(`use "%s search QUERY" or top-level "search QUERY"`, nounOf(cmd))
	}
	return flagCommandHints[cmd.Name()][name]
}

// subcommandHints maps a command name plus a removed or renamed subcommand token
// to the guidance that replaces it. Most entries stay dormant until a later
// phase deletes the old verb.
var subcommandHints = map[string]map[string]string{
	"task":      {"move": `use "task edit --branch BRANCH" (or --backlog)`},
	"sprint":    {"start": `use "sprint activate"`},
	"criterion": {"reset": `use "task criterion pending"`},
	"run":       {"reset": `a wrong step mark is corrected by re-marking (runbook run done|skip|fail)`},
}

// rootNounVerbs are the bare verbs an agent guesses at the top level; every
// write verb is noun-scoped (only kind-inferable reads go global).
var rootNounVerbs = map[string]bool{
	"list": true, "add": true, "edit": true, "rm": true, "comment": true,
}

const rootNounVerbHint = `commands are noun-scoped: try "task list", "note list", … ("status" shows the board)`

// subcommandHint returns guidance for a removed or misplaced subcommand token on
// cmd, or "" when there is none. At the root it also recognizes bare noun verbs
// and MCP-style underscore command names (task_list, runbook_step_add).
func subcommandHint(cmd *cobra.Command, arg string) string {
	if cmd == cmd.Root() {
		if rootNounVerbs[arg] {
			return rootNounVerbHint
		}
		if hint := underscoreHint(cmd, arg); hint != "" {
			return hint
		}
	}
	return subcommandHints[cmd.Name()][arg]
}

// underscoreHint resolves an MCP-style underscore command name against the live
// tree by splitting on "_" and probing root.Find; cobra's longest-prefix match
// handles multi-underscore nouns (runbook_step_add → runbook step add). It
// returns 'did you mean "task list"?' when the split resolves to a real command
// path, else "".
func underscoreHint(root *cobra.Command, token string) string {
	if !strings.Contains(token, "_") {
		return ""
	}
	cmd, rest, err := root.Find(strings.Split(token, "_"))
	if err != nil || cmd == root || len(rest) > 0 {
		return ""
	}
	return fmt.Sprintf("did you mean %q?", commandPathNoRoot(cmd))
}

// versionUnknownHint is the fallback for a root-level unknown command that
// matched no suggestion: this binary's own version is the stale-binary signal,
// so a newer release may define the command.
func versionUnknownHint(arg string) string {
	return fmt.Sprintf("this binary is cc-notes %s — a newer release may add %q; upgrade: brew upgrade yasyf/tap/cc-notes, or re-run scripts/install.sh", version.String(), arg)
}

// unknownSubcommandErr reports arg as an unknown command for cmd, appending hint
// when non-empty. noUnknownSubcommand and flagError share it so the removed-verb
// migration message reads identically whether the legacy verb was flagless or
// carried an unknown flag.
func unknownSubcommandErr(cmd *cobra.Command, arg, hint string) error {
	if hint != "" {
		return &UsageError{Err: fmt.Errorf("unknown command %q for %q; %s", arg, cmd.CommandPath(), hint)}
	}
	return &UsageError{Err: fmt.Errorf("unknown command %q for %q", arg, cmd.CommandPath())}
}
