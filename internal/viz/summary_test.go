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

// TestSummaryOfPlanExtras pins the legend extras a plan contributes: its
// lifecycle status, the executing and closing stamps, and the supersede flag
// its edge sets.
func TestSummaryOfPlanExtras(t *testing.T) {
	got := summaryOf(model.Plan{
		ID:           "0123456789abcdef0123456789abcdef01234567",
		Title:        "buffer the results channel",
		Status:       model.PlanDone,
		StartedAt:    1765509000,
		ClosedAt:     1765767296,
		SupersededBy: []model.EntityID{"89abcdef0123456789abcdef0123456789abcdef"},
	})
	want := EntitySummary{
		Kind:       entityPlan,
		ID:         "0123456789abcdef0123456789abcdef01234567",
		Short:      "0123456",
		Title:      "buffer the results channel",
		Status:     string(model.PlanDone),
		StartedAt:  1765509000,
		ClosedAt:   1765767296,
		Superseded: true,
	}
	if got != want {
		t.Errorf("summaryOf(plan) = %+v, want %+v", got, want)
	}
}
