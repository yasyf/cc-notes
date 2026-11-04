package store

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

type coldProjection struct {
	entities     map[string]sourceview.Entity
	dependencies []sourcegraph.Entry
}

func (c *coldProjection) Entity(kind model.Kind, id model.EntityID) (sourceview.Entity, bool, error) {
	entity, found := c.entities[refs.For(kind, id)]
	return entity, found && id != "", nil
}

func (c *coldProjection) Members(prefix string) ([]sourceview.Entity, error) {
	start, _ := slices.BinarySearchFunc(c.dependencies, prefix, func(entry sourcegraph.Entry, key string) int { return strings.Compare(entry.Key, key) })
	result := make([]sourceview.Entity, 0)
	for _, entry := range c.dependencies[start:] {
		if !strings.HasPrefix(entry.Key, prefix) {
			break
		}
		result = append(result, c.entities[string(entry.Value)])
	}
	return result, nil
}

func (s *Store) buildSourceSnapshot(objects *gitobj.SourceObjects, revision sourceindex.Revision, entities map[string]sourceview.Entity) (sourceindex.Revision, error) {
	index := sourcegraph.New(objects)
	reader := coldProjection{entities: entities}
	ordered := make([]string, 0, len(entities))
	for ref, entity := range entities {
		ordered = append(ordered, ref)
		for _, key := range sourceview.Dependencies(entity) {
			reader.dependencies = append(reader.dependencies, sourcegraph.Entry{Key: key, Value: []byte(ref)})
		}
	}
	slices.Sort(ordered)
	slices.SortFunc(reader.dependencies, func(a, b sourcegraph.Entry) int { return strings.Compare(a.Key, b.Key) })
	objectsByKey := make(map[string]sourceview.Object)
	names := make(map[string][]string)
	revision.ProjectionErrors, revision.ProjectionConflicts, revision.RetentionConflicts = 0, 0, 0
	addObject := func(entry sourceview.Object) error {
		if _, found := objectsByKey[entry.Key]; found {
			return fmt.Errorf("duplicate source key %s", entry.Key)
		}
		objectsByKey[entry.Key] = entry
		name := entry.Parent + "\x00" + entry.Name
		names[name] = append(names[name], entry.Key)
		if sourceview.ValidateObject(entry) != nil {
			revision.ProjectionErrors++
		}
		return nil
	}
	for _, entry := range sourceview.BaseObjects() {
		if err := addObject(entry); err != nil {
			return revision, err
		}
	}
	entityEntries := make([]sourcegraph.Entry, 0, len(entities))
	retained := make([]sourcegraph.RetentionEntry, 0, len(entities))
	lfsCounts := make(map[string]int64)
	lfsSizes := make(map[string]map[int64]struct{})
	for _, ref := range ordered {
		entity := entities[ref]
		emitted, emitErr := sourceview.Emit(entity, &reader)
		entity.ProjectionError = ""
		if emitErr != nil {
			entity.ProjectionError = emitErr.Error()
			revision.ProjectionErrors++
			emitted = nil
		}
		entity.Objects = emitted
		for _, entry := range emitted {
			if err := addObject(entry); err != nil {
				return revision, err
			}
		}
		data, err := json.Marshal(entity)
		if err != nil {
			return revision, err
		}
		entityEntries = append(entityEntries, sourcegraph.Entry{Key: ref, Value: data})
		retention := sourcegraph.RetainedEntity{Tip: entity.Tip}
		if !entity.Deleted {
			for _, attachment := range entity.Attachments {
				retention.LFS = append(retention.LFS, gitobj.SourceOperationLFS{OID: attachment.OID, Size: attachment.Size})
				key := "oid/" + attachment.OID + "/" + strconv.FormatInt(attachment.Size, 10)
				lfsCounts[key]++
				if lfsSizes[attachment.OID] == nil {
					lfsSizes[attachment.OID] = make(map[int64]struct{})
				}
				lfsSizes[attachment.OID][attachment.Size] = struct{}{}
			}
		}
		retained = append(retained, sourcegraph.RetentionEntry{Key: ref, Entity: retention})
	}
	objectEntries := make([]sourcegraph.Entry, 0, len(objectsByKey))
	for key, entry := range objectsByKey {
		data, err := json.Marshal(entry)
		if err != nil {
			return revision, err
		}
		objectEntries = append(objectEntries, sourcegraph.Entry{Key: key, Value: data})
	}
	nameEntries := make([]sourcegraph.Entry, 0, len(names))
	for key, members := range names {
		slices.Sort(members)
		if len(members) > 1 {
			revision.ProjectionConflicts++
		}
		data, err := json.Marshal(members)
		if err != nil {
			return revision, err
		}
		nameEntries = append(nameEntries, sourcegraph.Entry{Key: key, Value: data})
	}
	lfsEntries := make([]sourcegraph.Entry, 0, len(lfsCounts)+len(lfsSizes))
	for key, count := range lfsCounts {
		lfsEntries = append(lfsEntries, sourcegraph.Entry{Key: key, Value: []byte(strconv.FormatInt(count, 10))})
	}
	for oid, sizes := range lfsSizes {
		invalid := len(sizes) > 1 || !model.ValidAttachmentOID(oid)
		info, err := os.Stat(s.LFS().Path(oid))
		for size := range sizes {
			invalid = invalid || size <= 0 || err != nil || info.Size() != size
		}
		if invalid {
			revision.RetentionConflicts++
			lfsEntries = append(lfsEntries, sourcegraph.Entry{Key: "invalid/" + oid, Value: []byte("1")})
		}
	}
	for _, build := range []struct {
		root    *model.SHA
		entries []sourcegraph.Entry
	}{
		{&revision.EntityRoot, entityEntries},
		{&revision.DependencyRoot, reader.dependencies},
		{&revision.ObjectRoot, objectEntries},
		{&revision.NameRoot, nameEntries},
		{&revision.LFSRoot, lfsEntries},
	} {
		root, err := index.Build(build.entries)
		if err != nil {
			return revision, err
		}
		*build.root = root
	}
	retention, err := sourcegraph.NewRetention(objects).Build(retained)
	if err != nil {
		return revision, err
	}
	revision.RetentionIndex, revision.RetentionRoot = retention.Index, retention.Commit
	return revision, nil
}
