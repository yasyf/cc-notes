package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestTaskRelationIndexIsolatesLegacyMetadataWriters(t *testing.T) {
	s := initStore(t)
	id := create(t, s, taskOps("task", "main")).EntityID()
	identity, err := executableIdentity()
	if err != nil {
		t.Fatal(err)
	}
	legacy := s.listCacheName(model.KindTask, fmt.Appendf(nil, "metadata %d", 2), foldCacheGeneration, identity)
	source, err := queryMetadataIdentity(t.Context(), s, model.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(s.recordsCommonDir, "cc-notes-query", legacy+".db")
	if source.indexPath() == legacyPath {
		t.Fatal("new relation projection shares the legacy metadata namespace")
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, marker := range [][]byte{[]byte("legacy index"), []byte("legacy writer rebuilt its index")} {
		if err := os.WriteFile(legacyPath, marker, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, hit, err := s.IndexedTasksBlocking(t.Context(), id); err != nil || !hit || got != nil {
			t.Fatalf("new index with legacy writer = %v, %v, %v", got, hit, err)
		}
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, marker) {
			t.Fatalf("new reader rewrote the legacy index: %q", data)
		}
	}
}
