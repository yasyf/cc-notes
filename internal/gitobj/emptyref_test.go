package gitobj

import (
	"context"
	"errors"
	"testing"

	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

func TestRetryEmptyRef(t *testing.T) {
	errOther := errors.New("boom")
	cases := []struct {
		name      string
		failures  int   // leading lookups that fail with ErrEmptyRefFile
		always    error // when set, every lookup fails with this
		cancel    bool
		wantCalls int
		wantErr   error
	}{
		{name: "immediate success", wantCalls: 1},
		{name: "heals after transient empties", failures: 3, wantCalls: 4},
		{name: "persistent empty exhausts attempts", always: dotgit.ErrEmptyRefFile, wantCalls: emptyRefAttempts, wantErr: dotgit.ErrEmptyRefFile},
		{name: "other error returns immediately", always: errOther, wantCalls: 1, wantErr: errOther},
		{name: "cancelled context stops retrying", always: dotgit.ErrEmptyRefFile, cancel: true, wantCalls: 1, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			calls := 0
			got, err := retryEmptyRef(ctx, func() (int, error) {
				calls++
				if tc.always != nil {
					return 0, tc.always
				}
				if calls <= tc.failures {
					return 0, dotgit.ErrEmptyRefFile
				}
				return 42, nil
			})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("retryEmptyRef error = %v, want %v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Errorf("retryEmptyRef made %d calls, want %d", calls, tc.wantCalls)
			}
			if tc.wantErr == nil && got != 42 {
				t.Errorf("retryEmptyRef = %d, want 42", got)
			}
		})
	}
}
