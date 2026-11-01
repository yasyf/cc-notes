package store

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestLoadFallbackAfterPartialExtendMatchesFullFold(t *testing.T) {
	s := initStore(t)
	root := create(t, s, ledgerOps("partial strict refusal")).(model.Ledger)
	large := strings.Repeat("large-state:", 7000)
	head := loadParentCommit(t, s, []model.SHA{root.Head}, 2,
		model.UpsertRow{Key: "only-row", Fields: map[string]string{"state": large}, Position: "a"},
		model.SetBody{Body: "wrong kind"},
	)
	want, _, err := loadParentFold(t, s, head)
	if err != nil {
		t.Fatal(err)
	}
	ledger := want.(model.Ledger)
	if ledger.SkippedOps != 1 || len(ledger.Rows) != 1 || ledger.Rows[0].Fields["state"] != large {
		t.Fatalf("partial fixture = %#v", ledger)
	}
	got, err := s.LoadAt(t.Context(), head)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %#v, %v; full fold = %#v", got, err, want)
	}
	if _, hit := s.cache.get(head); hit {
		t.Fatal("partial snapshot was cached")
	}
}
