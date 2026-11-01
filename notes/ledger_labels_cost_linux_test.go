package notes

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestLedgerLabelsLogicalReadCost(t *testing.T) {
	c, _ := newWBClient(t)
	body := strings.Repeat("body", 1<<16)
	var selected model.Ledger
	for i := range 33 {
		labels := []string{"shared"}
		if i == 0 {
			labels = append(labels, "target")
		}
		ledger, reused, err := c.CreateLedger(t.Context(), LedgerSpec{
			Title:   fmt.Sprintf("private ledger %02d", i),
			Columns: []string{"state"},
			Labels:  labels,
			Rows:    []RowSpec{{Key: "row", Fields: map[string]string{"state": body}}},
		})
		if err != nil || reused {
			t.Fatalf("private ledger %d: reused=%v error=%v", i, reused, err)
		}
		if i == 0 {
			selected = ledger
		}
	}
	if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindLedger); err != nil || !hit {
		t.Fatalf("prime existing ledger metadata: hit=%v error=%v", hit, err)
	}
	filter := LedgerFilter{Labels: []string{"target", "shared"}}
	want := []model.Ledger{selected}
	warm, err := c.Ledgers(t.Context(), filter)
	if err != nil || !reflect.DeepEqual(warm, want) {
		t.Fatalf("warm result differs from complete selected ledger: %v", err)
	}
	before := labeledLedgerLogicalReadBytes(t)
	got, err := c.Ledgers(t.Context(), filter)
	readBytes := labeledLedgerLogicalReadBytes(t) - before
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("measured result differs from complete selected ledger: %v", err)
	}
	limit := uint64(2 << 20)
	t.Logf("selected=1 unselected=32 body_bytes=%d full_snapshot=PASS logical_rchar_bytes=%d max_logical_rchar=%d", len(body), readBytes, limit)
	if readBytes > limit {
		t.Fatalf("LEDGER_LABEL_CORPUS_READ_REGRESSION: logical reads %d exceed %d for one selected ledger", readBytes, limit)
	}
}

func labeledLedgerLogicalReadBytes(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "rchar:"); ok {
			count, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return count
		}
	}
	t.Fatal("/proc/self/io has no rchar counter")
	return 0
}
