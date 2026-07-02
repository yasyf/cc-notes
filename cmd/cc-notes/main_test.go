package main

import (
	"fmt"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestUpgradeHint(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "add_attachment from a newer binary",
			err:  fmt.Errorf("decode op 0: %w", &model.UnknownKindError{Kind: "add_attachment"}),
			want: "op kind \"add_attachment\" was written by a newer cc-notes; run `brew upgrade yasyf/tap/cc-notes` and retry",
		},
		{
			name: "remove_attachment from a newer binary",
			err:  &model.UnknownKindError{Kind: "remove_attachment"},
			want: "op kind \"remove_attachment\" was written by a newer cc-notes; run `brew upgrade yasyf/tap/cc-notes` and retry",
		},
		{
			name: "bare ErrUnknownKind carries no kind",
			err:  fmt.Errorf("marshal: %w", model.ErrUnknownKind),
			want: "",
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("resolve tip: %w", model.ErrInvalidValue),
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upgradeHint(tc.err); got != tc.want {
				t.Fatalf("upgradeHint(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
