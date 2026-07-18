package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

// maxTitleBytes caps a title in bytes, mirroring maxAttachmentNameBytes.
const maxTitleBytes = 256

// Escape hints name, per command surface, the places that actually hold long
// content: note/doc add and edit both carry it in --body/--checkout/--attach,
// logs carry it in entries, task/sprint/project in --body, and a checked-out
// file-mode buffer carries it in the body below the frontmatter (bufferHint
// serves both the title cap and errEmptyDocBody there).
const (
	titleHintBody  = "put the content in --body (- reads stdin), --checkout file mode, or --attach"
	titleHintLog   = "put the content in log entries (--entry on add, or log append)"
	titleHintDesc  = "put the content in --body (- reads stdin)"
	bufferHint     = "put the content in the body below the frontmatter"
	docBodyHintAdd = "pass --body (- reads stdin), --checkout file mode, or --attach the content"
)

// bodyArg returns value, or the command's stdin (trailing newlines trimmed)
// when value is "-".
func bodyArg(cmd *cobra.Command, value string) (string, error) {
	if value != "-" {
		return value, nil
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// freeText resolves free-form content from exactly one of: the positional pos
// (present iff posGiven), the flag flagName (value flagVal), or stdin (either
// source given as "-"). More than one is a UsageError; zero is too when
// required, as is a required source that resolves to empty text. Both source
// errors name all three forms. The chosen value flows through bodyArg, so "-"
// reads stdin with trailing newlines trimmed.
func freeText(cmd *cobra.Command, flagName, flagVal, pos string, posGiven, required bool) (string, error) {
	flagged := cmd.Flags().Changed(flagName)
	sources := 0
	if posGiven {
		sources++
	}
	if flagged {
		sources++
	}
	switch sources {
	case 0:
		if required {
			return "", &UsageError{Err: fmt.Errorf("%s requires text: a positional argument, --%s, or - for stdin", cmd.CommandPath(), flagName)}
		}
		return "", nil
	case 1:
		value := pos
		if flagged {
			value = flagVal
		}
		text, err := bodyArg(cmd, value)
		if err != nil {
			return "", err
		}
		if required && text == "" {
			return "", &UsageError{Err: fmt.Errorf("%s requires text: a positional argument, --%s, or - for stdin", cmd.CommandPath(), flagName)}
		}
		return text, nil
	default:
		return "", &UsageError{Err: fmt.Errorf("%s takes text from exactly one of a positional argument, --%s, or - for stdin", cmd.CommandPath(), flagName)}
	}
}

// validateTitle rejects an empty or over-long title as a UsageError, run before
// openStore/autoInstall so a rejected create or rename mutates nothing. hint names
// the flags on the calling command that hold the long content a title should not.
func validateTitle(title, hint string) error {
	switch {
	case title == "":
		return &UsageError{Err: errors.New("title is empty — a title is a short handle for the entity; give it a few descriptive words")}
	case len(title) > maxTitleBytes:
		return &UsageError{Err: fmt.Errorf("title is %d bytes (max %d) — a title is a short handle; %s", len(title), maxTitleBytes, hint)}
	default:
		return nil
	}
}

func validateInvestigationTitle(cmd *cobra.Command, title string) error {
	if err := validateTitle(title, titleHintDesc); err != nil {
		return err
	}
	for _, word := range strings.FieldsFunc(strings.ToUpper(title), func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch word {
		case "RESOLVED", "FIXED", "FALSIFIED", "CONFIRMED":
			_, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: investigation title contains %s; status is structural — use a transition verb instead\n", word)
			return err
		}
	}
	return nil
}

// errEmptyDocBody is the shared UsageError for a doc created or edited to carry
// no body; hint names where the content goes on the rejecting surface.
func errEmptyDocBody(hint string) error {
	return &UsageError{Err: fmt.Errorf("doc body is empty — a doc is its body; %s", hint)}
}

// exactArgs is cobra.ExactArgs returning a UsageError, so arity mistakes
// exit 2. The message shows the positional shape parsed from cmd.Use.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts %d arg(s)%s, received %d", cmd.CommandPath(), n, argShape(cmd, n), len(args))}
	}
}

// maxArgs is cobra.MaximumNArgs returning a UsageError, so arity mistakes
// exit 2 (cobra.MaximumNArgs would regress them to exit 1). The message shows
// the positional shape parsed from cmd.Use.
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts at most %d arg(s)%s, received %d", cmd.CommandPath(), n, argShape(cmd, n), len(args))}
	}
}

// argShape renders the n leading positional tokens of cmd.Use as " (TASK CRIT)":
// the fields after the verb, stopping at the first flag token, capped at n with
// brackets kept. It returns "" when Use names no positionals or yields fewer than
// n, so the caller's message falls back to the plain count form.
func argShape(cmd *cobra.Command, n int) string {
	if n <= 0 {
		return ""
	}
	fields := strings.Fields(cmd.Use)
	if len(fields) <= 1 {
		return ""
	}
	var pos []string
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			break
		}
		pos = append(pos, f)
	}
	if len(pos) < n {
		return ""
	}
	return " (" + strings.Join(pos[:n], " ") + ")"
}

// noUnknownSubcommand rejects positional arguments on a command group with
// a UsageError, so unknown subcommands exit 2. A root-level unknown that
// matched no suggestion carries the version-aware upgrade hint.
func noUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	arg := args[0]
	hint := subcommandHint(cmd, arg)
	if hint == "" && cmd == cmd.Root() {
		hint = versionUnknownHint(arg)
	}
	return unknownSubcommandErr(cmd, arg, hint)
}

// runHelp makes a command group runnable so cobra validates its args —
// rejecting unknown subcommands via noUnknownSubcommand — instead of
// short-circuiting to help; a bare group invocation still prints help.
func runHelp(cmd *cobra.Command, _ []string) error { return cmd.Help() }
