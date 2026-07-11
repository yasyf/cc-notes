package viz

import (
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// TestSummaryOfCoversKinds exercises summaryOf for every entity kind, so a kind
// missing from its per-kind extras switch fails here (via the default panic)
// rather than in production, and pins the common Kind field to the wire value.
// Each snapshot decodes a valid id so summaryOf's id.Short() has real bytes.
func TestSummaryOfCoversKinds(t *testing.T) {
	const idJSON = `{"id":"0123456789abcdef0123456789abcdef01234567"}`
	for _, k := range model.Kinds() {
		snap, err := k.DecodeSnapshot([]byte(idJSON))
		if err != nil {
			t.Fatalf("decode %s snapshot: %v", k, err)
		}
		if got := summaryOf(snap).Kind; got != string(k) {
			t.Errorf("summaryOf(%s).Kind = %q, want %q", k, got, k)
		}
	}
}
