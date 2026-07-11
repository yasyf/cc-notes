package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

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

// errEmptyDocBody is the shared UsageError for a doc created or edited to carry
// no body; hint names where the content goes on the rejecting surface.
func errEmptyDocBody(hint string) error {
	return &UsageError{Err: fmt.Errorf("doc body is empty — a doc is its body; %s", hint)}
}

// exactArgs is cobra.ExactArgs returning a UsageError, so arity mistakes
// exit 2.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts %d arg(s), received %d", cmd.CommandPath(), n, len(args))}
	}
}

// maxArgs is cobra.MaximumNArgs returning a UsageError, so arity mistakes
// exit 2 (cobra.MaximumNArgs would regress them to exit 1).
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts at most %d arg(s), received %d", cmd.CommandPath(), n, len(args))}
	}
}

// noUnknownSubcommand rejects positional arguments on a command group with
// a UsageError, so unknown subcommands exit 2.
func noUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return unknownSubcommandErr(cmd, args[0], subcommandHint(cmd, args[0]))
}

// runHelp makes a command group runnable so cobra validates its args —
// rejecting unknown subcommands via noUnknownSubcommand — instead of
// short-circuiting to help; a bare group invocation still prints help.
func runHelp(cmd *cobra.Command, _ []string) error { return cmd.Help() }
