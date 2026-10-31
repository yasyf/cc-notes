package store

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func runPriorDuplicateWriter(t *testing.T, executable, repo, title string) duplicatePriorWriterResult {
	t.Helper()
	request := duplicatePriorWriterRequest{Repo: repo, Title: title, Output: filepath.Join(t.TempDir(), "prior-writer.json")}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestDuplicatePriorWriterProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), duplicatePriorWriterRequestEnv+"="+string(data))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prior32 writer: %v\n%s", err, output)
	}
	data, err = os.ReadFile(request.Output)
	if err != nil {
		t.Fatal(err)
	}
	var result duplicatePriorWriterResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIndexedDuplicatePriorSchemaAndWriter(t *testing.T) {
	executable := os.Getenv("CCN_PRIOR_QUERY_WRITER")
	if executable == "" {
		t.Skip("requires the actual released32 writer test executable")
	}
	s := initStore(t)
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	var first model.EntityID
	for i, title := range []string{"prior32 primed", "prior32 writes after6"} {
		old := runPriorDuplicateWriter(t, executable, s.Git.Dir, title)
		if i == 0 {
			first = old.ID
		}
		if old.Metadata.Binding == q.binding || old.Metadata.Path == q.indexPath() || old.Metadata.Count != uint64(i+1) {
			t.Fatalf("prior32 did not prime a separate complete metadata binding: %+v", old)
		}
		before, err := os.ReadFile(old.Metadata.Path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			db, err := bolt.Open(old.Metadata.Path, 0o600, &bolt.Options{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := db.View(func(tx *bolt.Tx) error {
				var row map[string]json.RawMessage
				if err := json.Unmarshal(readQueryMetadata(tx, old.Metadata.Binding, old.ID), &row); err != nil {
					return err
				}
				if string(row["version"]) != "5" || row["duplicate_content"] != nil || queryMetadataBinding(tx, old.Metadata.Binding).Bucket(queryMetadataDuplicatesBucket) != nil {
					t.Fatal("prior32 fixture contains fingerprint semantics")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}()
		if got := mustDedupe(t, s, docOps(title)); got.EntityID() != old.ID {
			t.Fatalf("prior32 duplicate survivor changed: got %s want %s", got.EntityID(), old.ID)
		}
		after, err := os.ReadFile(old.Metadata.Path)
		if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatalf("new schema mutated prior32 cache: %v", err)
		}
		if entries, err := s.children(t.Context(), refs.Root(model.KindDoc)); err != nil || len(entries) != i+1 {
			t.Fatalf("prior32 duplicate created a new root: %d, %v", len(entries), err)
		}
		t.Logf("PRIOR32_DUPLICATE_AUTHORITY: stage=%d old_schema=5 new_schema=%d survivor=%s roots=%d old_cache_unchanged=true", i, queryMetadataVersion, old.ID, i+1)
	}
	func() {
		db, err := bolt.Open(q.indexPath(), 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := db.Update(func(tx *bolt.Tx) error {
			return queryMetadataBinding(tx, q.binding).DeleteBucket(queryMetadataDuplicatesBucket)
		}); err != nil {
			t.Fatal(err)
		}
	}()
	if got := mustDedupe(t, s, docOps("prior32 primed")); got.EntityID() != first {
		t.Fatal("missing fingerprint bucket was treated as authoritative absence")
	}
}
