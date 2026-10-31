package notes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestLabeledListsReadOnlySelectedBodies(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation} {
		t.Run(string(kind), func(t *testing.T) {
			c, dir := newWBClient(t)
			ctx := t.Context()
			selected := createLabeledListEntity(t, c, kind, "selected", []string{"target"})
			for i := range 24 {
				createLabeledListEntity(t, c, kind, fmt.Sprintf("unrelated %d", i), nil)
			}
			time.Sleep(2100 * time.Millisecond)
			if _, hit, err := c.s.StatusMetadata(ctx, kind); err != nil || !hit {
				t.Fatalf("prime metadata = %v, %v", hit, err)
			}
			tips, err := c.s.Repo.ListPrefix(ctx, refs.Root(kind))
			if err != nil {
				t.Fatal(err)
			}
			objects := make(map[model.SHA][]byte, len(tips))
			for _, tip := range tips {
				path := labeledListObjectPath(c, tip)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				objects[tip] = data
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			clearClientCache(t, c, "folds-v6")
			fresh, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			var reads atomic.Int64
			fresh.s.Repo.BackfillWith(func(ctx context.Context, tip model.SHA) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				data, ok := objects[tip]
				if !ok {
					return fmt.Errorf("unexpected entity body %s", tip)
				}
				reads.Add(1)
				return os.WriteFile(labeledListObjectPath(fresh, tip), data, 0o600)
			})
			got, err := labeledListIDs(ctx, fresh, kind, []string{"target"})
			if err != nil || !slices.Equal(got, []model.EntityID{selected}) {
				t.Fatalf("selected list = %v, %v; want %s", got, err, selected)
			}
			if got := reads.Load(); got != 1 {
				t.Fatalf("read %d authoritative entity bodies for one selected %s among %d; want 1", got, kind, len(objects))
			}
		})
	}
}

func createLabeledListEntity(t *testing.T, c *Client, kind model.Kind, title string, labels []string) model.EntityID {
	t.Helper()
	body := strings.Repeat("large body\n", 4096)
	var ops []model.Op
	switch kind {
	case model.KindTask:
		ops = []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Description: body, Type: model.TypeTask, Priority: 2, Labels: labels}}
	case model.KindLog:
		ops = []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: title, Tags: labels}, model.AppendEntry{Text: body}}
	case model.KindInvestigation:
		ops = []model.Op{model.CreateInvestigation{Nonce: model.NewNonce(), Title: title, Premise: body, Tags: labels}}
	default:
		t.Fatalf("unsupported fixture kind %s", kind)
	}
	snapshot, err := c.s.Create(t.Context(), ops)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.EntityID()
}

func labeledListObjectPath(c *Client, tip model.SHA) string {
	return filepath.Join(c.s.CommonDir(), "objects", string(tip)[:2], string(tip)[2:])
}

func labeledListIDs(ctx context.Context, c *Client, kind model.Kind, labels []string) ([]model.EntityID, error) {
	var ids []model.EntityID
	switch kind {
	case model.KindTask:
		values, err := c.Tasks(ctx, TaskFilter{Labels: labels})
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			ids = append(ids, value.ID)
		}
	case model.KindLog:
		values, err := c.Logs(ctx, LogFilter{Labels: labels})
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			ids = append(ids, value.ID)
		}
	case model.KindInvestigation:
		values, err := c.Investigations(ctx, InvestigationFilter{Labels: labels})
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			ids = append(ids, value.ID)
		}
	}
	return ids, nil
}
