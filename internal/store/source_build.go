package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

type projectionGraph struct {
	index    *sourcegraph.Map
	revision sourceindex.Revision
	// staged holds entities this build changed, nil for a removed one, until it
	// writes each into EntityRoot once.
	staged map[string]*sourceview.Entity
}

func (g *projectionGraph) Entity(kind model.Kind, id model.EntityID) (sourceview.Entity, bool, error) {
	if id == "" {
		return sourceview.Entity{}, false, nil
	}
	return g.entityRef(refs.For(kind, id))
}

func (g *projectionGraph) entityRef(ref string) (entity sourceview.Entity, found bool, err error) {
	if staged, ok := g.staged[ref]; ok {
		if staged == nil {
			return entity, false, nil
		}
		return *staged, true, nil
	}
	data, found, err := g.index.Get(g.revision.EntityRoot, ref)
	if err != nil || !found {
		return entity, found, err
	}
	err = json.Unmarshal(data, &entity)
	return entity, err == nil, err
}

func (g *projectionGraph) Members(prefix string) (values []sourceview.Entity, err error) {
	err = g.index.Prefix(g.revision.DependencyRoot, prefix, func(entry sourcegraph.Entry) error {
		entity, found, err := g.entityRef(string(entry.Value))
		if err != nil {
			return err
		}
		if !found {
			return managed.ErrReindex
		}
		values = append(values, entity)
		return nil
	})
	return
}

func (g *projectionGraph) putEntity(entity sourceview.Entity) error {
	data, err := json.Marshal(entity)
	if err != nil {
		return err
	}
	g.revision.EntityRoot, err = g.index.Put(g.revision.EntityRoot, entity.Ref, data)
	return err
}

type managedPreparation struct {
	Rows     []queryMetadata
	Entities map[string]sourceview.Entity
}

func (s *Store) prepareManaged(ctx context.Context, updates []gitcmd.RefUpdate, snapshots map[model.SHA]model.Snapshot) (*managedPreparation, error) {
	values := make(map[string]sourceview.Entity, len(updates))
	captured := make(map[string]sourceview.Entity, len(updates))
	if err := s.sourcePreparationRoots(ctx, updates, captured); err != nil {
		return nil, err
	}
	prepared := make(map[model.SHA]model.Snapshot, len(snapshots)+len(updates))
	for tip, snapshot := range snapshots {
		prepared[tip] = snapshot
	}
	for _, update := range updates {
		parsed, err := refs.Parse(update.Ref)
		if errors.Is(err, refs.ErrNotCCNotes) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if update.New == "" {
			continue
		}
		snapshot := prepared[update.New]
		root := model.PackCommit{SHA: captured[update.Ref].Root}
		if root.SHA == "" {
			if snapshot != nil && update.Old == "" && update.New == model.SHA(snapshot.EntityID()) {
				root.SHA = update.New
			} else {
				rooted, _, err := s.loadRootedAt(ctx, update.New)
				if err != nil {
					return nil, err
				}
				root = rooted.Root
				snapshot = rooted.Snapshot
			}
		}
		if snapshot == nil {
			var err error
			snapshot, _, err = s.loadAt(ctx, update.New)
			if err != nil {
				return nil, err
			}
		}
		if snapshot.EntityID() != parsed.ID || snapshot.Meta().Kind != parsed.Kind {
			return nil, fmt.Errorf("source entity differs from %s", update.Ref)
		}
		entity, err := sourceview.Describe(update.Ref, update.New, root, snapshot)
		if err != nil {
			return nil, err
		}
		values[update.Ref] = entity
		prepared[update.New] = snapshot
	}
	rows, err := s.managedRows(ctx, updates, prepared, nil)
	if err != nil {
		return nil, err
	}
	return &managedPreparation{Rows: rows, Entities: values}, nil
}

