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

func TestDocLabelsLogicalReadCost(t *testing.T) {
	c, _ := newWBClient(t)
	body := strings.Repeat("body", 1<<16)
	var selected model.Doc
	for i := range 33 {
		labels := []string{"shared"}
		if i == 0 {
			labels = append(labels, "target")
		}
		doc, reused, err := c.CreateDoc(t.Context(), DocSpec{
			Title: fmt.Sprintf("private doc %02d", i),
			Body:  body,
			When:  "private trigger",
			Tags:  labels,
		})
		if err != nil || reused {
			t.Fatalf("private doc %d: reused=%v error=%v", i, reused, err)
		}
		if i == 0 {
			selected = doc
		}
	}
	if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindDoc); err != nil || !hit {
		t.Fatalf("prime existing doc metadata: hit=%v error=%v", hit, err)
	}
	filter := DocumentFilter{Labels: []string{"target", "shared"}}
	want := []model.Doc{selected}
	warm, err := c.Docs(t.Context(), filter)
	if err != nil || !reflect.DeepEqual(warm, want) {
		t.Fatalf("warm result differs from complete selected doc: %v", err)
	}
	before := docLabelsLogicalReadBytes(t)
	got, err := c.Docs(t.Context(), filter)
	readBytes := docLabelsLogicalReadBytes(t) - before
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("measured result differs from complete selected doc: %v", err)
	}
	limit := uint64(2 << 20)
	t.Logf("selected=1 unselected=32 body_bytes=%d full_snapshot=PASS logical_rchar_bytes=%d max_logical_rchar=%d", len(body), readBytes, limit)
	if readBytes > limit {
		t.Fatalf("DOC_LABEL_CORPUS_READ_REGRESSION: logical reads %d exceed %d for one selected doc", readBytes, limit)
	}
}

func docLabelsLogicalReadBytes(t *testing.T) uint64 {
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
