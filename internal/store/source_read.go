package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

var (
	sourceHeadKey          = []byte("source-head")
	sourceRevisionsBucket  = []byte("source-revisions")
	sourceOperationsBucket = []byte("source-operations")
)

type sourcePublication struct {
	Revision    *sourceindex.Revision
	Revisions   []sourceindex.Revision
	OperationID string
	Operation   *sourceindex.Operation
}

func sourceRevision(tx *bolt.Tx, token model.SHA) (sourceindex.Revision, error) {
	current := token == ""
	root, err := managedRoot(tx)
	if err != nil {
		return sourceindex.Revision{}, err
	}
	if token == "" {
		token = model.SHA(root.Get(sourceHeadKey))
	}
	bucket := root.Bucket(sourceRevisionsBucket)
	if bucket == nil || token == "" {
		return sourceindex.Revision{}, managed.ErrReindex
	}
	data := bucket.Get([]byte(token))
	if data == nil {
		return sourceindex.Revision{}, fmt.Errorf("source revision %s: %w", token, sourceindex.ErrProjectionUnavailable)
	}
	var revision sourceindex.Revision
	if err := json.Unmarshal(data, &revision); err != nil {
		return revision, errors.Join(managed.ErrReindex, err)
	}
	if revision.Token != token || current && revision.ProjectionAvailable && revision.RenderPolicy != sourceview.RenderPolicy {
		return revision, managed.ErrReindex
	}
	return revision, nil
}

func sourceOperation(tx *bolt.Tx, id string) (sourceindex.Operation, bool, error) {
	if _, err := sourceindex.OperationRef(id); err != nil {
		return sourceindex.Operation{}, false, err
	}
	root, err := managedRoot(tx)
	if err != nil {
		return sourceindex.Operation{}, false, err
	}
	bucket := root.Bucket(sourceOperationsBucket)
	if bucket == nil {
		return sourceindex.Operation{}, false, managed.ErrReindex
	}
	data := bucket.Get([]byte(id))
	if data == nil {
		return sourceindex.Operation{}, false, nil
	}
	var operation sourceindex.Operation
	err = json.Unmarshal(data, &operation)
	if err == nil {
		err = validateManagedSourceOperation(id, operation)
	}
	return operation, err == nil, err
}

func applySource(tx *bolt.Tx, publication *sourcePublication) error {
	if publication == nil {
		return nil
	}
	root, err := managedRoot(tx)
	if err != nil {
		return err
	}
	revisions, err := root.CreateBucketIfNotExists(sourceRevisionsBucket)
	if err != nil {
		return err
	}
	operations, err := root.CreateBucketIfNotExists(sourceOperationsBucket)
	if err != nil {
		return err
	}
	for _, revision := range publication.Revisions {
		data, err := json.Marshal(revision)
		if err != nil {
			return err
		}
		if err := revisions.Put([]byte(revision.Token), data); err != nil {
			return err
		}
	}
	if publication.Revision != nil {
		data, err := json.Marshal(publication.Revision)
		if err != nil {
			return err
		}
		if err := revisions.Put([]byte(publication.Revision.Token), data); err != nil {
			return err
		}
		if err := root.Put(sourceHeadKey, []byte(publication.Revision.Token)); err != nil {
			return err
		}
	}
	if publication.Operation != nil {
		data, err := json.Marshal(publication.Operation)
		if err != nil {
			return err
		}
		return operations.Put([]byte(publication.OperationID), data)
	}
	return nil
}

// SourceHead returns the atomically published managed source fence.
func (s *Store) SourceHead(ctx context.Context) (revision sourceindex.Revision, err error) {
	err = s.managedView(ctx, func(tx *bolt.Tx) error { revision, err = sourceRevision(tx, ""); return err })
	return
}

// ResolveRevision reads normalized historical metadata without opening raw entity payloads.
func (s *Store) ResolveRevision(ctx context.Context, token model.SHA) (revision sourceindex.Revision, err error) {
	err = s.managedView(ctx, func(tx *bolt.Tx) error { revision, err = sourceRevision(tx, token); return err })
	return
}

func (s *Store) sourceGraphView(ctx context.Context, token model.SHA, read func(*sourcegraph.Map, sourceindex.Revision) error) error {
	return s.managedView(ctx, func(tx *bolt.Tx) error {
		revision, err := sourceRevision(tx, token)
		if err != nil {
			return err
		}
		return s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error { return read(sourcegraph.New(objects), revision) })
	})
}

// SourceEntityAt resolves one indexed immutable identity without reading its history.
func (s *Store) SourceEntityAt(ctx context.Context, token model.SHA, ref string) (entity sourceview.Entity, found bool, err error) {
	parsed, err := refs.Parse(ref)
	if err != nil {
		return entity, false, err
	}
	err = s.sourceGraphView(ctx, token, func(index *sourcegraph.Map, revision sourceindex.Revision) error {
		data, ok, err := index.Get(revision.EntityRoot, ref)
		if err != nil || !ok {
			return err
		}
		if err := json.Unmarshal(data, &entity); err != nil {
			return err
		}
		if entity.Ref != ref || entity.Kind != parsed.Kind || entity.ID != parsed.ID {
			return managed.ErrReindex
		}
		found = true
		return nil
	})
	return
}