func (s *Store) sourcePreparationRoots(ctx context.Context, updates []gitcmd.RefUpdate, captured map[string]sourceview.Entity) error {
	return s.managedView(ctx, func(tx *bolt.Tx) error {
		revision, err := sourceRevision(tx, "")
		if errors.Is(err, managed.ErrReindex) {
			root, rootErr := managedRoot(tx)
			if rootErr != nil {
				return rootErr
			}
			key, _ := root.Bucket([]byte("refs")).Cursor().First()
			if key == nil {
				return nil
			}
		}
		if err != nil {
			return err
		}
		return s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			graph := projectionGraph{index: sourcegraph.New(objects), revision: revision}
			for _, update := range updates {
				if _, err := refs.Parse(update.Ref); err != nil {
					if errors.Is(err, refs.ErrNotCCNotes) {
						continue
					}
					return err
				}
				entity, found, err := graph.entityRef(update.Ref)
				if err != nil {
					return err
				}
				if found {
					captured[update.Ref] = entity
				}
			}
			return nil
		})
	})
}

func sourceJumps(tx *bolt.Tx, previous sourceindex.Revision) ([]model.SHA, error) {
	if previous.Token == "" {
		return nil, nil
	}
	jumps := []model.SHA{previous.Token}
	ancestor := previous
	for level := 0; level < len(ancestor.AncestorJumps); level++ {
		token := ancestor.AncestorJumps[level]
		jumps = append(jumps, token)
		var err error
		ancestor, err = sourceRevision(tx, token)
		if err != nil {
			return nil, err
		}
	}
	return jumps, nil
}

