package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// flagSynonyms maps a mistyped flag (a long name, or a shorthand character) to
// the canonical flags that replace it, best candidate first. A hint fires only
// for the first candidate the failing command actually defines, so most entries
// stay dormant until a later phase renames the flag into existence.
var flagSynonyms = map[string][]string{
	"desc":          {"body"},
	"description":   {"body"},
	"tag":           {"label"},
	"add-tag":       {"add-label"},
	"rm-tag":        {"rm-label"},
	"message":       {"entry", "body"},
	"m":             {"entry", "body"},
	"text":          {"body", "entry"},
	"content":       {"body", "entry"},
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
// hiding the hint noUnknownSubcommand gives the flagless form. Otherwise it
// appends a did-you-mean hint when the mistyped flag has a known synonym the
// failing command defines. It never rewrites the exit class — a hint, not an
// alias — and a pflag wording change degrades to the plain error, never a wrong
// hint.
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
	for _, cand := range flagSynonyms[name] {
		if cmd.Flags().Lookup(cand) != nil {
			return &UsageError{Err: fmt.Errorf("%w (did you mean --%s?)", err, cand)}
		}
	}
	return &UsageError{Err: err}
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
	"list": true, "add": true, "edit": true, "rm": true, "search": true, "comment": true,
}

const rootNounVerbHint = `commands are noun-scoped: try "task list", "note list", … ("status" shows the board)`

// subcommandHint returns guidance for a removed or misplaced subcommand token on
// cmd, or "" when there is none.
func subcommandHint(cmd *cobra.Command, arg string) string {
	if cmd.Name() == "cc-notes" && rootNounVerbs[arg] {
		return rootNounVerbHint
	}
	return subcommandHints[cmd.Name()][arg]
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
