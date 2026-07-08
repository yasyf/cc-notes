package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestUnknownFlagName(t *testing.T) {
	tests := []struct {
		name   string
		msg    string
		want   string
		wantOK bool
	}{
		{"long flag", "unknown flag: --desc", "desc", true},
		{"long flag with dashes", "unknown flag: --anchor-path", "anchor-path", true},
		{"shorthand", "unknown shorthand flag: 'm' in -m", "m", true},
		{"unrelated error", "invalid argument for --foo", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := unknownFlagName(tt.msg)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("unknownFlagName(%q) = (%q, %v), want (%q, %v)", tt.msg, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestUnknownFlagNamePinsPflag pins the two pflag messages flagError parses
// against the real library: a wording change trips here, degrading hints to the
// plain error rather than emitting a wrong one.
func TestUnknownFlagNamePinsPflag(t *testing.T) {
	long := pflag.NewFlagSet("t", pflag.ContinueOnError)
	long.SetOutput(io.Discard)
	err := long.Parse([]string{"--nope"})
	if err == nil {
		t.Fatal("expected an unknown-flag error from pflag")
	}
	if got, ok := unknownFlagName(err.Error()); !ok || got != "nope" {
		t.Fatalf("long: unknownFlagName(%q) = (%q, %v), want (nope, true)", err.Error(), got, ok)
	}

	short := pflag.NewFlagSet("t", pflag.ContinueOnError)
	short.SetOutput(io.Discard)
	err = short.Parse([]string{"-z"})
	if err == nil {
		t.Fatal("expected an unknown-shorthand error from pflag")
	}
	if got, ok := unknownFlagName(err.Error()); !ok || got != "z" {
		t.Fatalf("shorthand: unknownFlagName(%q) = (%q, %v), want (z, true)", err.Error(), got, ok)
	}
}

func TestSubcommandHint(t *testing.T) {
	root := &cobra.Command{Use: "cc-notes"}
	task := &cobra.Command{Use: "task"}
	sprint := &cobra.Command{Use: "sprint"}
	criterion := &cobra.Command{Use: "criterion"}
	note := &cobra.Command{Use: "note"}

	tests := []struct {
		name     string
		cmd      *cobra.Command
		arg      string
		contains string // "" means no hint expected
	}{
		{"root bare list", root, "list", "noun-scoped"},
		{"root bare comment", root, "comment", "noun-scoped"},
		{"root real read stays clean", root, "show", ""},
		{"task move", task, "move", "task edit --branch"},
		{"sprint start", sprint, "start", "sprint activate"},
		{"criterion reset", criterion, "reset", "task criterion pending"},
		{"note unknown verb", note, "frobnicate", ""},
		{"task unknown verb", task, "frobnicate", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subcommandHint(tt.cmd, tt.arg)
			if tt.contains == "" {
				if got != "" {
					t.Fatalf("subcommandHint(%s, %q) = %q, want no hint", tt.cmd.Name(), tt.arg, got)
				}
				return
			}
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("subcommandHint(%s, %q) = %q, want it to contain %q", tt.cmd.Name(), tt.arg, got, tt.contains)
			}
		})
	}
}
