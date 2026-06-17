package cli

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/store"
)

func TestResolveCriterion(t *testing.T) {
	task := model.Task{Criteria: []model.Criterion{
		{ID: "aaaa1111", Text: "retry backoff"},
		{ID: "aaaa2222", Text: "no conn leaks"},
		{ID: "bbbb3333", Text: "clean shutdown"},
	}}
	tests := []struct {
		name    string
		prefix  string
		wantID  string
		wantErr error
	}{
		{"unique full id", "bbbb3333", "bbbb3333", nil},
		{"unique prefix", "bbbb", "bbbb3333", nil},
		{"case insensitive", "BBBB", "bbbb3333", nil},
		{"no match", "cccc", "", store.ErrNotFound},
		{"ambiguous prefix", "aaaa", "", store.ErrAmbiguous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCriterion(task, tc.prefix)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.ID != tc.wantID {
				t.Fatalf("id = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}