func (s *Store) buildSource(ctx context.Context, tx *bolt.Tx, updates []gitcmd.RefUpdate, values map[string]sourceview.Entity) (*sourcePublication, []gitcmd.RefUpdate, error) {
	previous, err := sourceRevision(tx, "")
	if err != nil && !errors.Is(err, managed.ErrReindex) {
		return nil, nil, err
	}
	if errors.Is(err, managed.ErrReindex) {
		root, rootErr := managedRoot(tx)
		if rootErr != nil {
			return nil, nil, rootErr
		}
		key, _ := root.Bucket([]byte("refs")).Cursor().First()
		if key != nil {
			return nil, nil, managed.ErrReindex
		}
		previous = sourceindex.Revision{ProjectionAvailable: true, RenderPolicy: sourceview.RenderPolicy}
	}
	changed := make([]gitcmd.RefUpdate, 0, len(updates))
	for _, update := range updates {
		if _, err := refs.Parse(update.Ref); err == nil {
			root, _ := managedRoot(tx)
			if model.SHA(root.Bucket([]byte("refs")).Get([]byte(update.Ref))) != update.New {
				changed = append(changed, update)
			}
		}
	}
	if len(changed) == 0 && previous.Token != "" {
		return nil, nil, nil
	}
	if previous.EntityGeneration == math.MaxUint64 || previous.Height == math.MaxUint64 {
		return nil, nil, errors.New("source generation overflow")
	}
	revision := previous
	if len(changed) != 0 {
		revision.EntityGeneration++
	}
	revision.Predecessor = previous.Token
	revision.Token = ""
	revision.ImportEvidence = ""
	revision.ProjectionDigest = [32]byte{}
	revision.Height++
	revision.AncestorJumps, err = sourceJumps(tx, previous)
	if err != nil {
		return nil, nil, err
	}
	err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		objects.HoldPacks()
		if previous.EntityRoot == "" {
			var err error
			revision, err = s.buildSourceSnapshot(objects, revision, values)
			if err != nil {
				return err
			}
			revision.Token, err = writeSourceRevision(objects, revision)
			return err
		}
		graph := projectionGraph{index: sourcegraph.New(objects), revision: revision, staged: make(map[string]*sourceview.Entity, len(changed))}
		before := make(map[string]sourceview.Entity, len(changed))
		affected := make(map[string]struct{}, len(changed))
		dependencyPrefixes := make(map[string]struct{})
		for _, update := range changed {
			old, found, err := graph.entityRef(update.Ref)
			if err != nil {
				return err
			}
			if found {
				before[update.Ref] = old
			}
			next, present := values[update.Ref]
			collectSourceDependents(old, next, dependencyPrefixes)
			affected[update.Ref] = struct{}{}
			kept := sourceview.Dependencies(next)
			for _, key := range sourceview.Dependencies(old) {
				if slices.Contains(kept, key) {
					continue
				}
				graph.revision.DependencyRoot, err = graph.index.Delete(graph.revision.DependencyRoot, key)
				if err != nil {
					return err
				}
			}
			for _, key := range kept {
				graph.revision.DependencyRoot, err = graph.index.Put(graph.revision.DependencyRoot, key, []byte(next.Ref))
				if err != nil {
					return err
				}
			}
			graph.staged[update.Ref] = nil
			if present {
				graph.staged[update.Ref] = &next
			}
		}
		for prefix := range dependencyPrefixes {
			members, err := graph.Members(prefix)
			if err != nil {
				return err
			}
			for _, member := range members {
				affected[member.Ref] = struct{}{}
				if strings.HasPrefix(prefix, "sprint-project/") {
					tasks, err := graph.Members("task-sprint/" + string(member.ID) + "/")
					if err != nil {
						return err
					}
					for _, task := range tasks {
						affected[task.Ref] = struct{}{}
					}
				}
			}
		}
		retained := sourcegraph.NewRetention(objects)
		retentionRoot := sourcegraph.RetentionRoot{Index: graph.revision.RetentionIndex, Commit: graph.revision.RetentionRoot}
		for _, update := range changed {
			old := before[update.Ref]
			next, present := values[update.Ref]
			if err := graph.updateLFS(s, old, next); err != nil {
				return err
			}
			if present {
				entity := sourcegraph.RetainedEntity{Tip: next.Tip}
				if !next.Deleted {
					for _, attachment := range next.Attachments {
						entity.LFS = append(entity.LFS, gitobj.SourceOperationLFS{OID: attachment.OID, Size: attachment.Size})
					}
				}
				retentionRoot, err = retained.Put(retentionRoot, update.Ref, entity)
			} else {
				retentionRoot, err = retained.Delete(retentionRoot, update.Ref)
			}
			if err != nil {
				return err
			}
		}
		graph.revision.RetentionIndex, graph.revision.RetentionRoot = retentionRoot.Index, retentionRoot.Commit
		ordered := make([]string, 0, len(affected))
		for ref := range affected {
			ordered = append(ordered, ref)
		}
		slices.Sort(ordered)
		retired := make(map[string]sourceview.Object)
		for _, ref := range ordered {
			old, found := before[ref]
			if !found {
				var err error
				old, _, err = graph.entityRef(ref)
				if err != nil {
					return err
				}
			}
			for _, entry := range old.Objects {
				retired[entry.Key] = entry
			}
			if old.ProjectionError != "" {
				graph.revision.ProjectionErrors--
			}
		}
		var projected []sourceview.Entity
		emitted := make(map[string]sourceview.Object)
		for _, ref := range ordered {
			next, found, err := graph.entityRef(ref)
			if err != nil {
				return err
			}
			if !found {
				graph.revision.EntityRoot, err = graph.index.Delete(graph.revision.EntityRoot, ref)
				if err != nil {
					return err
				}
				continue
			}
			objects, emitErr := sourceview.Emit(next, &graph)
			next.ProjectionError = ""
			if emitErr != nil {
				next.ProjectionError = emitErr.Error()
				graph.revision.ProjectionErrors++
				objects = nil
			}
			next.Objects = objects
			for _, entry := range objects {
				if _, duplicate := emitted[entry.Key]; duplicate {
					return fmt.Errorf("duplicate source key %s", entry.Key)
				}
				emitted[entry.Key] = entry
			}
			projected = append(projected, next)
		}
		for _, key := range slices.Sorted(maps.Keys(retired)) {
			if _, found := emitted[key]; found {
				continue
			}
			if err := graph.deleteObject(retired[key]); err != nil {
				return err
			}
		}
		for _, key := range slices.Sorted(maps.Keys(emitted)) {
			if err := graph.replaceObject(retired[key], emitted[key]); err != nil {
				return err
			}
		}
		for _, next := range projected {
			if err := graph.putEntity(next); err != nil {
				return err
			}
		}
		revision = graph.revision
		token, err := writeSourceRevision(objects, revision)
		if err != nil {
			return err
		}
		revision.Token = token
		if revision.RetentionRoot == "" {
			return objects.KeepOnly(token)
		}
		return objects.KeepOnly(token, revision.RetentionRoot)
	})
	if err != nil {
		return nil, nil, err
	}
	extra := []gitcmd.RefUpdate{{Ref: sourceindex.Ref, Old: previous.Token, New: revision.Token}}
	if previous.RetentionRoot != revision.RetentionRoot {
		extra = append(extra, gitcmd.RefUpdate{Ref: sourceindex.LiveRef, Old: previous.RetentionRoot, New: revision.RetentionRoot})
	}
	return &sourcePublication{Revision: &revision}, extra, nil
}

