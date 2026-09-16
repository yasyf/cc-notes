package cli

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lgShow(t *testing.T, dir, id string) ledgerDTO {
	t.Helper()
	return spJSON[ledgerDTO](t, spMust(t, dir, "ledger", "show", id, "--json"))
}

func lgRowFields(t *testing.T, l ledgerDTO, key string) map[string]string {
	t.Helper()
	for _, row := range l.Rows {
		if row.Key == key {
			return row.Fields
		}
	}
	t.Fatalf("ledger has no row keyed %q; rows = %+v", key, l.Rows)
	return nil
}

func TestLedgerAddDeclaresColumns(t *testing.T) {
	dir := spInitRepo(t)
	out := spMust(t, dir, "ledger", "add", "Open PRs", "--body", "one row per open pull request",
		"--column", "head", "--column", "test_state", "--label", "landing", "--json")
	ack := spJSON[ledgerSummaryDTO](t, out)
	if ack.Title != "Open PRs" || ack.Status != "active" || ack.RowCount != 0 {
		t.Errorf("add ack = %+v, want Open PRs/active/0 rows", ack)
	}
	if strings.Contains(out, `"rows"`) || strings.Contains(out, `"description"`) {
		t.Errorf("add ack %q carries the rows or description; a write acknowledgement is a summary", out)
	}

	l := lgShow(t, dir, ack.ID)
	if strings.Join(l.Columns, ",") != "head,test_state" {
		t.Errorf("columns = %v, want the declared order", l.Columns)
	}
	if l.Description != "one row per open pull request" {
		t.Errorf("description = %q", l.Description)
	}
}

