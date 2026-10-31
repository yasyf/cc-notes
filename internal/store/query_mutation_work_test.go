package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type queryMutationWorkContext struct {
	context.Context
	mu     sync.Mutex
	checks map[string]int
}

func (c *queryMutationWorkContext) Err() error {
	pc, _, line, ok := runtime.Caller(1)
	if ok {
		name := runtime.FuncForPC(pc).Name()
		if strings.HasSuffix(name, ".queryMetadataSource.capture") || strings.HasSuffix(name, ".diffQueryMetadata") {
			c.mu.Lock()
			c.checks[fmt.Sprintf("%s:%d", name, line)]++
			c.mu.Unlock()
		}
	}
	return c.Context.Err()
}

func TestTaskQueryIndexConsecutiveWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			dir := gittest.InitRepo(t)
			gitDir := filepath.Join(dir, ".git")
			repo, err := gitobj.Open(gitDir, gitDir)
			if err != nil {
				t.Fatal(err)
			}
			sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			var packed []string
			for i := range count + 1 {
				label := "other"
				if i == 0 {
					label = "target"
				}
				sha, err := repo.WriteOpsCommit(t.Context(), nil, sig, "task fixture", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("fixture %d", i), Type: model.TypeTask, Priority: 2, Labels: []string{label}}}})
				if err != nil {
					t.Fatal(err)
				}
				packed = append(packed, string(sha)+" "+refs.For(model.KindTask, model.EntityID(sha)))
			}
			slices.Sort(packed)
			if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			createProbe := func(ctx context.Context, title string) {
				t.Helper()
				s, err := OpenContext(ctx, dir)
				if err != nil {
					t.Fatal(err)
				}
				_, createErr := s.Create(ctx, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Priority: 2, Labels: []string{"target"}}})
				closeErr := s.Close()
				if createErr != nil {
					t.Fatal(createErr)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			createProbe(t.Context(), "cold probe")
			time.Sleep(2100 * time.Millisecond)
			s, err := OpenContext(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			_, hit, queryErr := s.IndexedTasks(t.Context(), []string{"target"})
			closeErr := s.Close()
			if queryErr != nil || !hit || closeErr != nil {
				t.Fatalf("prime hit=%v query=%v close=%v", hit, queryErr, closeErr)
			}
			for i := range 3 {
				ctx := &queryMutationWorkContext{Context: t.Context(), checks: make(map[string]int)}
				createProbe(ctx, fmt.Sprintf("consecutive probe %d", i+1))
				t.Logf("unrelated=%d consecutive=%d source_cancellation_check_callsites=%v", count, i+1, ctx.checks)
			}
		})
	}
}

func TestQueryMutationWorkContextCallsites(t *testing.T) {
	s := initStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	pins := map[string]model.SHA{
		refs.For(model.KindTask, "a"): "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		refs.For(model.KindTask, "b"): "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	ctx := &queryMutationWorkContext{Context: t.Context(), checks: make(map[string]int)}
	q := queryMetadataSource{store: s.Pinned(pins), kind: model.KindTask, pinned: true}
	captured, err := q.capture(ctx)
	if err != nil || len(captured.tips) != 2 {
		t.Fatalf("capture tips=%v err=%v", captured.tips, err)
	}
	visits := 0
	for name, count := range ctx.checks {
		if strings.Contains(name, ".queryMetadataSource.capture:") {
			visits += count
		}
	}
	if visits != 2 {
		t.Fatalf("capture cancellation callsite counted %d visits, want2: %v", visits, ctx.checks)
	}
}
