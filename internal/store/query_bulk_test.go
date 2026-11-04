package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

func TestBulkQueryMetadataMatchesIncrementalGraph(t *testing.T) {
	rows := make([]queryMetadata, 0, 48)
	for i := range 48 {
		id := model.EntityID(fmt.Sprintf("doc-%02d", i))
		row := projectQueryMetadata(fmt.Sprintf("binding-%d", i%2), model.SHA(fmt.Sprintf("tip-%02d", i)), model.Doc{ID: id, Tags: []string{"shared", fmt.Sprintf("tag-%d", i%3)}})
		row.Labels = append(row.Labels, "shared")
		rows = append(rows, row)
	}
	bulk, incremental := newQueryMetadataDB(t), newQueryMetadataDB(t)
	if err := bulk.Update(func(tx *bolt.Tx) error { return buildQueryMetadata(t.Context(), tx, rows) }); err != nil {
		t.Fatal(err)
	}
	if err := incremental.Update(func(tx *bolt.Tx) error {
		for _, row := range rows {
			if err := replaceQueryMetadata(t.Context(), tx, row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func(db *bolt.DB) map[string][]byte {
		t.Helper()
		values := make(map[string][]byte)
		if err := db.View(func(tx *bolt.Tx) error {
			root := tx.Bucket(queryGenerationsBucket)
			for _, binding := range []string{"binding-0", "binding-1"} {
				values[binding] = bytes.Clone(queryGenerationBinding(tx, binding).Get(queryCurrentRoot))
			}
			for _, name := range [][]byte{queryObjectsBucket, []byte(managed.QueryObjectRefs)} {
				if err := root.Bucket(name).ForEach(func(key, value []byte) error {
					values[string(name)+"/"+string(key)] = bytes.Clone(value)
					return nil
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return values
	}
	if got, want := snapshot(bulk), snapshot(incremental); !reflect.DeepEqual(got, want) {
		t.Fatalf("bulk graph differs from incremental graph: bulk=%d entries incremental=%d", len(got), len(want))
	}
}

func TestQueryReferenceCountsCancelWithoutDirtyingPages(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("counts"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("shared"), binary.BigEndian.AppendUint64(nil, 3))
	}); err != nil {
		t.Fatal(err)
	}
	writes := func() int64 {
		stats := db.Stats()
		return stats.TxStats.GetWrite()
	}
	before := writes()
	if err := db.Update(func(*bolt.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	emptyWrites := writes() - before
	before = writes()
	if err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		value, err := counts.get("shared")
		if err != nil {
			return err
		}
		counts.set("shared", value+1)
		counts.set("shared", value)
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	if changed := writes() - before; changed != emptyWrites {
		t.Fatalf("neutral reference changes wrote %d pages; empty transaction wrote %d", changed, emptyWrites)
	}
}

func TestQueryReferenceCountsFlushAndCancellation(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("counts"))
		if err != nil {
			return err
		}
		counts := newQueryReferenceCounts(bucket)
		if _, err := counts.get("shared"); err != nil {
			return err
		}
		counts.set("shared", 3)
		if err := counts.flush(t.Context()); err != nil {
			return err
		}
		counts.set("shared", 0)
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		value, err := counts.get("shared")
		if err != nil {
			return err
		}
		if value != 0 {
			t.Fatalf("released count = %d", value)
		}
		counts.set("shared", 2)
		cancel()
		return counts.flush(ctx)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("flush = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if value := tx.Bucket([]byte("counts")).Get([]byte("shared")); value != nil {
			t.Fatalf("canceled count committed: %x", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
