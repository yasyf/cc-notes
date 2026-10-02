package sync

import (
	"slices"
	"strings"
	"testing"
)

func TestPushBatches(t *testing.T) {
	ref := func(c byte, n int) string { return "refs/cc-notes/notes/" + strings.Repeat(string(c), n) }
	half := ref('a', pushArgBudget/2-len("refs/cc-notes/notes/"))
	other := ref('b', pushArgBudget/2-len("refs/cc-notes/notes/"))
	third := ref('c', 1)
	tests := []struct {
		name    string
		pending []string
		want    [][]string
	}{
		{name: "none", pending: nil, want: nil},
		{name: "one batch", pending: []string{"refs/cc-notes/notes/x", "refs/cc-notes/docs/y"}, want: [][]string{{"refs/cc-notes/notes/x", "refs/cc-notes/docs/y"}}},
		{name: "exactly the budget", pending: []string{half, other}, want: [][]string{{half, other}}},
		{name: "over the budget", pending: []string{half, other, third}, want: [][]string{{half, other}, {third}}},
		{name: "one ref over the budget alone", pending: []string{ref('d', pushArgBudget), third}, want: [][]string{{ref('d', pushArgBudget)}, {third}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pushBatches(tc.pending)
			if !slices.EqualFunc(got, tc.want, slices.Equal) {
				t.Fatalf("pushBatches batch lengths = %v, want %v", batchLens(got), batchLens(tc.want))
			}
		})
	}
}

func batchLens(batches [][]string) []int {
	lens := make([]int, len(batches))
	for i, b := range batches {
		lens[i] = len(b)
	}
	return lens
}
