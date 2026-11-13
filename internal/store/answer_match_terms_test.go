package store

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestAnswerMatchTermsProjection(t *testing.T) {
	tags := []string{"Orion", "match:hidden phrase", "owner:masked"}
	row := projectQueryMetadata("binding", "tip", model.Answer{
		ID: "answer", Title: "Nebula alerts", Body: "Quasar nebula quasar", Tags: tags,
	})
	if !slices.Equal(row.AnswerQuestionTerms, []string{"alert", "nebula", "orion"}) {
		t.Fatalf("question terms = %v", row.AnswerQuestionTerms)
	}
	if !slices.Equal(row.AnswerBodyTerms, []string{"nebula", "quasar"}) {
		t.Fatalf("body terms = %v", row.AnswerBodyTerms)
	}
	if !slices.Equal(tags, []string{"Orion", "match:hidden phrase", "owner:masked"}) {
		t.Fatalf("projection changed labels: %v", tags)
	}
}

func TestAnswerMatchTermsReplaceDeleteAndRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	original := projectQueryMetadata("binding", "old-tip", model.Answer{ID: "answer", Title: "Nebula", Body: "Quasar"})
	other := projectQueryMetadata("binding", "other-tip", model.Answer{ID: "other", Title: "Nebula", Body: "Quasar"})
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(t.Context(), tx, original); err != nil {
			return err
		}
		return replaceQueryMetadata(t.Context(), tx, other)
	}); err != nil {
		t.Fatal(err)
	}
	rows := func(index []byte, term string) []queryMetadata {
		t.Helper()
		var found []queryMetadata
		if err := db.View(func(tx *bolt.Tx) error {
			return walkQueryMetadataPostings(tx, "binding", index, queryMetadataLabelPrefix(model.KindAnswer, term), false, func(row queryMetadata) bool {
				if slices.Equal(index, queryMetadataQuestionTermsBucket) {
					return slices.Contains(row.AnswerQuestionTerms, term)
				}
				return slices.Contains(row.AnswerBodyTerms, term)
			}, func(row queryMetadata) error {
				found = append(found, row)
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		return found
	}
	check := func(index []byte, term string, want []queryMetadata) {
		t.Helper()
		if got := rows(index, term); !reflect.DeepEqual(got, want) {
			t.Fatalf("index %q term %q: got %#v want %#v", index, term, got, want)
		}
	}
	check(queryMetadataQuestionTermsBucket, "nebula", []queryMetadata{original, other})
	check(queryMetadataBodyTermsBucket, "quasar", []queryMetadata{original, other})
	replacement := projectQueryMetadata("binding", "new-tip", model.Answer{ID: "answer", Title: "Pulsar", Body: "Galaxy"})
	rollback := errors.New("rollback")
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(t.Context(), tx, replacement); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("replace rollback = %v", err)
	}
	check(queryMetadataQuestionTermsBucket, "nebula", []queryMetadata{original, other})
	check(queryMetadataQuestionTermsBucket, "pulsar", nil)
	check(queryMetadataBodyTermsBucket, "quasar", []queryMetadata{original, other})
	check(queryMetadataBodyTermsBucket, "galaxy", nil)
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(t.Context(), tx, replacement) }); err != nil {
		t.Fatal(err)
	}
	check(queryMetadataQuestionTermsBucket, "nebula", []queryMetadata{other})
	check(queryMetadataQuestionTermsBucket, "pulsar", []queryMetadata{replacement})
	check(queryMetadataBodyTermsBucket, "quasar", []queryMetadata{other})
	check(queryMetadataBodyTermsBucket, "galaxy", []queryMetadata{replacement})
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := deleteQueryMetadata(t.Context(), tx, "binding", "answer"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("delete rollback = %v", err)
	}
	check(queryMetadataQuestionTermsBucket, "pulsar", []queryMetadata{replacement})
	check(queryMetadataBodyTermsBucket, "galaxy", []queryMetadata{replacement})
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(t.Context(), tx, "binding", "answer") }); err != nil {
		t.Fatal(err)
	}
	check(queryMetadataQuestionTermsBucket, "pulsar", nil)
	check(queryMetadataBodyTermsBucket, "galaxy", nil)
	check(queryMetadataQuestionTermsBucket, "nebula", []queryMetadata{other})
	check(queryMetadataBodyTermsBucket, "quasar", []queryMetadata{other})
}
