package fold_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func TestLedgerIndexedRemoveReinsertAndReplace(t *testing.T) {
	chain := ledgerChain(
		model.UpsertRow{Key: "a", Fields: map[string]string{"value": "old", "retained": "yes"}, Position: "z"},
		model.UpsertRow{Key: "b", Fields: map[string]string{"value": "b0"}, Position: "a"},
		model.UpsertRow{Key: "c", Fields: map[string]string{"value": "c0"}, Position: "b"},
		model.RemoveRow{Key: "b"},
		model.UpsertRow{Key: "c", Fields: map[string]string{"value": "c1"}},
		model.UpsertRow{Key: "a", Fields: map[string]string{"value": "a1"}, Replace: true},
		model.UpsertRow{Key: "b", Fields: map[string]string{"value": "b1"}, Position: "0"},
		model.RemoveRow{Key: "missing"},
		model.UpsertRow{Key: "c", Fields: map[string]string{"extra": "1"}, Position: "y"},
	)
	got, err := fold.Ledger(chain)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.LedgerRow{
		{Key: "b", Fields: map[string]string{"value": "b1"}, Position: "0", UpdatedAt: 800, UpdatedBy: "actor"},
		{Key: "c", Fields: map[string]string{"value": "c1", "extra": "1"}, Position: "y", UpdatedAt: 1000, UpdatedBy: "actor"},
		{Key: "a", Fields: map[string]string{"value": "a1"}, Position: "z", UpdatedAt: 700, UpdatedBy: "actor"},
	}
	if !reflect.DeepEqual(got.Rows, want) {
		t.Fatalf("rows = %+v, want %+v", got.Rows, want)
	}
}

func TestLedgerIndexedWalkMatchesEachPrefix(t *testing.T) {
	chain := ledgerChain(
		model.UpsertRow{Key: "z", Fields: map[string]string{"value": "z0"}, Position: "z"},
		model.UpsertRow{Key: "a", Fields: map[string]string{"value": "a0"}, Position: "a"},
		model.UpsertRow{Key: "m", Fields: map[string]string{"value": "m0"}, Position: "m"},
		model.UpsertRow{Key: "z", Fields: map[string]string{"value": "z1"}},
		model.RemoveRow{Key: "a"},
		model.UpsertRow{Key: "m", Fields: map[string]string{"value": "m1"}},
		model.UpsertRow{Key: "a", Fields: map[string]string{"value": "a1"}, Position: "0"},
		model.UpsertRow{Key: "z", Fields: map[string]string{}, Replace: true},
	)
	seen := 0
	err := fold.Walk(chain, func(step fold.Step) error {
		seen++
		want, err := fold.Ledger(chain[:seen])
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(step.Snapshot, want) {
			t.Fatalf("prefix %d: snapshot = %+v, want %+v", seen, step.Snapshot, want)
		}
		return nil
	})
	if err != nil || seen != len(chain) {
		t.Fatalf("walk visited %d/%d commits: %v", seen, len(chain), err)
	}
}

func TestLedgerIndexedCheckpointKeepsFirstMatchingRowAndSeed(t *testing.T) {
	seed := model.Ledger{
		ID: "aaa", Title: "lg", Status: model.LedgerActive, Columns: []string{},
		Rows: []model.LedgerRow{
			{Key: "k", Fields: map[string]string{"which": "first"}, Position: "z", UpdatedAt: 100, UpdatedBy: "alice"},
			{Key: "k", Fields: map[string]string{"which": "second"}, Position: "a", UpdatedAt: 100, UpdatedBy: "alice"},
			{Key: "x", Fields: map[string]string{"value": "x"}, Position: "m", UpdatedAt: 100, UpdatedBy: "alice"},
		},
		Comments: []model.Comment{}, Author: "alice", CreatedAt: 100, UpdatedAt: 100,
	}
	before, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Checkpoint{EntityID: "aaa", State: seed, CoversLamport: 1, CoversShas: []model.SHA{"aaa"}}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.UpsertRow{Key: "k", Fields: map[string]string{"changed": "yes"}}),
		mk("ddd", []string{"ccc"}, "dave", 400, 4, model.RemoveRow{Key: "k"}),
		mk("eee", []string{"ddd"}, "erin", 500, 5, model.UpsertRow{Key: "k", Fields: map[string]string{"after": "yes"}}),
	}
	if _, err := fold.Ledger(chain[:2]); err != nil {
		t.Fatal(err)
	}
	got, err := fold.Ledger(chain)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.LedgerRow{
		{Key: "k", Fields: map[string]string{"which": "second", "after": "yes"}, Position: "a", UpdatedAt: 500, UpdatedBy: "erin"},
		{Key: "x", Fields: map[string]string{"value": "x"}, Position: "m", UpdatedAt: 100, UpdatedBy: "alice"},
	}
	after, err := json.Marshal(seed)
	if err != nil || string(after) != string(before) || !reflect.DeepEqual(got.Rows, want) {
		t.Fatalf("rows=%+v seed_changed=%t: %v", got.Rows, string(after) != string(before), err)
	}
}

