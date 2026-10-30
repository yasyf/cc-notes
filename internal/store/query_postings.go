package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

var (
	queryMetadataRootBucket        = []byte("query-metadata-v1")
	queryMetadataRowsBucket        = []byte("rows")
	queryMetadataTipsBucket        = []byte("tips")
	queryMetadataLabelsBucket      = []byte("labels")
	queryMetadataAnchorsBucket     = []byte("anchors")
	queryMetadataSupersedersBucket = []byte("superseders")
	queryMetadataBlockedByBucket   = []byte("blocked-by")
	queryMetadataParentsBucket     = []byte("parents")
	queryMetadataRunTasksBucket    = []byte("run-tasks")
)

func queryMetadataBinding(tx *bolt.Tx, binding string) *bolt.Bucket {
	root := tx.Bucket(queryMetadataRootBucket)
	if root == nil {
		return nil
	}
	return root.Bucket(queryMetadataAtom(binding))
}

func ensureQueryMetadataBinding(tx *bolt.Tx, name string) (*bolt.Bucket, error) {
	root, err := tx.CreateBucketIfNotExists(queryMetadataRootBucket)
	if err != nil {
		return nil, err
	}
	binding, err := root.CreateBucketIfNotExists(queryMetadataAtom(name))
	if err != nil {
		return nil, err
	}
	for _, name := range [][]byte{queryMetadataRowsBucket, queryMetadataLabelsBucket, queryMetadataAnchorsBucket, queryMetadataSupersedersBucket, queryMetadataTipsBucket, queryMetadataBlockedByBucket, queryMetadataParentsBucket, queryMetadataRunTasksBucket} {
		if _, err := binding.CreateBucketIfNotExists(name); err != nil {
			return nil, err
		}
	}
	return binding, nil
}

func replaceQueryMetadata(tx *bolt.Tx, row queryMetadata) error {
	binding, err := ensureQueryMetadataBinding(tx, row.Binding)
	if err != nil {
		return err
	}
	rows := binding.Bucket(queryMetadataRowsBucket)
	labels := binding.Bucket(queryMetadataLabelsBucket)
	anchors := binding.Bucket(queryMetadataAnchorsBucket)
	superseders := binding.Bucket(queryMetadataSupersedersBucket)
	if err := removeQueryMetadataPostings(binding, row.ID); err != nil {
		return err
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if err := rows.Put([]byte(row.ID), data); err != nil {
		return err
	}
	for _, label := range row.Labels {
		if err := labels.Put(queryMetadataPostingKey(queryMetadataLabelPrefix(row.Kind, label), row), nil); err != nil {
			return err
		}
	}
	for _, anchor := range row.Anchors {
		if err := anchors.Put(queryMetadataPostingKey(queryMetadataAnchorPrefix(row.Kind, anchor), row), nil); err != nil {
			return err
		}
	}
	for _, target := range row.SupersededBy {
		if err := superseders.Put(queryMetadataPostingKey(queryMetadataSupersedersPrefix(row.Kind, target), row), nil); err != nil {
			return err
		}
	}
	if row.Kind == model.KindTask {
		for _, target := range row.BlockedBy {
			if err := binding.Bucket(queryMetadataBlockedByBucket).Put(queryMetadataPostingKey(queryMetadataSupersedersPrefix(row.Kind, target), row), nil); err != nil {
				return err
			}
		}
		if err := binding.Bucket(queryMetadataParentsBucket).Put(queryMetadataPostingKey(queryMetadataSupersedersPrefix(row.Kind, row.Parent), row), nil); err != nil {
			return err
		}
	}
	for _, target := range row.RunTasks {
		if err := binding.Bucket(queryMetadataRunTasksBucket).Put(queryMetadataPostingKey(queryMetadataSupersedersPrefix(row.Kind, target), row), nil); err != nil {
			return err
		}
	}
	return putQueryMetadataTip(binding, row)
}

func deleteQueryMetadata(tx *bolt.Tx, binding string, id model.EntityID) error {
	bucket := queryMetadataBinding(tx, binding)
	if bucket == nil {
		return nil
	}
	if err := removeQueryMetadataPostings(bucket, id); err != nil {
		return err
	}
	return bucket.Bucket(queryMetadataRowsBucket).Delete([]byte(id))
}

func removeQueryMetadataPostings(binding *bolt.Bucket, id model.EntityID) error {
	data := binding.Bucket(queryMetadataRowsBucket).Get([]byte(id))
	if data == nil {
		return nil
	}
	var previous queryMetadata
	if err := json.Unmarshal(data, &previous); err != nil {
		return err
	}
	for _, label := range previous.Labels {
		if err := binding.Bucket(queryMetadataLabelsBucket).Delete(queryMetadataPostingKey(queryMetadataLabelPrefix(previous.Kind, label), previous)); err != nil {
			return err
		}
	}
	for _, anchor := range previous.Anchors {
		if err := binding.Bucket(queryMetadataAnchorsBucket).Delete(queryMetadataPostingKey(queryMetadataAnchorPrefix(previous.Kind, anchor), previous)); err != nil {
			return err
		}
	}
	for _, target := range previous.SupersededBy {
		if err := binding.Bucket(queryMetadataSupersedersBucket).Delete(queryMetadataPostingKey(queryMetadataSupersedersPrefix(previous.Kind, target), previous)); err != nil {
			return err
		}
	}
	if previous.Kind == model.KindTask {
		for _, target := range previous.BlockedBy {
			if err := binding.Bucket(queryMetadataBlockedByBucket).Delete(queryMetadataPostingKey(queryMetadataSupersedersPrefix(previous.Kind, target), previous)); err != nil {
				return err
			}
		}
		if err := binding.Bucket(queryMetadataParentsBucket).Delete(queryMetadataPostingKey(queryMetadataSupersedersPrefix(previous.Kind, previous.Parent), previous)); err != nil {
			return err
		}
	}
	for _, target := range previous.RunTasks {
		if err := binding.Bucket(queryMetadataRunTasksBucket).Delete(queryMetadataPostingKey(queryMetadataSupersedersPrefix(previous.Kind, target), previous)); err != nil {
			return err
		}
	}
	return deleteQueryMetadataTip(binding, previous)
}

func readQueryMetadata(tx *bolt.Tx, binding string, id model.EntityID) []byte {
	bucket := queryMetadataBinding(tx, binding)
	if bucket == nil {
		return nil
	}
	return bytes.Clone(bucket.Bucket(queryMetadataRowsBucket).Get([]byte(id)))
}

func walkQueryMetadataLabel(tx *bolt.Tx, binding string, kind model.Kind, label string, includeDeleted bool, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataLabelsBucket, queryMetadataLabelPrefix(kind, label), includeDeleted, func(row queryMetadata) bool {
		return row.Kind == kind && slices.Contains(row.Labels, label)
	}, visit)
}

