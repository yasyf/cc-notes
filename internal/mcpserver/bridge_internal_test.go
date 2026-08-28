package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

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
