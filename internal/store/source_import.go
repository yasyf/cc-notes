package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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

type importedSource struct {
	publications *foldPublicationBatch
	current      map[string]model.SHA
	rows         map[string]queryMetadata
	revisions    map[model.SHA]sourceindex.Revision
	manifests    map[model.SHA]gitobj.SourceManifest
	operations   map[string]sourceindex.Operation
	applied      map[model.SHA][]string
	legacyProofs map[string]gitobj.SourceOperationProof
	proofUpdates []gitcmd.RefUpdate
	legacy       map[model.SHA]bool
	head         model.SHA
	catalog      model.SHA
	live         model.SHA
}

func (s *Store) importSourceMetadata(ctx context.Context, publications *foldPublicationBatch, current map[string]model.SHA) (*importedSource, error) {
	result := &importedSource{publications: publications, current: current, rows: make(map[string]queryMetadata), revisions: make(map[model.SHA]sourceindex.Revision), manifests: make(map[model.SHA]gitobj.SourceManifest), operations: make(map[string]sourceindex.Operation), applied: make(map[model.SHA][]string), legacyProofs: make(map[string]gitobj.SourceOperationProof), legacy: make(map[model.SHA]bool)}
	queue := make([]model.SHA, 0)
	tip := func(ref string) (model.SHA, error) {
		sha, err := s.Repo.Tip(ctx, ref)
		if errors.Is(err, gitobj.ErrRefNotFound) {
			return "", nil
		}
		return sha, err
	}
	var err error
	result.head, err = tip(sourceindex.Ref)
	if err != nil {
		return nil, err
	}
	result.catalog, err = tip(sourceindex.CatalogRef)
	if err != nil {
		return nil, err
	}
	result.live, err = tip(sourceindex.LiveRef)
	if err != nil {
		return nil, err
	}
	modernHead := result.head
	if result.live != "" && modernHead == "" {
		return nil, fmt.Errorf("reserved live source ref has no owned v2 head: %s", sourceindex.LiveRef)
	}
	legacyHead, err := tip(sourceindex.LegacyRef)
	if err != nil {
		return nil, err
	}
	if result.head != "" {
		queue = append(queue, result.head)
	} else {
		result.head = legacyHead
	}
	if legacyHead != "" {
		queue = append(queue, legacyHead)
	}
	operations, err := s.Repo.ListPrefix(ctx, sourceindex.OperationPrefix)
	if err != nil {
		return nil, err
	}
	operationRefs := make([]string, 0, len(operations))
	for ref := range operations {
		operationRefs = append(operationRefs, ref)
	}
	slices.Sort(operationRefs)
	for _, ref := range operationRefs {
		id := strings.TrimPrefix(ref, sourceindex.OperationPrefix)
		if _, err := sourceindex.OperationRef(id); err != nil {
			return nil, err
		}
		sha := operations[ref]
		operation, modern, err := s.readManagedSourceProof(ctx, sha, id)
		if err != nil {
			return nil, err
		}
		if !modern {
			proof, err := s.Repo.ReadSourceOperationProof(ctx, sha)
			if err != nil {
				return nil, fmt.Errorf("import receipt %s: %w", id, err)
			}
			if proof.OperationID != id {
				return nil, fmt.Errorf("import receipt %s: proof identity differs", id)
			}
			result.legacyProofs[id] = proof
			operation = sourceindex.Operation{Proof: sha, Previous: proof.Expected, Token: proof.Committed, Result: proof.Result, RequestDigest: proof.RequestDigest, ReceiptDigest: proof.ReceiptDigest, State: proof.State}
		}
		pin, err := tip(sourceindex.OperationPinRef(id))
		if err != nil {
			return nil, err
		}
		if operation.State == gitobj.SourceOperationApplied {
			if pin != sha {
				return nil, fmt.Errorf("import Applied receipt %s token %s: content pin differs", id, operation.Token)
			}
			operation.Pin = pin
			result.applied[operation.Token] = append(result.applied[operation.Token], id)
		} else if pin != "" {
			return nil, fmt.Errorf("import receipt %s: settled content pin remains", id)
		}
		result.operations[id] = operation
		if operation.Previous != "" {
			queue = append(queue, operation.Previous)
		}
		if operation.Token != "" {
			queue = append(queue, operation.Token)
		}
	}
	if result.catalog != "" {
		err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			commit, err := objects.ReadCommit(result.catalog)
			if err != nil {
				return err
			}
			if commit.Message != "cc-notes: imported source metadata" || len(commit.Parents) != 0 {
				return managed.ErrReindex
			}
			entries, err := objects.ReadTree(commit.Tree)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Mode != filemode.Dir || !plumbing.IsHash(entry.Name) {
					return fmt.Errorf("source catalog has unexpected entry %s", entry.Name)
				}
				revision, err := readSourceRevisionTree(objects, model.SHA(entry.Hash.String()), model.SHA(entry.Name))
				if err != nil {
					return err
				}
				result.revisions[revision.Token] = revision
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	seen := make(map[model.SHA]bool)
	for len(queue) > 0 {
		token := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[token] {
			continue
		}
		seen[token] = true
		if revision, found := result.revisions[token]; found {
			if revision.Predecessor != "" {
				queue = append(queue, revision.Predecessor)
			}
			continue
		}
		var revision sourceindex.Revision
		modern := false
		err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			commit, err := objects.ReadCommit(token)
			if err != nil {
				return err
			}
			entries, err := objects.ReadTree(commit.Tree)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.Name == "revision" {
					modern = true
					revision, err = readSourceRevisionTree(objects, commit.Tree, token)
					if err != nil {
						return err
					}
					if len(commit.Parents) > 1 || len(commit.Parents) == 0 && revision.Predecessor != "" || len(commit.Parents) == 1 && commit.Parents[0] != revision.Predecessor {
						return managed.ErrReindex
					}
					return nil
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("import source revision %s: %w", token, err)
		}
		if !modern {
			manifest, previous, err := s.Repo.ReadSourceManifestCommit(ctx, token)
			if err != nil {
				return nil, err
			}
			result.manifests[token] = manifest
			result.legacy[token] = true
			revision = sourceindex.Revision{Token: token, Predecessor: previous}
		}
		result.revisions[token] = revision
		if revision.Predecessor != "" {
			queue = append(queue, revision.Predecessor)
		}
	}
	if modernHead != "" && result.live != "" {
		revision, found := result.revisions[modernHead]
		if !found || revision.RetentionRoot != result.live {
			return nil, fmt.Errorf("reserved live source ref differs from its owned v2 head: %s", sourceindex.LiveRef)
		}
	}
	return result, nil
}

func (s *Store) readManagedSourceProof(ctx context.Context, sha model.SHA, id string) (operation sourceindex.Operation, modern bool, err error) {
	err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		commit, err := objects.ReadCommit(sha)
		if err != nil {
			return err
		}
		entries, err := objects.ReadTree(commit.Tree)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name != "source-operation-v2" {
				continue
			}
			modern = true
			if len(entries) != 1 || entry.Mode != filemode.Regular {
				return managed.ErrReindex
			}
			data, err := objects.ReadBlob(model.SHA(entry.Hash.String()))
			if err != nil {
				return err
			}
			var body struct {
				Format      string
				OperationID string
				Operation   sourceindex.Operation
			}
			if err := json.Unmarshal(data, &body); err != nil {
				return err
			}
			if body.Format != "cc-notes-source-operation-v2" || body.OperationID != id {
				return managed.ErrReindex
			}
			canonical, err := json.Marshal(body)
			if err != nil {
				return err
			}
			if !bytes.Equal(canonical, data) || body.Operation.Proof != "" || body.Operation.Pin != "" {
				return managed.ErrReindex
			}
			if err := validateManagedSourceOperation(id, body.Operation); err != nil {
				return err
			}
			operation = body.Operation
			operation.Proof = sha
			expected := []model.SHA(nil)
			if operation.State != gitobj.SourceOperationForgotten {
				expected = append(expected, operation.Token)
			}
			if operation.State == gitobj.SourceOperationApplied && operation.RetentionRoot != "" {
				expected = append(expected, operation.RetentionRoot)
			}
			if !slices.Equal(expected, commit.Parents) {
				return managed.ErrReindex
			}
			return nil
		}
		return nil
	})
	return
}