func walkQueryMetadataAnchor(tx *bolt.Tx, binding string, kind model.Kind, anchor model.Anchor, includeDeleted bool, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataAnchorsBucket, queryMetadataAnchorPrefix(kind, anchor), includeDeleted, func(row queryMetadata) bool {
		return row.Kind == kind && slices.Contains(row.Anchors, anchor)
	}, visit)
}

func walkQueryMetadataSuperseders(tx *bolt.Tx, binding string, kind model.Kind, target model.EntityID, includeDeleted bool, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataSupersedersBucket, queryMetadataSupersedersPrefix(kind, target), includeDeleted, func(row queryMetadata) bool {
		return row.Kind == kind && slices.Contains(row.SupersededBy, target)
	}, visit)
}

func walkQueryMetadataBlockedBy(tx *bolt.Tx, binding string, target model.EntityID, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataBlockedByBucket, queryMetadataSupersedersPrefix(model.KindTask, target), false, func(row queryMetadata) bool {
		return row.Kind == model.KindTask && slices.Contains(row.BlockedBy, target)
	}, visit)
}

func walkQueryMetadataTaskChildren(tx *bolt.Tx, binding string, target model.EntityID, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataParentsBucket, queryMetadataSupersedersPrefix(model.KindTask, target), false, func(row queryMetadata) bool {
		return row.Kind == model.KindTask && row.Parent == target
	}, visit)
}

func walkQueryMetadataRunTasks(tx *bolt.Tx, binding string, target model.EntityID, visit func(queryMetadata) error) error {
	return walkQueryMetadataPostings(tx, binding, queryMetadataRunTasksBucket, queryMetadataSupersedersPrefix(model.KindRunbook, target), false, func(row queryMetadata) bool {
		return row.Kind == model.KindRunbook && slices.Contains(row.RunTasks, target)
	}, visit)
}

func walkQueryMetadataPostings(tx *bolt.Tx, binding string, index, prefix []byte, includeDeleted bool, matches func(queryMetadata) bool, visit func(queryMetadata) error) error {
	bucket := queryMetadataBinding(tx, binding)
	if bucket == nil {
		return nil
	}
	idOffset := len(prefix) + 1
	if !includeDeleted {
		prefix = append(prefix, 0)
	}
	rows, postings := bucket.Bucket(queryMetadataRowsBucket), bucket.Bucket(index)
	cursor := postings.Cursor()
	for key, _ := cursor.Seek(prefix); bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
		id := key[idOffset:]
		data := rows.Get(id)
		if data == nil {
			return fmt.Errorf("query posting has no metadata row: %s", id)
		}
		var row queryMetadata
		if err := json.Unmarshal(data, &row); err != nil {
			return err
		}
		if !matches(row) {
			continue
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

func queryMetadataAtom(value string) []byte {
	return append(binary.BigEndian.AppendUint64(nil, uint64(len(value))), value...)
}

func queryMetadataLabelPrefix(kind model.Kind, label string) []byte {
	return queryMetadataPostingDigest(append(queryMetadataAtom(string(kind)), queryMetadataAtom(label)...))
}

func queryMetadataAnchorPrefix(kind model.Kind, anchor model.Anchor) []byte {
	prefix := append(queryMetadataAtom(string(kind)), queryMetadataAtom(string(anchor.Kind))...)
	return queryMetadataPostingDigest(append(prefix, queryMetadataAtom(anchor.Value)...))
}

func queryMetadataSupersedersPrefix(kind model.Kind, target model.EntityID) []byte {
	return queryMetadataPostingDigest(append(queryMetadataAtom(string(kind)), queryMetadataAtom(string(target))...))
}

func queryMetadataPostingKey(prefix []byte, row queryMetadata) []byte {
	deleted := byte(0)
	if row.Deleted {
		deleted = 1
	}
	return append(append(prefix, deleted), string(row.ID)...)
}

func queryMetadataPostingDigest(term []byte) []byte {
	sum := sha256.Sum256(term)
	return sum[:]
}
