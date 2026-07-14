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
