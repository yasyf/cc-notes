package mcpserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// TestBridgeRunErrorText pins the tool error text to `<label>: <message>` with
// the notes-layer "cc-notes: " program prefix trimmed, so a raw notes error does
// not render doubly (`conflict: cc-notes: ...`) as it would if run wrapped the
// raw error under the label, and pins the hint onto a following line so an MCP
// caller reads the same remediation the CLI prints to stderr.
func TestBridgeRunErrorText(t *testing.T) {
	tests := []struct {
		name   string
		runErr error
		hint   string
		want   string
	}{
		{"trims program prefix", errors.New("cc-notes: abc1234 already done"), "", "conflict: abc1234 already done"},
		{"verbatim without prefix", errors.New("abc1234 already done"), "", "conflict: abc1234 already done"},
		{"appends the hint line", errors.New("abc1234 already done"), "reopen it first", "conflict: abc1234 already done\nreopen it first"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runErr := tc.runErr
			b := &bridge{
				newRoot: func() *cobra.Command {
					return &cobra.Command{
						Use:           "root",
						SilenceErrors: true,
						SilenceUsage:  true,
						RunE:          func(*cobra.Command, []string) error { return runErr },
					}
				},
				label:   func(error) string { return "conflict" },
				message: func(err error) string { return strings.TrimPrefix(err.Error(), "cc-notes: ") },
				hint:    func(error) string { return tc.hint },
			}
			_, _, err := b.run(context.Background())
			if err == nil {
				t.Fatalf("run returned nil error, want %q", tc.want)
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("error text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuccessorNamesTheBinaryAnUpgradeInstalled(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "cc-notes")
	//nolint:gosec // G306: LookPath finds only an executable file.
	if err := os.WriteFile(installed, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	next, err := successor(installed, "cc-notes")
	if err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "" {
		t.Fatalf("successor before any upgrade = %q, want none", got)
	}
	upgrade := filepath.Join(dir, "cc-notes.new")
	//nolint:gosec // G306: LookPath finds only an executable file.
	if err := os.WriteFile(upgrade, []byte("#!/bin/sh\necho new\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(upgrade, installed); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != installed {
		t.Fatalf("successor after the binary was replaced = %q, want %q", got, installed)
	}
}

func TestBridgeRunsEveryCallWithTheUpgradedBinary(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "cc-notes")
	stub := "#!/bin/sh\nif [ \"$1\" = fail ]; then echo 'conflict: abc1234 already done' >&2; echo 'reopen it first' >&2; exit 4; fi\nprintf '{\"argv\":\"%s\"}\\n' \"$*\"\necho 'synced' >&2\n"
	//nolint:gosec // G306: the stub stands in for an installed executable.
	if err := os.WriteFile(installed, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	inProcess := func() *cobra.Command {
		t.Fatal("a call ran in-process after the upgrade")
		return nil
	}
	b := &bridge{newRoot: inProcess, successor: func() string { return installed }}

	res, _, err := b.run(t.Context(), "note", "list", "--json")
	if err != nil {
		t.Fatalf("run through the upgraded binary: %v", err)
	}
	if got := res.Content[0].(*mcp.TextContent).Text; got != `{"argv":"note list --json"}` {
		t.Fatalf("result = %q, want the upgraded binary's stdout", got)
	}
	if got := res.Content[1].(*mcp.TextContent).Text; got != "synced" {
		t.Fatalf("notices = %q, want the upgraded binary's stderr", got)
	}
	if _, _, err := b.run(t.Context(), "fail"); err == nil || err.Error() != "conflict: abc1234 already done\nreopen it first" {
		t.Fatalf("failed call through the upgraded binary = %v, want its label, message and hint", err)
	}
}
