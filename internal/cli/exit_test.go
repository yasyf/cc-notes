package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/mountd"
)

func TestExitCodeAndLabel(t *testing.T) {
	ambiguous := &store.AmbiguousError{
		Kind:   model.KindTask,
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
		{"sync contended", fmt.Errorf("sync: %w", ccsync.ErrSyncContended), 4, "conflict"},
		{"ambiguous", fmt.Errorf("resolve: %w", ambiguous), 5, "ambiguous"},
		{"remote missing", fmt.Errorf("sync: %w", ccsync.ErrRemoteNotFound), 1, "error"},
		// Mount-holder conflicts: exit 4.
		{"holder busy", fmt.Errorf("mount: %w", mountd.ErrBusy), 4, "conflict"},
		{"foreign mount", fmt.Errorf("mount: %w", mountd.ErrForeignMount), 4, "conflict"},
		{"base mismatch", fmt.Errorf("mount: %w", mountd.ErrBaseMismatch), 4, "conflict"},
		// Every other holder-class error and the fuse sentinels: exit 1.
		{"holder unavailable", fmt.Errorf("mount: %w", mountd.ErrHolderUnavailable), 1, "error"},
		{"tcc denied", fmt.Errorf("mount: %w", mountd.ErrTCCDenied), 1, "error"},
		{"holder unmount wedged", fmt.Errorf("unmount: %w", mountd.ErrUnmountWedged), 1, "error"},
		{"mount timeout", fmt.Errorf("mount: %w", mountd.ErrMountTimeout), 1, "error"},
		{"mount failed", fmt.Errorf("mount: %w", mountd.ErrMountFailed), 1, "error"},
		{"unknown class", fmt.Errorf("mount: %w", mountd.ErrUnknownClass), 1, "error"},
		{"cannot host", fmt.Errorf("spawn: %w", mountd.ErrCannotHost), 1, "error"},
		{"fuse unavailable", fmt.Errorf("mount: %w", fusekit.ErrFuseUnavailable), 1, "error"},
		// RemoteHost dual-wraps a TCC denial with fusekit.ErrMountNotLive; it must
		// still classify as a plain error, never a conflict.
		{"overlay tcc", fmt.Errorf("mount: %w: %w", fusekit.ErrMountNotLive, mountd.ErrTCCDenied), 1, "error"},
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