// TestLedgerSyncMergesUnnamedFields drives the refresh path end to end through
// the CLI: a hand-set hold reason survives a sync that names only the fields
// the external system reports.
func TestLedgerSyncMergesUnnamedFields(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID

	seed := `[{"key":"21052","fields":{"head":"e8ad886","test_state":"failure"}}]`
	if _, _, err := spRun(t, dir, seed, "ledger", "sync", id, "--json"); err != nil {
		t.Fatalf("ledger sync seed: %v", err)
	}
	spMust(t, dir, "ledger", "row", "set", id, "--key", "21052", "--field", "hold_reason=owner review", "--json")

	refresh := `[{"key":"21052","fields":{"head":"0ac5ed7","test_state":"success"}}]`
	out, _, err := spRun(t, dir, refresh, "ledger", "sync", id, "--json")
	if err != nil {
		t.Fatalf("ledger sync refresh: %v", err)
	}
	ack := spJSON[ledgerSyncDTO](t, out)
	if ack.Updated != 1 || ack.Added != 0 || ack.Removed != 0 {
		t.Errorf("sync ack = %+v, want one update", ack)
	}

	want := map[string]string{"head": "0ac5ed7", "test_state": "success", "hold_reason": "owner review"}
	if got := lgRowFields(t, lgShow(t, dir, id), "21052"); !maps.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

// TestLedgerSyncPruneDropsOmittedRows pins that only --prune removes a row the
// set omits, so a partial refresh cannot silently empty the register.
func TestLedgerSyncPruneDropsOmittedRows(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	seed := `[{"key":"a","fields":{"state":"success"}},{"key":"b","fields":{"state":"failure"}}]`
	if _, _, err := spRun(t, dir, seed, "ledger", "sync", id, "--json"); err != nil {
		t.Fatalf("ledger sync seed: %v", err)
	}

	subset := `[{"key":"a","fields":{"state":"success"}}]`
	if _, _, err := spRun(t, dir, subset, "ledger", "sync", id, "--json"); err != nil {
		t.Fatalf("ledger sync subset: %v", err)
	}
	if got := len(lgShow(t, dir, id).Rows); got != 2 {
		t.Fatalf("rows = %d after a prune-less sync, want 2", got)
	}

	out, _, err := spRun(t, dir, subset, "ledger", "sync", id, "--prune", "--json")
	if err != nil {
		t.Fatalf("ledger sync prune: %v", err)
	}
	if ack := spJSON[ledgerSyncDTO](t, out); ack.Removed != 1 {
		t.Errorf("sync ack = %+v, want one removal", ack)
	}
	l := lgShow(t, dir, id)
	if len(l.Rows) != 1 || l.Rows[0].Key != "a" {
		t.Errorf("rows = %+v, want only a", l.Rows)
	}
}

func TestLedgerSyncReadsAFile(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	path := filepath.Join(t.TempDir(), "rows.json")
	if err := os.WriteFile(path, []byte(`[{"key":"a","fields":{"state":"success"}}]`), 0o644); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	ack := spJSON[ledgerSyncDTO](t, spMust(t, dir, "ledger", "sync", id, "--file", path, "--json"))
	if ack.Added != 1 || ack.RowCount != 1 {
		t.Errorf("sync ack = %+v, want one row added from the file", ack)
	}
}

func TestLedgerShowAndRowListFilterOnWhere(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--column", "state", "--json")).ID
	seed := `[{"key":"a","fields":{"state":"failure"}},{"key":"b","fields":{"state":"success"}}]`
	if _, _, err := spRun(t, dir, seed, "ledger", "sync", id, "--json"); err != nil {
		t.Fatalf("ledger sync seed: %v", err)
	}

	filtered := spJSON[ledgerDTO](t, spMust(t, dir, "ledger", "show", id, "--where", "state=failure", "--json"))
	if len(filtered.Rows) != 1 || filtered.Rows[0].Key != "a" {
		t.Errorf("show --where rows = %+v, want only a", filtered.Rows)
	}

	lean := spMust(t, dir, "ledger", "row", "list", id, "--where", "state=failure")
	if strings.TrimSpace(lean) != "a\tfailure" {
		t.Errorf("row list = %q, want the key and one cell per column", lean)
	}
}

func TestLedgerRowSetReplaceDropsUnnamedFields(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	spMust(t, dir, "ledger", "row", "set", id, "--key", "a", "--field", "state=failure", "--field", "lane=watch", "--json")
	spMust(t, dir, "ledger", "row", "set", id, "--key", "a", "--field", "state=success", "--replace", "--json")
	want := map[string]string{"state": "success"}
	if got := lgRowFields(t, lgShow(t, dir, id), "a"); !maps.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

func TestLedgerRowRmUnknownKeyFails(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	if _, _, err := spRun(t, dir, "", "ledger", "row", "rm", id, "--key", "nope", "--json"); err == nil {
		t.Fatal("removing an absent row succeeded, want not-found")
	}
}

func TestLedgerArchiveBlocksRowWrites(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	spMust(t, dir, "ledger", "archive", id, "--json")
	_, _, err := spRun(t, dir, "", "ledger", "row", "set", id, "--key", "a", "--field", "state=x", "--json")
	if err == nil {
		t.Fatal("row set on an archived ledger succeeded, want a conflict")
	}
	if !strings.Contains(err.Error(), "archived") {
		t.Errorf("err = %v, want it to name the archived ledger", err)
	}
	spMust(t, dir, "ledger", "activate", id, "--json")
	spMust(t, dir, "ledger", "row", "set", id, "--key", "a", "--field", "state=x", "--json")
}

// TestLedgerShowRendersATable pins the human view: a key column, the declared
// columns, and a dash for a field a row does not carry.
func TestLedgerShowRendersATable(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs",
		"--column", "state", "--column", "lane", "--json")).ID
	spMust(t, dir, "ledger", "row", "set", id, "--key", "21052", "--field", "state=failure", "--json")
	out := spMust(t, dir, "ledger", "show", id)
	for _, want := range []string{"rows: 1", "key    state    lane", "21052  failure  -"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestLedgerListCountsRows(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	spMust(t, dir, "ledger", "row", "set", id, "--key", "a", "--field", "state=x", "--json")
	lean := spMust(t, dir, "ledger", "list")
	if !strings.Contains(lean, "\tactive\t1\tOpen PRs") {
		t.Errorf("list = %q, want the status, row count, and title", lean)
	}
}

func TestLedgerRowSetRejectsMalformedField(t *testing.T) {
	dir := spInitRepo(t)
	id := spJSON[ledgerSummaryDTO](t, spMust(t, dir, "ledger", "add", "Open PRs", "--json")).ID
	if _, _, err := spRun(t, dir, "", "ledger", "row", "set", id, "--key", "a", "--field", "novalue"); err == nil {
		t.Fatal("a --field without = succeeded, want a usage error")
	}
}