// ResolveEntityAt selects one captured entity tip without materializing its manifest.
func (s *Store) ResolveEntityAt(ctx context.Context, token model.SHA, ref string) (entry gitobj.SourceRefTip, found bool, err error) {
	entity, found, err := s.SourceEntityAt(ctx, token, ref)
	if err != nil || !found {
		return entry, false, err
	}
	return gitobj.SourceRefTip{Ref: ref, Tip: entity.Tip}, true, nil
}

// SourceSnapshot materializes an explicitly requested raw tip map, including released history.
func (s *Store) SourceSnapshot(ctx context.Context, token model.SHA) (manifest gitobj.SourceManifest, err error) {
	err = s.sourceGraphView(ctx, token, func(index *sourcegraph.Map, revision sourceindex.Revision) error {
		return index.Prefix(revision.EntityRoot, "", func(entry sourcegraph.Entry) error {
			var entity sourceview.Entity
			if err := json.Unmarshal(entry.Value, &entity); err != nil {
				return err
			}
			manifest = append(manifest, gitobj.SourceRefTip{Ref: entry.Key, Tip: entity.Tip})
			return nil
		})
	})
	return
}

func sourceAncestor(tx *bolt.Tx, from, to model.SHA) error {
	before, err := sourceRevision(tx, from)
	if err != nil {
		return err
	}
	after, err := sourceRevision(tx, to)
	if err != nil {
		return err
	}
	if before.Height > after.Height {
		return sourceindex.ErrNotAncestor
	}
	distance := after.Height - before.Height
	for bit := 0; distance != 0; bit++ {
		if distance&1 != 0 {
			if bit >= len(after.AncestorJumps) {
				return managed.ErrReindex
			}
			after, err = sourceRevision(tx, after.AncestorJumps[bit])
			if err != nil {
				return err
			}
		}
		distance >>= 1
	}
	if after.Token != from {
		return sourceindex.ErrNotAncestor
	}
	return nil
}

func (s *Store) sourceDiff(ctx context.Context, from, to model.SHA, objects bool, emit func(sourcegraph.Change) error) error {
	return s.managedView(ctx, func(tx *bolt.Tx) error {
		if err := sourceAncestor(tx, from, to); err != nil {
			return err
		}
		before, err := sourceRevision(tx, from)
		if err != nil {
			return err
		}
		after, err := sourceRevision(tx, to)
		if err != nil {
			return err
		}
		if objects {
			if err := sourceProjection(before); err != nil {
				return err
			}
			if err := sourceProjection(after); err != nil {
				return err
			}
		}
		oldRoot, newRoot := before.EntityRoot, after.EntityRoot
		if objects {
			oldRoot, newRoot = before.ObjectRoot, after.ObjectRoot
		}
		return s.Repo.SourceObjects(ctx, func(scope *gitobj.SourceObjects) error { return sourcegraph.New(scope).Diff(oldRoot, newRoot, emit) })
	})
}

// SourceChanges skips equal entity subtrees and returns only changed raw tips.
func (s *Store) SourceChanges(ctx context.Context, from, to model.SHA) (changes sourceindex.Changes, err error) {
	err = s.sourceDiff(ctx, from, to, false, func(change sourcegraph.Change) error {
		if !change.AfterExists {
			changes.Deletes = append(changes.Deletes, change.Key)
			return nil
		}
		var after sourceview.Entity
		if err := json.Unmarshal(change.After, &after); err != nil {
			return err
		}
		if change.BeforeExists {
			var before sourceview.Entity
			if err := json.Unmarshal(change.Before, &before); err != nil {
				return err
			}
			if before.Tip == after.Tip {
				return nil
			}
		}
		changes.Upserts = append(changes.Upserts, gitobj.SourceRefTip{Ref: change.Key, Tip: after.Tip})
		return nil
	})
	return
}

// ReadOperation reads one normalized receipt or consumed-ID tombstone.
func (s *Store) ReadOperation(ctx context.Context, id string) (operation sourceindex.Operation, found bool, err error) {
	err = s.managedView(ctx, func(tx *bolt.Tx) error { operation, found, err = sourceOperation(tx, id); return err })
	return
}

func sourceProjection(revision sourceindex.Revision) error {
	if !revision.ProjectionAvailable || revision.RenderPolicy != sourceview.RenderPolicy {
		return sourceindex.ErrProjectionUnavailable
	}
	if revision.ProjectionConflicts != 0 || revision.ProjectionErrors != 0 {
		return fmt.Errorf("source namespace has %d conflicts and %d invalid entities", revision.ProjectionConflicts, revision.ProjectionErrors)
	}
	return nil
}

