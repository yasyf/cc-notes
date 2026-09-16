package notes_test

import (
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newLedger(t *testing.T, c *notes.Client, spec notes.LedgerSpec) model.Ledger {
	t.Helper()
	l, _, err := c.CreateLedger(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateLedger: %v", err)
	}
	return l
}

func ledgerFields(t *testing.T, l model.Ledger, key string) map[string]string {
	t.Helper()
	i := notes.RowIndex(l, key)
	if i < 0 {
		t.Fatalf("ledger has no row keyed %q; rows = %+v", key, l.Rows)
	}
	return l.Rows[i].Fields
}

func TestCreateLedgerSeedsRowsInOrder(t *testing.T) {
	c, _ := newClient(t)
	l := newLedger(t, c, notes.LedgerSpec{
		Title:   "Open PRs",
		Columns: []string{"state"},
		Rows: []notes.RowSpec{
			{Key: "b", Fields: map[string]string{"state": "failure"}},
			{Key: "a", Fields: map[string]string{"state": "success"}},
		},
	})
	if got := []string{l.Rows[0].Key, l.Rows[1].Key}; !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("row order = %v, want slice order [b a]", got)
	}
	if l.Status != model.LedgerActive {
		t.Errorf("status = %s, want active", l.Status)
	}
}

// TestSyncRowsMergesAndPreservesUnnamedFields is the contract the refresh path
// rests on: the pass writes only what it re-read, and the operator-set columns
// stand. Without it every twenty-minute refresh would erase the hold reason
// that explains why a row is parked.
func TestSyncRowsMergesAndPreservesUnnamedFields(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs"})

	l, _, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{
		{Key: "21052", Fields: map[string]string{"head": "e8ad886", "state": "failure"}},
	}, false)
	if err != nil {
		t.Fatalf("SyncRows seed: %v", err)
	}
	if l, err = c.SetRow(ctx, l.ID, "21052", map[string]string{"hold_reason": "owner review"}, false); err != nil {
		t.Fatalf("SetRow: %v", err)
	}
	l, result, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{
		{Key: "21052", Fields: map[string]string{"head": "0ac5ed7", "state": "success"}},
	}, false)
	if err != nil {
		t.Fatalf("SyncRows refresh: %v", err)
	}
	if result.Updated != 1 || result.Added != 0 || result.Removed != 0 {
		t.Errorf("result = %+v, want one update", result)
	}
	want := map[string]string{"head": "0ac5ed7", "state": "success", "hold_reason": "owner review"}
	if got := ledgerFields(t, l, "21052"); !maps.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

// TestSyncRowsPruneDropsOmittedRows pins how a unit that left the external
// system leaves the ledger, and that it does so only under --prune.
func TestSyncRowsPruneDropsOmittedRows(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs", Rows: []notes.RowSpec{
		{Key: "a", Fields: map[string]string{"state": "success"}},
		{Key: "b", Fields: map[string]string{"state": "failure"}},
	}})

	kept, result, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{{Key: "a", Fields: map[string]string{"state": "success"}}}, false)
	if err != nil {
		t.Fatalf("SyncRows without prune: %v", err)
	}
	if len(kept.Rows) != 2 || result.Removed != 0 {
		t.Fatalf("rows = %d, removed = %d; a prune-less sync must drop nothing", len(kept.Rows), result.Removed)
	}
	pruned, result, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{{Key: "a", Fields: map[string]string{"state": "success"}}}, true)
	if err != nil {
		t.Fatalf("SyncRows with prune: %v", err)
	}
	if len(pruned.Rows) != 1 || pruned.Rows[0].Key != "a" || result.Removed != 1 {
		t.Errorf("rows = %+v, removed = %d, want only a", pruned.Rows, result.Removed)
	}
}

