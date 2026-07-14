package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestFreeText covers the exactly-one-of resolver: a positional, the named flag,
// or stdin (either source as "-"), with the required/optional absence paths and
// the two-source conflict. Trailing newlines from stdin are trimmed via bodyArg.
func TestFreeText(t *testing.T) {
	tests := []struct {
		name        string
		flagName    string
		flagSet     bool
		flagVal     string
		pos         string
		posGiven    bool
		required    bool
		stdin       string
		want        string
		wantErr     bool
		errContains string
	}{
		{name: "positional literal", flagName: "body", pos: "hello", posGiven: true, required: true, want: "hello"},
		{name: "flag literal", flagName: "body", flagSet: true, flagVal: "flagged", required: true, want: "flagged"},
		{name: "positional stdin trims newlines", flagName: "entry", pos: "-", posGiven: true, required: true, stdin: "from stdin\n\n", want: "from stdin"},
		{name: "flag stdin", flagName: "text", flagSet: true, flagVal: "-", required: true, stdin: "flag stdin\n", want: "flag stdin"},
		{name: "optional absent yields empty", flagName: "body", required: false, want: ""},
		{name: "required absent errors", flagName: "body", required: true, wantErr: true, errContains: "requires text: a positional argument, --body, or - for stdin"},
		{name: "positional and flag conflict", flagName: "entry", flagSet: true, flagVal: "b", pos: "a", posGiven: true, required: true, wantErr: true, errContains: "takes text from exactly one of a positional argument, --entry, or - for stdin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var flagVal string
			cmd := &cobra.Command{Use: "x"}
			cmd.Flags().StringVar(&flagVal, tt.flagName, "", "")
			if tt.flagSet {
				if err := cmd.Flags().Set(tt.flagName, tt.flagVal); err != nil {
					t.Fatalf("set --%s: %v", tt.flagName, err)
				}
			}
			cmd.SetIn(strings.NewReader(tt.stdin))

			got, err := freeText(cmd, tt.flagName, flagVal, tt.pos, tt.posGiven, tt.required)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("freeText() = %q, want error", got)
				}
				var usage *UsageError
				if !errors.As(err, &usage) {
					t.Fatalf("freeText() error %T, want *UsageError", err)
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("freeText() error = %q, want it to contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("freeText() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("freeText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestArgShape derives the positional grammar from cmd.Use bounded by the arity
// n, mirroring the live tree: flag-bearing Use stops at the first flag token
// (supersede is exactArgs 1), mode-dependent arity caps the tokens (doc add
// --checkout is maxArgs 1, runbook add is maxArgs 2), and fewer positionals than
// n omits the shape entirely.
func TestArgShape(t *testing.T) {
	tests := []struct {
		name string
		use  string
		n    int
		want string
	}{
		{"exactArgs 1 stops at the first flag token", "supersede OLD --by NEW", 1, " (OLD)"},
		{"maxArgs 1 caps to the first positional", "add TITLE [BODY]", 1, " (TITLE)"},
		{"maxArgs 2 keeps the bracketed optional", "add TITLE [BODY]", 2, " (TITLE [BODY])"},
		{"exactArgs 2 renders both positionals", "met TASK CRIT", 2, " (TASK CRIT)"},
		{"single positional", "search QUERY", 1, " (QUERY)"},
		{"fewer positionals than arity omits the shape", "supersede OLD --by NEW", 2, ""},
		{"zero arity", "status", 0, ""},
		{"empty use", "", 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := argShape(&cobra.Command{Use: tt.use}, tt.n); got != tt.want {
				t.Fatalf("argShape(%q, %d) = %q, want %q", tt.use, tt.n, got, tt.want)
			}
		})
	}
}