func collectSourceDependents(old, next sourceview.Entity, prefixes map[string]struct{}) {
	kind := next.Kind
	if kind == "" {
		kind = old.Kind
	}
	if kind == model.KindSprint && (old.Visible != next.Visible || old.Project != next.Project) {
		for _, entity := range []sourceview.Entity{old, next} {
			if entity.ID != "" {
				prefixes["task-sprint/"+string(entity.ID)+"/"] = struct{}{}
			}
		}
	}
	if kind == model.KindProject && old.Visible != next.Visible {
		for _, entity := range []sourceview.Entity{old, next} {
			if entity.ID != "" {
				prefixes["task-project/"+string(entity.ID)+"/"] = struct{}{}
				prefixes["sprint-project/"+string(entity.ID)+"/"] = struct{}{}
			}
		}
	}
	if old.Visible != next.Visible || !slices.Equal(old.Attachments, next.Attachments) {
		for _, entity := range []sourceview.Entity{old, next} {
			for _, key := range sourceview.Dependencies(entity) {
				if strings.HasPrefix(key, "attachment-owner/") {
					last := strings.LastIndexByte(key, '/')
					prefixes[key[:last+1]] = struct{}{}
				}
			}
		}
	}
}

func (g *projectionGraph) replaceName(entry sourceview.Object, remove bool) error {
	key := entry.Parent + "\x00" + entry.Name
	data, found, err := g.index.Get(g.revision.NameRoot, key)
	if err != nil {
		return err
	}
	var members []string
	if found {
		if err := json.Unmarshal(data, &members); err != nil {
			return err
		}
	}
	wasConflict := len(members) > 1
	position, exists := slices.BinarySearch(members, entry.Key)
	if remove {
		if !exists {
			return managed.ErrReindex
		}
		members = slices.Delete(members, position, position+1)
	} else {
		if exists {
			return fmt.Errorf("duplicate source key %s", entry.Key)
		}
		members = slices.Insert(members, position, entry.Key)
	}
	isConflict := len(members) > 1
	if wasConflict && !isConflict {
		g.revision.ProjectionConflicts--
	}
	if !wasConflict && isConflict {
		g.revision.ProjectionConflicts++
	}
	if len(members) == 0 {
		g.revision.NameRoot, err = g.index.Delete(g.revision.NameRoot, key)
	} else {
		data, err = json.Marshal(members)
		if err != nil {
			return err
		}
		g.revision.NameRoot, err = g.index.Put(g.revision.NameRoot, key, data)
	}
	return err
}

func (g *projectionGraph) deleteObject(entry sourceview.Object) error {
	if err := g.replaceName(entry, true); err != nil {
		return err
	}
	var err error
	g.revision.ObjectRoot, err = g.index.Delete(g.revision.ObjectRoot, entry.Key)
	if err != nil {
		return err
	}
	if sourceview.ValidateObject(entry) != nil {
		g.revision.ProjectionErrors--
	}
	return nil
}

func (g *projectionGraph) putObject(entry sourceview.Object) error {
	if err := g.replaceName(entry, false); err != nil {
		return err
	}
	return g.writeObject(entry)
}

func (g *projectionGraph) writeObject(entry sourceview.Object) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	g.revision.ObjectRoot, err = g.index.Put(g.revision.ObjectRoot, entry.Key, data)
	if err != nil {
		return err
	}
	if sourceview.ValidateObject(entry) != nil {
		g.revision.ProjectionErrors++
	}
	return nil
}

func (g *projectionGraph) replaceObject(old, entry sourceview.Object) error {
	if old == entry {
		return nil
	}
	if old.Key == "" {
		return g.putObject(entry)
	}
	if old.Parent != entry.Parent || old.Name != entry.Name {
		if err := g.replaceName(old, true); err != nil {
			return err
		}
		if err := g.replaceName(entry, false); err != nil {
			return err
		}
	}
	if sourceview.ValidateObject(old) != nil {
		g.revision.ProjectionErrors--
	}
	return g.writeObject(entry)
}