func ledgerFoldCostChain(rows, history int) []model.PackCommit {
	ops := make([]model.Op, rows)
	for i := range rows {
		ops[i] = model.UpsertRow{Key: fmt.Sprintf("row-%08d", i), Fields: map[string]string{"value": strconv.Itoa(i)}, Position: fmt.Sprintf("%08d", rows-i)}
	}
	chain := make([]model.PackCommit, 0, 2+history)
	chain = append(chain,
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "cost"}),
		mk("rows", []string{"aaa"}, "bob", 200, 2, ops...),
	)
	for i := range history {
		chain = append(chain, mk(fmt.Sprintf("history-%08d", i), []string{string(chain[len(chain)-1].SHA)}, "carol", int64(300+i), uint64(3+i), model.UpsertRow{Key: fmt.Sprintf("row-%08d", rows-1), Fields: map[string]string{"pass": strconv.Itoa(i)}}))
	}
	return chain
}

func reportLedgerFoldCost(b *testing.B, snapshot model.Ledger, rows int) {
	b.Helper()
	if len(snapshot.Rows) != rows || snapshot.Rows[0].Key != fmt.Sprintf("row-%08d", rows-1) || snapshot.Rows[rows-1].Key != "row-00000000" {
		b.Fatalf("incorrect full ledger: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(rows), "rows")
	b.ReportMetric(float64(len(encoded)), "snapshot-bytes")
	b.Logf("ledger-fixture\t%s\t%x\t%d", b.Name(), sha256.Sum256(encoded), len(encoded))
}

func BenchmarkLedgerFoldRows(b *testing.B) {
	for _, rows := range []int{32, 10000} {
		b.Run(strconv.Itoa(rows), func(b *testing.B) {
			chain := ledgerFoldCostChain(rows, 0)
			b.ReportAllocs()
			b.ResetTimer()
			var got model.Ledger
			var err error
			for range b.N {
				got, err = fold.Ledger(chain)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reportLedgerFoldCost(b, got, rows)
		})
	}
}

func BenchmarkLedgerFoldHistory(b *testing.B) {
	for _, rows := range []int{32, 10000} {
		for _, history := range []int{32, 1024} {
			b.Run(fmt.Sprintf("rows=%d/history=%d", rows, history), func(b *testing.B) {
				chain := ledgerFoldCostChain(rows, history)
				b.ReportAllocs()
				b.ResetTimer()
				var got model.Ledger
				var err error
				for range b.N {
					got, err = fold.Ledger(chain)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				reportLedgerFoldCost(b, got, rows)
			})
		}
	}
}

func BenchmarkLedgerExtendRows(b *testing.B) {
	for _, rows := range []int{32, 10000} {
		b.Run(strconv.Itoa(rows), func(b *testing.B) {
			chain := ledgerFoldCostChain(rows, 0)
			base, err := fold.Ledger(chain)
			if err != nil {
				b.Fatal(err)
			}
			commit := mk("append", []string{"rows"}, "carol", 300, 3, model.UpsertRow{Key: fmt.Sprintf("row-%08d", rows-1), Fields: map[string]string{"pass": "append"}})
			b.ReportAllocs()
			b.ResetTimer()
			var got model.Snapshot
			for range b.N {
				got, err = fold.Extend(base, commit)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reportLedgerFoldCost(b, got.(model.Ledger), rows)
		})
	}
}