func readSourceRevisionTree(objects *gitobj.SourceObjects, tree, token model.SHA) (revision sourceindex.Revision, err error) {
	entries, err := objects.ReadTree(tree)
	if err != nil {
		return revision, err
	}
	roots := make(map[string]model.SHA, len(entries))
	for _, entry := range entries {
		if _, found := roots[entry.Name]; found {
			return revision, managed.ErrReindex
		}
		roots[entry.Name] = model.SHA(entry.Hash.String())
		if entry.Name == "revision" {
			if entry.Mode != filemode.Regular {
				return revision, managed.ErrReindex
			}
			data, err := objects.ReadBlob(model.SHA(entry.Hash.String()))
			if err != nil {
				return revision, err
			}
			if err := json.Unmarshal(data, &revision); err != nil {
				return revision, err
			}
		} else if entry.Mode != filemode.Dir {
			return revision, managed.ErrReindex
		}
	}
	if roots["revision"] == "" || revision.Token != "" {
		return revision, managed.ErrReindex
	}
	expected := map[string]model.SHA{"entities": revision.EntityRoot, "objects": revision.ObjectRoot, "dependencies": revision.DependencyRoot, "names": revision.NameRoot, "lfs": revision.LFSRoot, "retention-metadata": revision.RetentionIndex}
	for name, sha := range expected {
		if roots[name] != sha {
			return revision, managed.ErrReindex
		}
		delete(roots, name)
	}
	if len(roots) != 1 {
		return revision, managed.ErrReindex
	}
	revision.Token = token
	return revision, nil
}

