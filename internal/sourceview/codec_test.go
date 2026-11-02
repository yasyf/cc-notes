package sourceview

import (
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestCodecsExhaustive(t *testing.T) {
	kinds := model.Kinds()
	if len(codecs) != len(kinds) {
		t.Fatalf("codecs has %d entries, want %d kinds", len(codecs), len(kinds))
	}
	for _, kind := range kinds {
		if got := CodecOf(kind).Kind(); got != kind {
			t.Errorf("codecOf(%s).Kind() = %s, want %s", kind, got, kind)
		}
	}
}
