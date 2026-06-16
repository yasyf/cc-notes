package cli

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestMaxArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "mount"}
	for _, tc := range []struct {
		name    string
		n       int
		args    []string
		wantErr bool
	}{
		{"zero of one", 1, nil, false},
		{"one of one", 1, []string{"a"}, false},
		{"two of one", 1, []string{"a", "b"}, true},
		{"none allowed empty", 0, nil, false},
		{"none allowed one", 0, []string{"a"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := maxArgs(tc.n)(cmd, tc.args)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("maxArgs(%d)(%v) err = %v, want nil", tc.n, tc.args, err)
				}
				return
			}
			var usage *UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("maxArgs(%d)(%v) err = %v, want *UsageError", tc.n, tc.args, err)
			}
		})
	}
}
