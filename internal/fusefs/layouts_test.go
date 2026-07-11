package fusefs

import (
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// TestLayoutsExhaustive pins the untagged layouts table to exactly model.Kinds()
// and round-trips every kind through the exported ParsePath: a missing row fails
// the cardinality check, and a wrong dir or ext fails the round-trip.
func TestLayoutsExhaustive(t *testing.T) {
	kinds := model.Kinds()
	if len(layouts) != len(kinds) {
		t.Fatalf("layouts has %d entries, want %d kinds", len(layouts), len(kinds))
	}
	for _, kind := range kinds {
		layout, ok := layouts[kind]
		if !ok {
			t.Errorf("layouts missing kind %s", kind)
			continue
		}
		if layout.dir == "" || layout.ext == "" {
			t.Errorf("layouts[%s] = %+v, want non-empty dir and ext", kind, layout)
			continue
		}
		if node, err := ParsePath(layout.dir); err != nil {
			t.Errorf("ParsePath(%q): %v", layout.dir, err)
		} else if kd, ok := node.(KindDir); !ok || kd.Kind != kind {
			t.Errorf("ParsePath(%q) = %#v, want KindDir{Kind:%s}", layout.dir, node, kind)
		}
		file := layout.dir + "/abcdef0" + layout.ext
		if node, err := ParsePath(file); err != nil {
			t.Errorf("ParsePath(%q): %v", file, err)
		} else if ef, ok := node.(EntityFile); !ok || ef.Kind != kind || ef.ShortID != "abcdef0" {
			t.Errorf("ParsePath(%q) = %#v, want EntityFile{Kind:%s, ShortID:abcdef0}", file, node, kind)
		}
	}
}