func (s *Store) materializeSourceImport(ctx context.Context, imported *importedSource, token model.SHA, policy sourceindex.ImportPolicy) (sourceindex.Revision, error) {
	revision := imported.revisions[token]
	if !imported.legacy[token] {
		return revision, nil
	}
	manifest := imported.manifests[token]
	entities := make(map[string]sourceview.Entity, len(manifest))
	for _, entry := range manifest {
		rooted, publication, err := s.loadRootedAt(ctx, entry.Tip)
		if err != nil {
			if len(imported.applied[token]) != 0 {
				return revision, fmt.Errorf("import Applied receipts %v token %s missing entity %s at %s: %w", imported.applied[token], token, entry.Ref, entry.Tip, err)
			}
			return s.metadataOnlySourceImport(ctx, revision, manifest)
		}
		imported.publications.add(publication)
		if imported.current[entry.Ref] == entry.Tip {
			if _, found := imported.rows[entry.Ref]; !found {
				rows, err := s.managedRows(ctx, []gitcmd.RefUpdate{{Ref: entry.Ref, New: entry.Tip}}, map[model.SHA]model.Snapshot{entry.Tip: rooted.Snapshot}, nil)
				if err != nil {
					return revision, err
				}
				imported.rows[entry.Ref] = rows[0]
			}
		}
		entity, err := sourceview.Describe(entry.Ref, entry.Tip, rooted.Root, rooted.Snapshot)
		if err != nil {
			return revision, err
		}
		entities[entry.Ref] = entity
	}
	var provenance sourceindex.ImportProvenance
	if len(imported.applied[token]) != 0 {
		if policy == nil {
			return revision, fmt.Errorf("applied receipts %v token %s: authenticated historical renderer evidence is required", imported.applied[token], token)
		}
		var err error
		provenance, err = policy.SourceProvenance(ctx, token, manifest)
		if err != nil {
			return revision, err
		}
		if provenance.Evidence == "" || provenance.RenderPolicy != sourceview.RenderPolicy || provenance.ObjectDigest == ([sha256.Size]byte{}) {
			return revision, fmt.Errorf("applied receipts %v token %s: historical renderer evidence is incomplete", imported.applied[token], token)
		}
	}
	revision.ProjectionAvailable = true
	revision.RenderPolicy = sourceview.RenderPolicy
	err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		var err error
		revision, err = s.buildSourceSnapshot(objects, revision, entities)
		if err != nil {
			return err
		}
		digest, err := projectionDigest(sourcegraph.New(objects), revision.ObjectRoot)
		if err != nil {
			return err
		}
		if len(imported.applied[token]) != 0 && digest != provenance.ObjectDigest {
			return fmt.Errorf("source token %s: renderer descriptor digest differs from %s", token, provenance.Evidence)
		}
		if len(imported.applied[token]) != 0 {
			revision.ImportEvidence = provenance.Evidence
			revision.ProjectionDigest = provenance.ObjectDigest
		}
		if len(imported.applied[token]) != 0 && revision.RetentionConflicts != 0 {
			return fmt.Errorf("applied receipts %v token %s: required LFS content is missing or inconsistent", imported.applied[token], token)
		}
		return nil
	})
	return revision, err
}

