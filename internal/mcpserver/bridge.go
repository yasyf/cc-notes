// Package mcpserver serves the cc-notes tool surface to an MCP client over
// stdio. Every tool drives the same cobra command tree as the CLI in-process:
// a handler builds argv, runs a fresh root against string buffers, and returns
// the JSON DTO on stdout (plus any stderr notices) as the tool result text.
// Validation is never re-implemented — the CLI owns it, and a typed error maps
// to an error result carrying the exit-class label.
package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
)

// bridge runs argv against a fresh cobra root and maps stdout/stderr/error into
// an MCP tool result. The CLI seams are injected so this package never imports
// internal/cli (which imports this one to register the mcp command).
type bridge struct {
	newRoot func() *cobra.Command
	label   func(error) string
	message func(error) string
	hint    func(error) string

	retained  *store.Retained
	successor func() string
}

// run executes argv in-process and returns the tool result: stdout (the JSON
// DTO) with any stderr notices appended, or a `<label>: <message>` error — plus
// the error's hint line, so a caller sees the same way forward the CLI prints —
// that the SDK renders as an error result. It never touches os.Stdout — the stdio
// protocol owns it — and pins stdin to empty so no command blocks on the
// protocol's own stdin. A call that names its repository runs with --repo,
// which the CLI resolves ahead of the retained store.
func (b *bridge) run(ctx context.Context, argv ...string) (*mcp.CallToolResult, any, error) {
	if repo, ok := repoFrom(ctx); ok {
		argv = append([]string{"--repo=" + repo}, argv...)
	}
	if b.successor != nil {
		if installed := b.successor(); installed != "" {
			return runInstalled(ctx, installed, argv)
		}
	}
	if b.retained != nil {
		ctx = store.WithRetained(ctx, b.retained)
	}
	ctx, scope := store.WithScope(ctx)
	defer func() { _ = scope.Close() }()
	root := b.newRoot()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(bytes.NewReader(nil))
	root.SetArgs(argv)
	if err := root.ExecuteContext(ctx); err != nil {
		text := fmt.Sprintf("%s: %s", b.label(err), b.message(err))
		if hint := b.hint(err); hint != "" {
			text += "\n" + hint
		}
		return nil, nil, errors.New(text)
	}
	return toolResult(out.String(), errBuf.String()), nil, nil
}

func runInstalled(ctx context.Context, installed string, argv []string) (*mcp.CallToolResult, any, error) {
	//nolint:gosec // G204: installed is the binary this server's own argv[0] resolves to, and argv is a tool's CLI argv.
	cmd := exec.CommandContext(ctx, installed, argv...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case errors.As(err, &exit):
		return nil, nil, errors.New(strings.TrimSpace(errBuf.String()))
	case err != nil:
		return nil, nil, err
	}
	return toolResult(out.String(), errBuf.String()), nil, nil
}

func toolResult(out, notices string) *mcp.CallToolResult {
	// The JSON DTO is the primary block so it stays parseable; stderr notices
	// (one-time git config installs, sync reports) ride as a separate block.
	contents := []mcp.Content{&mcp.TextContent{Text: strings.TrimRight(out, "\n")}}
	if notices = strings.TrimSpace(notices); notices != "" {
		contents = append(contents, &mcp.TextContent{Text: notices})
	}
	return &mcp.CallToolResult{Content: contents}
}

// argvFor assembles a command invocation: the fixed subcommand path, the built
// flags, then a "--" guard and the positional args. The guard keeps a positional
// that begins with "-" (a title, a markdown comment body) from being parsed as a
// flag; flag values themselves are safe unguarded, since pflag takes the token
// after "--flag" literally.
func argvFor(path []string, flags []string, positionals ...string) []string {
	argv := make([]string, 0, len(path)+len(flags)+1+len(positionals))
	argv = append(argv, path...)
	argv = append(argv, flags...)
	if len(positionals) > 0 {
		argv = append(argv, "--")
		argv = append(argv, positionals...)
	}
	return argv
}

// optStr appends flag value only when value is non-empty.
func optStr(argv []string, flag, value string) []string {
	if value != "" {
		return append(argv, flag, value)
	}
	return argv
}

// optRepeated appends flag once per value.
func optRepeated(argv []string, flag string, values []string) []string {
	for _, v := range values {
		argv = append(argv, flag, v)
	}
	return argv
}

// optBool appends the bare flag when set.
func optBool(argv []string, flag string, set bool) []string {
	if set {
		return append(argv, flag)
	}
	return argv
}

// optInt appends flag value only when the pointer is non-nil, so a caller that
// omits the field leaves the CLI default in force (0 is a valid value for
// several int flags).
func optInt(argv []string, flag string, value *int) []string {
	if value != nil {
		return append(argv, flag, strconv.Itoa(*value))
	}
	return argv
}
