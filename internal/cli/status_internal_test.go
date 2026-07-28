package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/notes"
)

// TestPrintStatusSkippedOps proves the board reports history this binary cannot
// fold — the only place a skipped op is visible — and stays quiet without one.
func TestPrintStatusSkippedOps(t *testing.T) {
	cases := []struct {
		name    string
		skipped int
		want    string
	}{
		{name: "nothing skipped", skipped: 0},
		{name: "three skipped", skipped: 3, want: "skipped 3 op(s) this cc-notes cannot fold; " + UpgradeRemedy + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := printStatusText(cmd, notes.StatusReport{SkippedOps: tc.skipped}); err != nil {
				t.Fatalf("printStatusText: %v", err)
			}
			if tc.want == "" {
				if strings.Contains(out.String(), "skipped") {
					t.Fatalf("output = %q, want no skipped-op line", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output = %q, want it to contain %q", out.String(), tc.want)
			}
		})
	}
}
