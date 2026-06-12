package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

func TestExitCodeAndLabel(t *testing.T) {
	ambiguous := &store.AmbiguousError{
		Kind:   refs.KindTask,
		Prefix: "a",
		Candidates: []store.Candidate{
			{ID: model.EntityID("aaaaaaa1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Title: "one"},
			{ID: model.EntityID("aaaaaaa2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Title: "two"},
		},
	}
	cases := []struct {
		name  string
		err   error
		code  int
		label string
	}{
		{"nil", nil, 0, ""},
		{"plain error", errors.New("boom"), 1, "error"},
		{"usage", &cli.UsageError{Err: errors.New("unknown flag")}, 2, "usage"},
		{"wrapped usage", fmt.Errorf("run: %w", &cli.UsageError{Err: errors.New("bad arity")}), 2, "usage"},
		{"not found store", fmt.Errorf("resolve: %w", store.ErrNotFound), 3, "not-found"},
		{"not found ref", fmt.Errorf("load: %w", gitobj.ErrRefNotFound), 3, "not-found"},
		{"conflict", &cli.ConflictError{Msg: "already done"}, 4, "conflict"},
		{"contended", fmt.Errorf("append: %w", store.ErrContended), 4, "conflict"},
		{"not live", fmt.Errorf("promote: %w", ccsync.ErrNotLive), 4, "conflict"},
		{"ambiguous", fmt.Errorf("resolve: %w", ambiguous), 5, "ambiguous"},
		{"remote missing", fmt.Errorf("sync: %w", ccsync.ErrRemoteNotFound), 1, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cli.ExitCode(tc.err); got != tc.code {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.code)
			}
			if got := cli.Label(tc.err); got != tc.label {
				t.Errorf("Label(%v) = %q, want %q", tc.err, got, tc.label)
			}
		})
	}
}
