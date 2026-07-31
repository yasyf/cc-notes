// White-box tests for the ranking accessors: id, updatedAt, and the order they
// feed. Living in the package lets them build RelevantEntry values directly, so
// a kind whose accessor arm is missing is caught by an exact value rather than
// by whichever way an unstable sort happens to land.
package notes

import (
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

var relevantAccessorCases = []struct {
	name      string
	entry     RelevantEntry
	wantID    model.EntityID
	wantStamp int64
}{
	{"note", RelevantEntry{Kind: model.KindNote, Note: model.Note{ID: "noteid", UpdatedAt: 11}}, "noteid", 11},
	{"doc", RelevantEntry{Kind: model.KindDoc, Doc: model.Doc{ID: "docid", UpdatedAt: 22}}, "docid", 22},
	{"log", RelevantEntry{Kind: model.KindLog, Log: model.Log{ID: "logid", UpdatedAt: 33}}, "logid", 33},
	{"runbook", RelevantEntry{Kind: model.KindRunbook, Runbook: model.Runbook{ID: "rbid", UpdatedAt: 44}}, "rbid", 44},
	{
		"investigation",
		RelevantEntry{Kind: model.KindInvestigation, Investigation: model.Investigation{ID: "invid", UpdatedAt: 55}},
		"invid", 55,
	},
	{"plan", RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "planid", UpdatedAt: 66}}, "planid", 66},
}

// TestRelevantEntryAccessorsPerKind pins that every carried kind reports its own
// id and UpdatedAt. Both accessors fall through to the Note arm by default, so a
// kind without an explicit case ranks on the zero Note's empty id and zero
// timestamp — sorting to the bottom of every tie and colliding with every other
// unhandled kind — with nothing else to signal it.
func TestRelevantEntryAccessorsPerKind(t *testing.T) {
	for _, tc := range relevantAccessorCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.id(); got != tc.wantID {
				t.Errorf("id() = %q, want %q", got, tc.wantID)
			}
			if got := tc.entry.updatedAt(); got != tc.wantStamp {
				t.Errorf("updatedAt() = %d, want %d", got, tc.wantStamp)
			}
		})
	}
}

// TestRelevantAccessorCasesCoverCarriedKinds keeps the table above exhaustive:
// every snapshot RelevantEntry can carry needs a row, so a tenth kind joining
// the struct cannot land its accessor arms untested.
func TestRelevantAccessorCasesCoverCarriedKinds(t *testing.T) {
	snapshot := reflect.TypeOf((*model.Snapshot)(nil)).Elem()
	var carried []string
	for _, f := range reflect.VisibleFields(reflect.TypeOf(RelevantEntry{})) {
		if f.Type.Implements(snapshot) {
			carried = append(carried, f.Name)
		}
	}
	if len(carried) != len(relevantAccessorCases) {
		t.Fatalf("RelevantEntry carries %d snapshots %v, but the accessor table has %d rows",
			len(carried), carried, len(relevantAccessorCases))
	}
}

// TestCompareScoredRanksPlansOnOwnFields drives the ranking order through two
// equally-scored plans. The tiebreak reads updatedAt then id, both of which the
// accessors must take from the plan itself: on the note fallback the two
// entries compare equal and their order becomes whatever the caller's slice
// order happened to be.
func TestCompareScoredRanksPlansOnOwnFields(t *testing.T) {
	older := RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "aaa", UpdatedAt: 100}, Score: 150}
	newer := RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "zzz", UpdatedAt: 200}, Score: 150}
	if got := compareScored(newer, older); got >= 0 {
		t.Errorf("compareScored(newer, older) = %d, want negative: the newer plan ranks first", got)
	}
	if got := compareScored(older, newer); got <= 0 {
		t.Errorf("compareScored(older, newer) = %d, want positive", got)
	}

	lowID := RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "aaa", UpdatedAt: 100}, Score: 150}
	highID := RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "bbb", UpdatedAt: 100}, Score: 150}
	if got := compareScored(lowID, highID); got >= 0 {
		t.Errorf("compareScored(aaa, bbb) = %d, want negative: equal stamps break on the lower id", got)
	}

	// Score still dominates both tiebreaks.
	boosted := RelevantEntry{Kind: model.KindPlan, Plan: model.Plan{ID: "zzz", UpdatedAt: 1}, Score: 200}
	if got := compareScored(boosted, newer); got >= 0 {
		t.Errorf("compareScored(boosted, newer) = %d, want negative: score outranks the stamp", got)
	}
}