// SourceObject resolves one immutable descriptor without rendering unrelated entities.
func (s *Store) SourceObject(ctx context.Context, token model.SHA, key string) (value sourceview.Object, found bool, err error) {
	err = s.sourceGraphView(ctx, token, func(index *sourcegraph.Map, revision sourceindex.Revision) error {
		if err := sourceProjection(revision); err != nil {
			return err
		}
		data, ok, err := index.Get(revision.ObjectRoot, key)
		if err != nil || !ok {
			return err
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.Key != key {
			return managed.ErrReindex
		}
		if err := sourceview.ValidateObject(value); err != nil {
			return err
		}
		found = true
		return nil
	})
	return
}

// SourceObjectRange pages in original logical-ID order with exact subtree counts.
func (s *Store) SourceObjectRange(ctx context.Context, token model.SHA, start uint64, limit int) (values []sourceview.Object, total uint64, err error) {
	err = s.sourceGraphView(ctx, token, func(index *sourcegraph.Map, revision sourceindex.Revision) error {
		if err := sourceProjection(revision); err != nil {
			return err
		}
		total, err = index.Count(revision.ObjectRoot)
		if err != nil {
			return err
		}
		entries, err := index.Range(revision.ObjectRoot, start, limit)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			var value sourceview.Object
			if err := json.Unmarshal(entry.Value, &value); err != nil {
				return err
			}
			if entry.Key != value.Key {
				return managed.ErrReindex
			}
			if err := sourceview.ValidateObject(value); err != nil {
				return err
			}
			values = append(values, value)
		}
		return nil
	})
	return
}

// SourceObjectRank verifies an object's actual ordered position for cursor validation.
func (s *Store) SourceObjectRank(ctx context.Context, token model.SHA, key string) (rank uint64, found bool, err error) {
	err = s.sourceGraphView(ctx, token, func(index *sourcegraph.Map, revision sourceindex.Revision) error {
		if err := sourceProjection(revision); err != nil {
			return err
		}
		rank, found, err = index.Rank(revision.ObjectRoot, key)
		return err
	})
	return
}

// SourceObjectChanges returns only changed descriptors and deletions.
func (s *Store) SourceObjectChanges(ctx context.Context, from, to model.SHA) (values []sourceview.Object, deletes []string, err error) {
	err = s.sourceDiff(ctx, from, to, true, func(change sourcegraph.Change) error {
		if !change.AfterExists {
			deletes = append(deletes, change.Key)
			return nil
		}
		var value sourceview.Object
		if err := json.Unmarshal(change.After, &value); err != nil {
			return err
		}
		if value.Key != change.Key {
			return managed.ErrReindex
		}
		if err := sourceview.ValidateObject(value); err != nil {
			return err
		}
		if change.BeforeExists {
			var before sourceview.Object
			if err := json.Unmarshal(change.Before, &before); err != nil {
				return err
			}
			before.EntityRef, before.EntityTip, before.RenderPolicy = "", "", ""
			after := value
			after.EntityRef, after.EntityTip, after.RenderPolicy = "", "", ""
			if before == after {
				return nil
			}
		}
		values = append(values, value)
		return nil
	})
	return
}

// OpenSourceContent keeps the managed lease through exact render or LFS descriptor acquisition.
func (s *Store) OpenSourceContent(ctx context.Context, token model.SHA, key string) (value sourceview.Object, reader io.ReadCloser, err error) {
	err = s.managedView(ctx, func(tx *bolt.Tx) error {
		revision, err := sourceRevision(tx, token)
		if err != nil {
			return err
		}
		if err := sourceProjection(revision); err != nil {
			return err
		}
		err = s.Repo.SourceObjects(ctx, func(scope *gitobj.SourceObjects) error {
			data, found, err := sourcegraph.New(scope).Get(revision.ObjectRoot, key)
			if err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return err
			}
			if value.Key != key {
				return managed.ErrReindex
			}
			return sourceview.ValidateObject(value)
		})
		if err != nil {
			return err
		}
		if value.Kind != sourceview.File {
			return ErrNotFound
		}
		if value.Attachment != "" {
			file, err := s.LFS().Open(value.Attachment)
			if err != nil {
				return err
			}
			reader = file
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if info.Size() != value.Size {
				return errors.New("source attachment size differs from immutable descriptor")
			}
			return nil
		}
		if value.RenderPolicy != sourceview.RenderPolicy {
			return sourceindex.ErrProjectionUnavailable
		}
		snapshot, err := s.LoadAt(ctx, value.EntityTip)
		if err != nil {
			return err
		}
		body := sourceview.CodecOf(snapshot.Meta().Kind).Render(snapshot)
		if int64(len(body)) != value.Size || sha256.Sum256(body) != value.Hash {
			return errors.New("source content differs from immutable descriptor")
		}
		reader = io.NopCloser(bytes.NewReader(body))
		return nil
	})
	if err != nil && reader != nil {
		err = errors.Join(err, reader.Close())
		reader = nil
	}
	return
}

var _ sourceindex.Backend = (*Store)(nil)