func (s *Store) metadataOnlySourceImport(ctx context.Context, revision sourceindex.Revision, manifest gitobj.SourceManifest) (sourceindex.Revision, error) {
	err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		index := sourcegraph.New(objects)
		for _, entry := range manifest {
			parsed, err := refs.Parse(entry.Ref)
			if err != nil {
				return err
			}
			data, err := json.Marshal(sourceview.Entity{Ref: entry.Ref, Tip: entry.Tip, Kind: parsed.Kind, ID: parsed.ID})
			if err != nil {
				return err
			}
			revision.EntityRoot, err = index.Put(revision.EntityRoot, entry.Ref, data)
			if err != nil {
				return err
			}
		}
		revision.ProjectionAvailable = false
		return nil
	})
	return revision, err
}

func projectionDigest(index *sourcegraph.Map, root model.SHA) ([sha256.Size]byte, error) {
	digest := sha256.New()
	err := index.Prefix(root, "", func(entry sourcegraph.Entry) error {
		var value sourceview.Object
		if err := json.Unmarshal(entry.Value, &value); err != nil {
			return err
		}
		value.EntityRef, value.EntityTip, value.RenderPolicy = "", "", ""
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := digest.Write(append(data, '\n')); err != nil {
			return err
		}
		return nil
	})
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, err
}

func importedManifestChanges(before, after gitobj.SourceManifest) sourceindex.Changes {
	old := before.Tips()
	changes := sourceindex.Changes{}
	for _, entry := range after {
		if old[entry.Ref] != entry.Tip {
			changes.Upserts = append(changes.Upserts, entry)
		}
		delete(old, entry.Ref)
	}
	for ref := range old {
		changes.Deletes = append(changes.Deletes, ref)
	}
	slices.Sort(changes.Deletes)
	return changes
}

