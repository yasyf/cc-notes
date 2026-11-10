package store

import (
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestDuplicateCheckActivatesThroughThePublicationThatFollows(t *testing.T) {
	s := initStore(t)
	create(t, s, docOps("first"))
	source, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	generations := func() (current, active queryGeneration) {
		t.Helper()
		if err := s.managedView(t.Context(), func(tx *bolt.Tx) error {
			binding := queryGenerationBinding(tx, source.binding)
			if current, err = readQueryGeneration(binding, queryCurrentRoot); err != nil {
				return err
			}
			if binding.Get(queryActiveRoot) != nil {
				active, err = readQueryGeneration(binding, queryActiveRoot)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return current, active
	}
	afterFirst, _ := generations()
	create(t, s, docOps("second"))
	afterSecond, active := generations()
	if active.Number != afterFirst.Number || afterSecond.Number == afterFirst.Number {
		t.Fatalf("active %d after the second write, want the generation its duplicate check read, %d (now %d)", active.Number, afterFirst.Number, afterSecond.Number)
	}
	if pending := len(s.activations.bindings); pending != 0 {
		t.Fatalf("%d activations still pending after the publication", pending)
	}
	if _, err := s.ListDocs(t.Context(), false, false); err != nil {
		t.Fatal(err)
	}
	if _, active := generations(); active.Number != afterSecond.Number {
		t.Fatalf("a plain read left active at %d, want %d", active.Number, afterSecond.Number)
	}
}
