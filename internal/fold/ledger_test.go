package fold_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func ledgerChain(ops ...model.Op) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(ops))
	chain = append(chain, mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"}))
	for i, op := range ops {
		sha := fmt.Sprintf("c%02d", i)
		chain = append(chain, mk(sha, []string{string(chain[len(chain)-1].SHA)}, "actor", 200+100*int64(i), uint64(i)+2, op))
	}
	return chain
}

func rowByKey(t *testing.T, l model.Ledger, key string) model.LedgerRow {
	t.Helper()
	for _, row := range l.Rows {
		if row.Key == key {
			return row
		}
	}
	t.Fatalf("ledger has no row keyed %q; rows = %+v", key, l.Rows)
	return model.LedgerRow{}
}

func TestFoldLedgerLifecycle(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{
			Nonce:       "n",
			Title:       "Open PRs",
			Description: "one row per open pull request",
			Columns:     []string{"head", "state"},
			Labels:      []string{"landing"},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.UpsertRow{Key: "21052", Fields: map[string]string{"head": "e8ad886", "state": "failure"}, Position: "a"},
			model.UpsertRow{Key: "21020", Fields: map[string]string{"head": "e7b8cd6", "state": "success"}, Position: "i"},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.UpsertRow{Key: "21052", Fields: map[string]string{"hold_reason": "waiting on 21020"}},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.UpsertRow{Key: "21052", Fields: map[string]string{"state": "success"}},
		),
		mk("eee", []string{"ddd"}, "erin", 500, 5,
			model.SetColumns{Columns: []string{"head", "state", "hold_reason"}},
			model.RemoveRow{Key: "21020"},
		),
		mk("fff", []string{"eee"}, "frank", 600, 6, model.AddComment{Body: "landed"}),
		mk("ggg", []string{"fff"}, "grace", 700, 7, model.SetLedgerStatus{Status: model.LedgerArchived}),
	}
	l, err := fold.Ledger(chain)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if l.Title != "Open PRs" || l.Description != "one row per open pull request" {
		t.Errorf("title/description = %q/%q", l.Title, l.Description)
	}
	if want := []string{"head", "state", "hold_reason"}; !reflect.DeepEqual(l.Columns, want) {
		t.Errorf("Columns = %v, want %v", l.Columns, want)
	}
	if l.Status != model.LedgerArchived || l.ArchivedAt != 700 {
		t.Errorf("status/archived = %s/%d, want archived/700", l.Status, l.ArchivedAt)
	}
	if len(l.Rows) != 1 {
		t.Fatalf("rows = %+v, want only 21052 after the remove", l.Rows)
	}
	row := rowByKey(t, l, "21052")
	want := map[string]string{"head": "e8ad886", "state": "success", "hold_reason": "waiting on 21020"}
	if !reflect.DeepEqual(row.Fields, want) {
		t.Errorf("fields = %v, want %v", row.Fields, want)
	}
	if row.UpdatedAt != 400 || row.UpdatedBy != "dave" {
		t.Errorf("row stamp = %d/%s, want 400/dave", row.UpdatedAt, row.UpdatedBy)
	}
	if l.UpdatedAt != 700 {
		t.Errorf("ledger UpdatedAt = %d, want 700", l.UpdatedAt)
	}
}

// TestFoldLedgerUpsertMergesUnnamedFields is the field-merge contract the
// refresh path depends on: a pass that names only the fields it re-read leaves
// an operator's hold reason standing. Without it every refresh would erase the
// hand-written columns and the ledger could hold only what the external system
// reports.
func TestFoldLedgerUpsertMergesUnnamedFields(t *testing.T) {
	l, err := fold.Ledger(ledgerChain(
		model.UpsertRow{Key: "k", Fields: map[string]string{"state": "failure", "hold_reason": "owner review"}, Position: "a"},
		model.UpsertRow{Key: "k", Fields: map[string]string{"state": "success"}},
	))
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	row := rowByKey(t, l, "k")
	if got := row.Fields["hold_reason"]; got != "owner review" {
		t.Errorf("hold_reason = %q, want it to survive a refresh that did not name it", got)
	}
	if got := row.Fields["state"]; got != "success" {
		t.Errorf("state = %q, want success", got)
	}
}

// TestFoldLedgerUpsertReplaceDropsUnnamedFields pins the other half: Replace is
// the only way a field leaves a row short of removing it.
func TestFoldLedgerUpsertReplaceDropsUnnamedFields(t *testing.T) {
	l, err := fold.Ledger(ledgerChain(
		model.UpsertRow{Key: "k", Fields: map[string]string{"state": "failure", "hold_reason": "owner review"}, Position: "a"},
		model.UpsertRow{Key: "k", Fields: map[string]string{"state": "success"}, Replace: true},
	))
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	row := rowByKey(t, l, "k")
	if want := map[string]string{"state": "success"}; !reflect.DeepEqual(row.Fields, want) {
		t.Errorf("fields = %v, want %v", row.Fields, want)
	}
}