func (s *Store) normalizeSourceImport(ctx context.Context, db *bolt.DB, imported *importedSource, policy sourceindex.ImportPolicy) error {
	visiting := make(map[model.SHA]bool)
	complete := make(map[model.SHA]bool)
	var visit func(model.SHA) error
	visit = func(token model.SHA) error {
		if token == "" || complete[token] {
			return nil
		}
		if visiting[token] {
			return errors.New("source revision predecessor cycle")
		}
		visiting[token] = true
		revision := imported.revisions[token]
		if err := visit(revision.Predecessor); err != nil {
			return err
		}
		if imported.legacy[token] {
			var err error
			if len(imported.applied[token]) == 0 {
				revision, err = s.metadataOnlySourceImport(ctx, revision, imported.manifests[token])
			} else {
				revision, err = s.materializeSourceImport(ctx, imported, token, policy)
			}
			if err != nil {
				return err
			}
			if revision.Predecessor != "" {
				previous := imported.revisions[revision.Predecessor]
				revision.Height = previous.Height + 1
				revision.EntityGeneration = previous.EntityGeneration
				if !imported.manifests[token].Equal(imported.manifests[revision.Predecessor]) {
					revision.EntityGeneration++
				}
				revision.AncestorJumps = []model.SHA{previous.Token}
				ancestor := previous
				for level := 0; level < len(ancestor.AncestorJumps); level++ {
					next := ancestor.AncestorJumps[level]
					revision.AncestorJumps = append(revision.AncestorJumps, next)
					ancestor = imported.revisions[next]
				}
			} else {
				revision.Height = 1
				if len(imported.manifests[token]) > 0 {
					revision.EntityGeneration = 1
				}
			}
		}
		imported.revisions[token] = revision
		if err := db.Update(func(tx *bolt.Tx) error {
			return applySource(tx, &sourcePublication{Revisions: []sourceindex.Revision{revision}})
		}); err != nil {
			return err
		}
		complete[token] = true
		delete(visiting, token)
		return nil
	}
	tokens := make([]model.SHA, 0, len(imported.revisions))
	for token := range imported.revisions {
		tokens = append(tokens, token)
	}
	slices.Sort(tokens)
	for _, token := range tokens {
		if err := visit(token); err != nil {
			return err
		}
	}
	if err := s.validateAppliedImports(ctx, imported); err != nil {
		return err
	}
	ids := make([]string, 0, len(imported.operations))
	for id := range imported.operations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		operation := imported.operations[id]
		if operation.State != gitobj.SourceOperationForgotten {
			if manifest, found := imported.manifests[operation.Token]; found {
				before := imported.manifests[operation.Previous]
				operation.Changes = importedManifestChanges(before, manifest)
			}
			if operation.State == gitobj.SourceOperationApplied {
				revision := imported.revisions[operation.Token]
				if !revision.ProjectionAvailable {
					return fmt.Errorf("applied receipt %s token %s has no complete projection", id, operation.Token)
				}
				operation.RetentionRoot = revision.RetentionRoot
				if _, legacy := imported.legacyProofs[id]; legacy {
					oldProof, oldPin := operation.Proof, operation.Pin
					proof, err := s.writeManagedSourceProof(ctx, id, operation)
					if err != nil {
						return err
					}
					operation.Proof, operation.Pin = proof, proof
					ref, _ := sourceindex.OperationRef(id)
					imported.proofUpdates = append(imported.proofUpdates, gitcmd.RefUpdate{Ref: ref, Old: oldProof, New: proof}, gitcmd.RefUpdate{Ref: sourceindex.OperationPinRef(id), Old: oldPin, New: proof})
				}
			}
		}
		imported.operations[id] = operation
		if err := db.Update(func(tx *bolt.Tx) error {
			return applySource(tx, &sourcePublication{OperationID: id, Operation: &operation})
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) writeSourceCatalog(ctx context.Context, imported *importedSource) (sha model.SHA, err error) {
	err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		entries := make([]object.TreeEntry, 0, len(imported.revisions))
		for token, revision := range imported.revisions {
			commitSHA, err := writeSourceRevision(objects, revision)
			if err != nil {
				return err
			}
			commit, err := objects.ReadCommit(commitSHA)
			if err != nil {
				return err
			}
			entries = append(entries, object.TreeEntry{Name: string(token), Mode: filemode.Dir, Hash: plumbing.NewHash(string(commit.Tree))})
		}
		tree, err := objects.WriteTree(entries)
		if err != nil {
			return err
		}
		identity := gitobj.Signature{Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: time.Unix(0, 0).UTC()}
		sha, err = objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Author: identity, Committer: identity, Message: "cc-notes: imported source metadata"})
		return err
	})
	return
}

func sourceImportsSame(ctx context.Context, s *Store, revision sourceindex.Revision, tips map[string]model.SHA) (bool, error) {
	same := true
	err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		index := sourcegraph.New(objects)
		count, err := index.Count(revision.EntityRoot)
		if err != nil {
			return err
		}
		if count != uint64(len(tips)) {
			same = false
			return nil
		}
		for ref, tip := range tips {
			data, found, err := index.Get(revision.EntityRoot, ref)
			if err != nil {
				return err
			}
			if !found {
				same = false
				return nil
			}
			var entity sourceview.Entity
			if err := json.Unmarshal(data, &entity); err != nil {
				return err
			}
			if entity.Tip != tip {
				same = false
				return nil
			}
		}
		return nil
	})
	return same, err
}

