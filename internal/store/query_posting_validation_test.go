package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryTaskRelationValidatesMatchingMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*queryMetadata)
		summary []byte
		want    string
	}{
		{"version", func(row *queryMetadata) { row.Version++ }, nil, "query metadata differs from captured task child"},
		{"binding", func(row *queryMetadata) { row.Binding = "other" }, nil, "query metadata differs from captured task child"},
		{"empty tip", func(row *queryMetadata) { row.Tip = "" }, nil, "query metadata differs from captured task child"},
		{"different tip", func(row *queryMetadata) { row.Tip = "other" }, nil, "query metadata differs from captured task child"},
		{"different id", func(row *queryMetadata) { row.ID = "other" }, nil, "query metadata differs from captured task other"},
		{"invalid summary", func(*queryMetadata) {}, []byte{2}, "invalid metadata tip summary for child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				row := projectQueryMetadata("binding", "tip", model.Task{ID: "child", Parent: "parent", BlockedBy: []model.EntityID{"parent"}})
				if err := replaceQueryMetadata(tx, row); err != nil {
					return err
				}
				tc.mutate(&row)
				data, err := json.Marshal(row)
				if err != nil {
					return err
				}
				if err := queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte("child"), data); err != nil {
					return err
				}
				if tc.summary != nil {
					return queryMetadataKindTips(tx, "binding", model.KindTask).Put([]byte("child"), tc.summary)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.View(func(tx *bolt.Tx) error {
				source := queryMetadataSource{binding: "binding", kind: model.KindTask}
				for _, walk := range []func(*bolt.Tx, string, model.EntityID, func(queryMetadata) error) error{walkQueryMetadataBlockedBy, walkQueryMetadataTaskChildren} {
					got, err := queryTaskRelationIDs(t.Context(), tx, source, "parent", walk)
					if got != nil || err == nil || err.Error() != tc.want {
						t.Fatalf("invalid matching metadata = %v, %v; want nil, %q", got, err, tc.want)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryTaskRelationPreservesDecodeAndCancellationOrder(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid JSON", true: "malformed JSON"}[malformed], func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				row := projectQueryMetadata("binding", "tip", model.Task{ID: "child", Parent: "parent"})
				if err := replaceQueryMetadata(tx, row); err != nil {
					return err
				}
				if malformed {
					return queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte("child"), []byte("invalid JSON"))
				}
				return queryMetadataKindTips(tx, "binding", model.KindTask).Put([]byte("child"), []byte{2})
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := db.View(func(tx *bolt.Tx) error {
				got, err := queryTaskRelationIDs(ctx, tx, queryMetadataSource{binding: "binding", kind: model.KindTask}, "parent", walkQueryMetadataTaskChildren)
				var syntax *json.SyntaxError
				if got != nil || (malformed && !errors.As(err, &syntax)) || (!malformed && !errors.Is(err, context.Canceled)) {
					t.Fatalf("malformed %v: canceled matching metadata = %v, %v", malformed, got, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