// TestFoldLedgerUpsertKeepsPositionWhenUnset pins that a refresh reordering
// nothing cannot renumber the table: an upsert with no position leaves the
// row where it was.
func TestFoldLedgerUpsertKeepsPositionWhenUnset(t *testing.T) {
	l, err := fold.Ledger(ledgerChain(
		model.UpsertRow{Key: "a", Fields: map[string]string{"n": "1"}, Position: "a"},
		model.UpsertRow{Key: "b", Fields: map[string]string{"n": "2"}, Position: "i"},
		model.UpsertRow{Key: "a", Fields: map[string]string{"n": "3"}},
	))
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if got := []string{l.Rows[0].Key, l.Rows[1].Key}; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("row order = %v, want [a b]", got)
	}
	if got := rowByKey(t, l, "a").Position; got != "a" {
		t.Errorf("position = %q, want the original a", got)
	}
}

// TestFoldLedgerRowsSortByPositionThenKey pins the total order rows fold into,
// so two replicas that applied the same ops render the same table.
func TestFoldLedgerRowsSortByPositionThenKey(t *testing.T) {
	l, err := fold.Ledger(ledgerChain(
		model.UpsertRow{Key: "z", Fields: map[string]string{}, Position: "i"},
		model.UpsertRow{Key: "b", Fields: map[string]string{}, Position: "a"},
		model.UpsertRow{Key: "a", Fields: map[string]string{}, Position: "i"},
	))
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	got := make([]string, 0, len(l.Rows))
	for _, row := range l.Rows {
		got = append(got, row.Key)
	}
	if want := []string{"b", "a", "z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("row order = %v, want %v", got, want)
	}
}

// TestFoldLedgerConcurrentFieldWritesConverge drives two branches that touch
// different fields of one row through the merge. Field-wise last-write-wins is
// what lets a refresh lane and a triage lane write the same ledger without
// either erasing the other.
func TestFoldLedgerConcurrentFieldWritesConverge(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.UpsertRow{Key: "k", Fields: map[string]string{"state": "failure"}, Position: "a"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.UpsertRow{Key: "k", Fields: map[string]string{"next_action": "rebase"}}),
		mk("ddd", []string{"bbb"}, "dave", 300, 3,
			model.UpsertRow{Key: "k", Fields: map[string]string{"lane": "watch-21052"}}),
		mk("eee", []string{"ccc", "ddd"}, "erin", 400, 4, model.AddComment{Body: "merged"}),
	}
	l, err := fold.Ledger(chain)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	row := rowByKey(t, l, "k")
	want := map[string]string{"state": "failure", "next_action": "rebase", "lane": "watch-21052"}
	if !reflect.DeepEqual(row.Fields, want) {
		t.Errorf("fields = %v, want the union %v", row.Fields, want)
	}
}

// TestFoldLedgerCheckpointSeedIsNotAliased folds a prefix and then the whole
// chain over one decoded history. A shallow seed would share the checkpoint's
// field maps, so the first fold's merge would corrupt the second's starting
// point — the aliasing cloneRows exists to prevent.
func TestFoldLedgerCheckpointSeedIsNotAliased(t *testing.T) {
	seed := model.Ledger{
		ID: "aaa", Title: "lg", Status: model.LedgerActive,
		Columns: []string{"state"},
		Rows: []model.LedgerRow{{
			Key: "k", Fields: map[string]string{"state": "failure"}, Position: "a", UpdatedAt: 100, UpdatedBy: "alice",
		}},
		Comments: []model.Comment{}, Author: "alice", CreatedAt: 100, UpdatedAt: 100,
	}
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Checkpoint{EntityID: "aaa", State: seed, CoversLamport: 1, CoversShas: []model.SHA{"aaa"}}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.UpsertRow{Key: "k", Fields: map[string]string{"state": "success"}}),
	}
	if _, err := fold.Ledger(chain[:2]); err != nil {
		t.Fatalf("prefix fold: %v", err)
	}
	if _, err := fold.Ledger(chain); err != nil {
		t.Fatalf("full fold: %v", err)
	}
	if got := seed.Rows[0].Fields["state"]; got != "failure" {
		t.Fatalf("checkpoint state mutated by a fold: state = %q, want failure", got)
	}
}

func TestFoldLedgerRejectsForeignChain(t *testing.T) {
	chain := []model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{Nonce: "n", Title: "rb"})}
	if _, err := fold.Ledger(chain); err == nil {
		t.Fatal("folding a runbook chain as a ledger succeeded, want ErrKindMismatch")
	}
}