func (s *Store) validateAppliedImports(ctx context.Context, imported *importedSource) error {
	for id, operation := range imported.operations {
		if operation.State != gitobj.SourceOperationApplied {
			continue
		}
		revision := imported.revisions[operation.Token]
		if _, legacy := imported.legacyProofs[id]; !legacy && operation.RetentionRoot != revision.RetentionRoot {
			return fmt.Errorf("applied receipt %s token %s: pinned aggregate differs from committed revision", id, operation.Token)
		}
		if err := sourceProjection(revision); err != nil {
			return fmt.Errorf("applied receipt %s token %s: %w", id, operation.Token, err)
		}
		entities := make([]sourceview.Entity, 0)
		err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			index := sourcegraph.New(objects)
			for _, root := range []model.SHA{revision.EntityRoot, revision.ObjectRoot, revision.DependencyRoot, revision.NameRoot, revision.LFSRoot} {
				if err := index.Validate(root); err != nil {
					return err
				}
			}
			if err := sourcegraph.NewRetention(objects).Validate(sourcegraph.RetentionRoot{Index: revision.RetentionIndex, Commit: revision.RetentionRoot}); err != nil {
				return err
			}
			return index.Prefix(revision.EntityRoot, "", func(entry sourcegraph.Entry) error {
				var entity sourceview.Entity
				if err := json.Unmarshal(entry.Value, &entity); err != nil {
					return err
				}
				entities = append(entities, entity)
				return nil
			})
		})
		if err != nil {
			return fmt.Errorf("applied receipt %s token %s: %w", id, operation.Token, err)
		}
		tips := make([]model.SHA, 0, len(entities))
		lfs := make(map[string]int64)
		for _, entity := range entities {
			snapshot, err := s.LoadAt(ctx, entity.Tip)
			if err != nil {
				return fmt.Errorf("applied receipt %s token %s entity %s: %w", id, operation.Token, entity.Ref, err)
			}
			if revision.RenderPolicy != sourceview.RenderPolicy {
				return fmt.Errorf("applied receipt %s token %s: unknown renderer %s", id, operation.Token, revision.RenderPolicy)
			}
			rendered := sourceview.CodecOf(entity.Kind).Render(snapshot)
			if int64(len(rendered)) != entity.Size || sha256.Sum256(rendered) != entity.Hash {
				return fmt.Errorf("applied receipt %s token %s entity %s: rendered bytes differ", id, operation.Token, entity.Ref)
			}
			tips = append(tips, entity.Tip)
			if entity.Deleted {
				continue
			}
			for _, attachment := range entity.Attachments {
				if size, found := lfs[attachment.OID]; found && size != attachment.Size {
					return fmt.Errorf("applied receipt %s token %s: LFS size conflict", id, operation.Token)
				}
				lfs[attachment.OID] = attachment.Size
				info, err := os.Stat(s.LFS().Path(attachment.OID))
				if err != nil || info.Size() != attachment.Size {
					return fmt.Errorf("applied receipt %s token %s LFS %s: %w", id, operation.Token, attachment.OID, errors.Join(err, sourceindex.ErrProjectionUnavailable))
				}
			}
		}
		if proof, legacy := imported.legacyProofs[id]; legacy {
			slices.Sort(tips)
			tips = slices.Compact(tips)
			if !slices.Equal(tips, proof.RetainedTips) {
				return fmt.Errorf("applied receipt %s token %s: raw content pin does not cover its complete committed view", id, operation.Token)
			}
			retained := make([]gitobj.SourceOperationLFS, 0, len(lfs))
			for oid, size := range lfs {
				retained = append(retained, gitobj.SourceOperationLFS{OID: oid, Size: size})
			}
			slices.SortFunc(retained, func(a, b gitobj.SourceOperationLFS) int { return strings.Compare(a.OID, b.OID) })
			if !slices.Equal(retained, proof.RetainedLFS) {
				return fmt.Errorf("applied receipt %s token %s: LFS pin does not cover its complete committed view", id, operation.Token)
			}
		}
	}
	return nil
}