// TestSyncRowsNoOpWritesNothing keeps a twenty-minute cadence from appending a
// commit per pass to a ledger nothing moved in.
func TestSyncRowsNoOpWritesNothing(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs", Rows: []notes.RowSpec{
		{Key: "a", Fields: map[string]string{"state": "success"}},
	}})
	again, result, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{{Key: "a", Fields: map[string]string{"state": "success"}}}, true)
	if err != nil {
		t.Fatalf("SyncRows: %v", err)
	}
	if result.Changed() {
		t.Errorf("result = %+v, want a no-op", result)
	}
	if again.Head != l.Head {
		t.Errorf("head moved %s → %s on a no-op sync", l.Head, again.Head)
	}
}

// TestSyncRowsRefusesDuplicateKey stops a caller's two rows for one key from
// silently collapsing into whichever came last.
func TestSyncRowsRefusesDuplicateKey(t *testing.T) {
	c, _ := newClient(t)
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs"})
	_, _, err := c.SyncRows(t.Context(), l.ID, []notes.RowSpec{
		{Key: "a", Fields: map[string]string{"state": "success"}},
		{Key: "a", Fields: map[string]string{"state": "failure"}},
	}, false)
	if !errors.Is(err, notes.ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey", err)
	}
}

func TestSetRowReplaceDropsUnnamedFields(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs", Rows: []notes.RowSpec{
		{Key: "a", Fields: map[string]string{"state": "failure", "hold_reason": "owner review"}},
	}})
	l, err := c.SetRow(ctx, l.ID, "a", map[string]string{"state": "success"}, true)
	if err != nil {
		t.Fatalf("SetRow: %v", err)
	}
	if want := map[string]string{"state": "success"}; !maps.Equal(ledgerFields(t, l, "a"), want) {
		t.Errorf("fields = %v, want %v", ledgerFields(t, l, "a"), want)
	}
}

func TestRemoveRowRejectsUnknownKey(t *testing.T) {
	c, _ := newClient(t)
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs"})
	if _, err := c.RemoveRow(t.Context(), l.ID, "nope"); !errors.Is(err, notes.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestArchivedLedgerRefusesRowWrites(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs"})
	if _, err := c.ArchiveLedger(ctx, l.ID); err != nil {
		t.Fatalf("ArchiveLedger: %v", err)
	}
	var conflict *notes.ConflictError
	if _, err := c.SetRow(ctx, l.ID, "a", map[string]string{"state": "x"}, false); !errors.As(err, &conflict) {
		t.Fatalf("SetRow on an archived ledger: err = %v, want *ConflictError", err)
	}
	if _, _, err := c.SyncRows(ctx, l.ID, []notes.RowSpec{{Key: "a"}}, false); !errors.As(err, &conflict) {
		t.Fatalf("SyncRows on an archived ledger: err = %v, want *ConflictError", err)
	}
}

func TestMatchRowsFiltersOnEveryPair(t *testing.T) {
	c, _ := newClient(t)
	l := newLedger(t, c, notes.LedgerSpec{Title: "Open PRs", Rows: []notes.RowSpec{
		{Key: "a", Fields: map[string]string{"state": "failure", "lane": "x"}},
		{Key: "b", Fields: map[string]string{"state": "failure", "lane": "y"}},
		{Key: "c", Fields: map[string]string{"state": "success", "lane": "x"}},
	}})
	got := notes.MatchRows(l, map[string]string{"state": "failure", "lane": "x"})
	if len(got) != 1 || got[0].Key != "a" {
		t.Errorf("MatchRows = %+v, want only a", got)
	}
	if all := notes.MatchRows(l, nil); len(all) != 3 {
		t.Errorf("MatchRows(nil) = %d rows, want every row", len(all))
	}
}

func TestSearchLedgersMatchesFieldValues(t *testing.T) {
	c, _ := newClient(t)
	newLedger(t, c, notes.LedgerSpec{Title: "Open PRs", Rows: []notes.RowSpec{
		{Key: "21052", Fields: map[string]string{"branch": "infra/accounts-register"}},
	}})
	got, err := c.SearchLedgers(t.Context(), "accounts-register", notes.SearchFilter{Limit: -1})
	if err != nil {
		t.Fatalf("SearchLedgers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("SearchLedgers = %d results, want the ledger whose row carries the branch", len(got))
	}
}