func (g *projectionGraph) updateLFS(s *Store, old, next sourceview.Entity) error {
	deltas := make(map[string]int64)
	for _, change := range []struct {
		entity sourceview.Entity
		delta  int64
	}{{old, -1}, {next, 1}} {
		if change.entity.Deleted {
			continue
		}
		for _, attachment := range change.entity.Attachments {
			key := "oid/" + attachment.OID + "/" + strconv.FormatInt(attachment.Size, 10)
			deltas[key] += change.delta
		}
	}
	oids := make(map[string]struct{})
	for key, delta := range deltas {
		if delta == 0 {
			continue
		}
		oid := strings.Split(key, "/")[1]
		oids[oid] = struct{}{}
	}
	invalid := func(oid string) (bool, error) {
		count := 0
		bad := false
		err := g.index.Prefix(g.revision.LFSRoot, "oid/"+oid+"/", func(entry sourcegraph.Entry) error {
			count++
			size, err := strconv.ParseInt(entry.Key[strings.LastIndexByte(entry.Key, '/')+1:], 10, 64)
			if err != nil {
				return err
			}
			info, err := os.Stat(s.LFS().Path(oid))
			if !model.ValidAttachmentOID(oid) || size <= 0 || err != nil || info.Size() != size {
				bad = true
			}
			return nil
		})
		return count > 1 || bad, err
	}
	for oid := range oids {
		summary, _, err := g.index.Get(g.revision.LFSRoot, "invalid/"+oid)
		if err != nil {
			return err
		}
		wasInvalid := string(summary) == "1"
		for key, delta := range deltas {
			if !strings.HasPrefix(key, "oid/"+oid+"/") || delta == 0 {
				continue
			}
			data, found, err := g.index.Get(g.revision.LFSRoot, key)
			if err != nil {
				return err
			}
			var count int64
			if found {
				count, err = strconv.ParseInt(string(data), 10, 64)
				if err != nil {
					return err
				}
			}
			count += delta
			if count < 0 {
				return managed.ErrReindex
			}
			if count == 0 {
				g.revision.LFSRoot, err = g.index.Delete(g.revision.LFSRoot, key)
			} else {
				g.revision.LFSRoot, err = g.index.Put(g.revision.LFSRoot, key, []byte(strconv.FormatInt(count, 10)))
			}
			if err != nil {
				return err
			}
		}
		isInvalid, err := invalid(oid)
		if err != nil {
			return err
		}
		if wasInvalid && !isInvalid {
			g.revision.RetentionConflicts--
		}
		if !wasInvalid && isInvalid {
			g.revision.RetentionConflicts++
		}
		if isInvalid {
			g.revision.LFSRoot, err = g.index.Put(g.revision.LFSRoot, "invalid/"+oid, []byte("1"))
		} else {
			g.revision.LFSRoot, err = g.index.Delete(g.revision.LFSRoot, "invalid/"+oid)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeSourceRevision(objects *gitobj.SourceObjects, revision sourceindex.Revision) (model.SHA, error) {
	revision.Token = ""
	data, err := json.Marshal(revision)
	if err != nil {
		return "", err
	}
	blob, err := objects.WriteBlob(data)
	if err != nil {
		return "", err
	}
	entries := []object.TreeEntry{{Name: "revision", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))}}
	for _, root := range []struct {
		name string
		sha  model.SHA
	}{{"entities", revision.EntityRoot}, {"objects", revision.ObjectRoot}, {"dependencies", revision.DependencyRoot}, {"names", revision.NameRoot}, {"lfs", revision.LFSRoot}, {"retention-metadata", revision.RetentionIndex}} {
		if root.sha != "" {
			entries = append(entries, object.TreeEntry{Name: root.name, Mode: filemode.Dir, Hash: plumbing.NewHash(string(root.sha))})
		}
	}
	tree, err := objects.WriteTree(entries)
	if err != nil {
		return "", err
	}
	var parents []model.SHA
	if revision.Predecessor != "" {
		parents = []model.SHA{revision.Predecessor}
	}
	identity := gitobj.Signature{Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: time.Unix(0, 0).UTC()}
	return objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Parents: parents, Author: identity, Committer: identity, Message: "cc-notes: managed source revision v2"})
}
