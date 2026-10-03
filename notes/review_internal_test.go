package notes

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

func TestMemoAncestryJudgesEachCommitOncePerHead(t *testing.T) {
	broken := errors.New("walk failed")
	calls := map[string]int{}
	judge := memoAncestry(func(_ context.Context, head model.SHA, rev string) (gitobj.Ancestry, error) {
		calls[string(head)+" "+rev]++
		switch rev {
		case "broken":
			return gitobj.NotAncestor, broken
		case "gone":
			return gitobj.NotAncestor, nil
		}
		return gitobj.Ancestor, nil
	})
	for _, tc := range []struct {
		head model.SHA
		rev  string
		want gitobj.Ancestry
		err  error
	}{
		{"h1", "kept", gitobj.Ancestor, nil},
		{"h1", "kept", gitobj.Ancestor, nil},
		{"h1", "gone", gitobj.NotAncestor, nil},
		{"h1", "gone", gitobj.NotAncestor, nil},
		{"h2", "kept", gitobj.Ancestor, nil},
		{"h1", "broken", gitobj.NotAncestor, broken},
		{"h1", "broken", gitobj.NotAncestor, broken},
	} {
		got, err := judge(t.Context(), tc.head, tc.rev)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Fatalf("judge(%s, %s) = %v, %v; want %v, %v", tc.head, tc.rev, got, err, tc.want, tc.err)
		}
	}
	want := map[string]int{"h1 kept": 1, "h1 gone": 1, "h2 kept": 1, "h1 broken": 2}
	if !maps.Equal(calls, want) {
		t.Fatalf("inner judge calls = %v, want %v: each (head, rev) once, errors never cached", calls, want)
	}
}
