//go:build fuse

package fusefs

import (
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// TestCodecsExhaustive asserts every model.Kind has a codec keyed by itself and
// that codecs holds no extra entries: a new kind without a codec fails here.
func TestCodecsExhaustive(t *testing.T) {
	kinds := model.Kinds()
	if len(codecs) != len(kinds) {
		t.Fatalf("codecs has %d entries, want %d kinds", len(codecs), len(kinds))
	}
	for _, kind := range kinds {
		if got := codecOf(kind).Kind(); got != kind {
			t.Errorf("codecOf(%s).Kind() = %s, want %s", kind, got, kind)
		}
	}
}
