package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func statusDecodeFixture(kind model.Kind) queryMetadata {
	return queryMetadata{
		StatusRecord: StatusRecord{
			Version: queryMetadataVersion(kind), Binding: "binding", Tip: "tip", ID: "id", Kind: kind,
			CreatedAt: 13, UpdatedAt: 17, Deleted: true, Labels: []string{"label"},
			Anchors:      []model.Anchor{{Kind: model.AnchorPath, Value: "path"}},
			SupersededBy: []model.EntityID{"successor"}, Branch: "branch", SkippedOps: 2,
			Witness: []model.AnchorWitness{{}}, VerifiedAt: 19, StaleAt: 23,
			TaskStatus: model.StatusInProgress, Assignee: "worker", BlockedBy: []model.EntityID{"blocker"},
			HasRunningRun: true, RunbookStatus: model.RunbookActive, LedgerStatus: model.LedgerActive,
			LogEntryCount: 3, InvestigationStatus: model.InvestigationFixed, OpenFindingCount: 5,
			PlanStatus: model.PlanApproved,
		},
		UpdatedTime:         time.Unix(17, 29).UTC(),
		AnswerQuestionTerms: []string{"question"}, AnswerBodyTerms: []string{"body"},
		TextDigest: "digest", DuplicateContent: "fingerprint", Priority: 2,
		Parent: "parent", RunTasks: []model.EntityID{"run-task"},
	}
}

func TestStatusMetadataDecodeMatchesFullProjection(t *testing.T) {
	for _, kind := range []model.Kind{
		model.KindNote, model.KindDoc, model.KindAnswer, model.KindLog, model.KindTask,
		model.KindSprint, model.KindProject, model.KindRunbook, model.KindLedger,
		model.KindInvestigation, model.KindPlan,
	} {
		t.Run(string(kind), func(t *testing.T) {
			for _, populated := range []bool{false, true} {
				row := statusDecodeFixture(kind)
				if !populated {
					row = projectQueryMetadata("binding", "tip", model.Note{ID: "id"})
					row.Kind, row.Version = kind, queryMetadataVersion(kind)
				}
				data, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				source := queryMetadataSource{kind: kind, binding: row.Binding, tips: map[model.EntityID]model.SHA{row.ID: row.Tip}}
				full, err := decodeQueryMetadata(nil, source, data)
				if err != nil {
					t.Fatal(err)
				}
				got, err := decodeStatusMetadata(nil, source, data)
				if err != nil || !reflect.DeepEqual(got, full.StatusRecord) || !reflect.DeepEqual(got, row.StatusRecord) {
					t.Fatalf("populated=%t: status=%#v error=%v full=%#v", populated, got, err, full.StatusRecord)
				}
			}
		})
	}
}

func TestStatusMetadataDecodeRejectsInvalidRows(t *testing.T) {
	row := statusDecodeFixture(model.KindAnswer)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	source := queryMetadataSource{kind: row.Kind, binding: row.Binding, tips: map[model.EntityID]model.SHA{row.ID: row.Tip}}
	cases := map[string][]byte{
		"missing": nil, "empty": {}, "null": []byte("null"), "missing identity": []byte("{}"),
		"corrupt": []byte(`{"id":`), "trailing": append(append([]byte{}, data...), 'x'),
	}
	for field, replacement := range map[string]string{
		"version": "8", "binding": `"other"`, "kind": `"doc"`, "id": `"other"`,
		"tip": `"other"`, "created_at": `"invalid"`, "labels": `[1]`,
	} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = json.RawMessage(replacement)
		cases[field], err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeQueryMetadata(nil, source, data); err == nil {
				t.Fatal("full decoder accepted invalid row")
			}
			if got, err := decodeStatusMetadata(nil, source, data); err == nil || !reflect.DeepEqual(got, StatusRecord{}) {
				t.Fatalf("invalid row = %#v, %v", got, err)
			}
		})
	}
}

func TestStatusMetadataRejectsDifferentCapturedIdentity(t *testing.T) {
	s := initStore(t)
	first := create(t, s, noteOps("first")).EntityID()
	second := create(t, s, noteOps("second")).EntityID()
	if _, hit, err := s.StatusMetadata(t.Context(), model.KindNote); err != nil || !hit {
		t.Fatalf("warm hit=%t error=%v", hit, err)
	}
	source, err := captureQueryMetadata(t.Context(), s, model.KindNote)
	if err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(source.indexPath(), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		rows := queryMetadataBinding(tx, source.binding).Bucket(queryMetadataRowsBucket)
		return rows.Put([]byte(first), rows.Get([]byte(second)))
	})
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("replace error=%v close error=%v", err, closeErr)
	}
	got, hit, err := s.StatusMetadata(t.Context(), model.KindNote)
	if err == nil || hit || got.Captured != nil || len(got.Records) != 0 {
		t.Fatalf("mismatched identity view=%#v hit=%t error=%v", got, hit, err)
	}
}

func TestStatusMetadataDecodeAnswerAllocations(t *testing.T) {
	row := statusDecodeFixture(model.KindAnswer)
	source := queryMetadataSource{kind: row.Kind, binding: row.Binding, tips: map[model.EntityID]model.SHA{row.ID: row.Tip}}
	measure := func(row queryMetadata) (float64, float64) {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		status := testing.AllocsPerRun(3, func() {
			if got, err := decodeStatusMetadata(nil, source, data); err != nil || got.ID != row.ID {
				t.Fatalf("status decode=%#v error=%v", got, err)
			}
		})
		full := testing.AllocsPerRun(3, func() {
			if got, err := decodeQueryMetadata(nil, source, data); err != nil || got.ID != row.ID {
				t.Fatalf("full decode=%#v error=%v", got, err)
			}
		})
		return status, full
	}
	smallStatus, smallFull := measure(row)
	row.AnswerQuestionTerms, row.AnswerBodyTerms = nil, nil
	for i := range 4096 {
		row.AnswerQuestionTerms = append(row.AnswerQuestionTerms, fmt.Sprintf("question%04d", i))
		row.AnswerBodyTerms = append(row.AnswerBodyTerms, fmt.Sprintf("body%04d", i))
	}
	largeStatus, largeFull := measure(row)
	t.Logf("allocations: status small=%.0f large=%.0f full small=%.0f large=%.0f", smallStatus, largeStatus, smallFull, largeFull)
	if largeStatus > smallStatus || largeFull-smallFull < 8190 || largeStatus*10 >= largeFull {
		t.Fatalf("lexical allocation regression: status %.0f -> %.0f full %.0f -> %.0f", smallStatus, largeStatus, smallFull, largeFull)
	}
}
