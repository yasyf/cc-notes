package store

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// PruneGuardConfigs are the config lines AttachFile installs on a
// repository's first attachment, making `git lfs prune` verify remote
// presence before deleting. Both keys are required: verify-remote alone
// covers only objects reachable from git commits, and cc-notes attachments
// are referenced solely by refs/cc-notes/* — without verify-unreachable,
// prune deletes an un-synced attachment without ever consulting the remote
// (verified live: the object is unrecoverable). Callers print these verbatim
// the one time AttachFile reports installing them.
var PruneGuardConfigs = [2]string{
	"lfs.pruneverifyremotealways=true",
	"lfs.pruneverifyunreachablealways=true",
}

// AttachmentUse names one live reference to an LFS object: the entity kind
// and id referencing it, and the attachment name it is referenced under.
type AttachmentUse struct {
	Kind   model.Kind
	Entity model.EntityID
	Name   string
}

// ReferencedObject is one LFS object the repository's live entity state
// references, with every use of it — the detail sync needs to name entities
// in transfer errors.
type ReferencedObject struct {
	OID  string
	Size int64
	// Uses lists every live reference, sorted by kind, entity, then name.
	Uses []AttachmentUse
}

const (
	attachIndexSubdir = "cc-notes"
	attachIndexName   = "attachments-v1.json"
)

// refAttachments pairs one parsed entity ref with the attachments its live
// state references.
type refAttachments struct {
	ref      refs.Ref
	tip      model.SHA
	atts     []model.Attachment
	complete bool
}

type attachIndex struct {
	Generation string                      `json:"generation"`
	Refs       map[string]attachIndexEntry `json:"refs"`
}

type attachIndexEntry struct {
	Tip         model.SHA          `json:"tip"`
	Attachments []model.Attachment `json:"attachments,omitempty"`
}

// useKey dedupes attachment uses per oid when a checkpoint State and the
// folded snapshot both carry the same attachment.
type useKey struct {
	oid string
	use AttachmentUse
}

// LFS returns the records repository's local LFS content store, rooted at
// <records-common-dir>/lfs so linked worktrees and bound contexts share one
// store and the git-lfs CLI reads and writes the same objects. It performs no
// backend recheck of its own: callers reach it after a records operation that
// already passed CheckRecords.
func (s *Store) LFS() lfs.Store {
	return lfs.Store{Dir: filepath.Join(s.recordsCommonDir, "lfs")}
}

// AttachFile hashes path's content into the local LFS store and returns the
// attachment referencing it, named path's base name. The attachment is
// validated through the op codec before it is returned, so a value AttachFile
// hands back always survives the Create/Append path. Attaching is offline:
// content moves to the remote only at sync. guarded reports that this call
// installed PruneGuardConfig — set once per repository, on the first attach —
// so the caller can print the config line that one time.
func (s *Store) AttachFile(ctx context.Context, path string) (att model.Attachment, guarded bool, err error) {
	if err := ctx.Err(); err != nil {
		return model.Attachment{}, false, err
	}
	if err := s.CheckRecords(); err != nil {
		return model.Attachment{}, false, fmt.Errorf("attach %s: %w", path, err)
	}
	content := s.LFS()
	oid, size, err := content.PutFile(path)
	if err != nil {
		return model.Attachment{}, false, fmt.Errorf("attach %s: %w", path, err)
	}
	att = model.Attachment{Name: filepath.Base(path), OID: oid, Size: size}
	op := model.AddAttachment(att)
	if _, err := roundTrip(model.Pack{Lamport: 1, Ops: []model.Op{op}}); err != nil {
		return model.Attachment{}, false, fmt.Errorf("attach %s: %w", path, err)
	}
	guarded, err = s.ensurePruneGuard(ctx)
	if err != nil {
		return model.Attachment{}, false, fmt.Errorf("attach %s: %w", path, err)
	}
	return att, guarded, nil
}

// ensurePruneGuard installs each PruneGuardConfigs line in the records
// repository's local config unless that key is already set in any scope,
// reporting whether this call wrote any of them.
func (s *Store) ensurePruneGuard(ctx context.Context) (bool, error) {
	wrote := false
	for _, line := range PruneGuardConfigs {
		key, value, _ := strings.Cut(line, "=")
		current, err := s.RecordsGit.ConfigGet(ctx, key)
		if err != nil {
			return wrote, err
		}
		if current != "" {
			continue
		}
		if err := s.RecordsGit.ConfigSet(ctx, key, value); err != nil {
			return wrote, err
		}
		wrote = true
	}
	return wrote, nil
}

// ReferencedAttachments scans every local note, doc, log, and answer ref and returns
// the LFS objects their live state references: the folded snapshot's
// attachments plus the attachments of every checkpoint State in the chain —
// checkpoints are fold seeds, so content they reference must stay
// resolvable. Historical add_attachment ops covered by neither contribute
// nothing: removing an entity's last attachment removes its objects from the
// set. The attachment index answers every ref whose tip it recorded, so only
// refs that moved since the last scan read their chains; the fold cache
// accelerates the snapshot half of those. The result is sorted by oid with
// sorted uses, so transfer errors name entities deterministically.
func (s *Store) ReferencedAttachments(ctx context.Context) ([]ReferencedObject, error) {
	var entries []tipEntry
	for _, prefix := range []string{refs.Root(model.KindNote), refs.Root(model.KindDoc), refs.Root(model.KindLog), refs.Root(model.KindAnswer)} {
		children, err := s.children(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("referenced attachments: %w", err)
		}
		entries = append(entries, children...)
	}
	index := s.readAttachIndex()
	perRef := make([]refAttachments, len(entries))
	misses := 0
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i, e := range entries {
		if hit, ok := index.Refs[e.ref]; ok && hit.Tip == e.tip {
			parsed, err := refs.Parse(e.ref)
			if err != nil {
				return nil, fmt.Errorf("referenced attachments: %w", err)
			}
			perRef[i] = refAttachments{ref: parsed, tip: e.tip, atts: hit.Attachments, complete: true}
			continue
		}
		misses++
		g.Go(func() error {
			ra, err := s.refAttachments(gctx, e)
			if err != nil {
				return err
			}
			perRef[i] = ra
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("referenced attachments: %w", err)
	}
	if misses > 0 || len(index.Refs) != len(entries) {
		s.writeAttachIndex(perRef)
	}
	return mergeReferenced(perRef), nil
}

func (s *Store) readAttachIndex() attachIndex {
	//nolint:gosec // G304: the path is this store's own cache file, not external input.
	data, err := os.ReadFile(filepath.Join(s.recordsCommonDir, attachIndexSubdir, attachIndexName))
	if err != nil {
		return attachIndex{}
	}
	var index attachIndex
	if json.Unmarshal(data, &index) != nil || index.Generation != foldCacheGeneration {
		return attachIndex{}
	}
	return index
}

func (s *Store) writeAttachIndex(perRef []refAttachments) {
	index := attachIndex{Generation: foldCacheGeneration, Refs: make(map[string]attachIndexEntry, len(perRef))}
	for _, ra := range perRef {
		if ra.complete {
			index.Refs[refs.For(ra.ref.Kind, ra.ref.ID)] = attachIndexEntry{Tip: ra.tip, Attachments: ra.atts}
		}
	}
	data, err := json.Marshal(index)
	if err != nil {
		return
	}
	dir := filepath.Join(s.recordsCommonDir, attachIndexSubdir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	writeFileAtomic(dir, attachIndexName, data)
}

// refAttachments folds one entity ref and collects its live attachment set:
// the folded snapshot's attachments plus every checkpoint State's.
func (s *Store) refAttachments(ctx context.Context, e tipEntry) (refAttachments, error) {
	parsed, err := refs.Parse(e.ref)
	if err != nil {
		return refAttachments{}, err
	}
	chain, err := s.Repo.ReadChain(ctx, e.tip)
	if err != nil {
		return refAttachments{}, fmt.Errorf("read %s: %w", e.ref, err)
	}
	snap, ok := s.cache.get(e.tip)
	if !ok {
		snap, err = fold.Fold(chain)
		if err != nil {
			return refAttachments{}, fmt.Errorf("fold %s: %w", e.ref, err)
		}
		s.cache.put(e.tip, snap)
	}
	atts := slices.Clone(snap.Meta().Attachments)
	for _, c := range chain {
		for _, op := range c.Pack.Ops {
			if cp, ok := op.(model.Checkpoint); ok {
				atts = append(atts, cp.State.Meta().Attachments...)
			}
		}
	}
	return refAttachments{ref: parsed, tip: e.tip, atts: atts, complete: snap.Meta().SkippedOps == 0}, nil
}

// mergeReferenced folds per-ref attachment sets into the deduplicated,
// deterministically ordered object list.
func mergeReferenced(perRef []refAttachments) []ReferencedObject {
	byOID := map[string]*ReferencedObject{}
	seen := map[useKey]bool{}
	for _, ra := range perRef {
		for _, a := range ra.atts {
			obj := byOID[a.OID]
			if obj == nil {
				obj = &ReferencedObject{OID: a.OID, Size: a.Size}
				byOID[a.OID] = obj
			}
			key := useKey{oid: a.OID, use: AttachmentUse{Kind: ra.ref.Kind, Entity: ra.ref.ID, Name: a.Name}}
			if !seen[key] {
				seen[key] = true
				obj.Uses = append(obj.Uses, key.use)
			}
		}
	}
	out := make([]ReferencedObject, 0, len(byOID))
	for _, obj := range byOID {
		slices.SortFunc(obj.Uses, func(a, b AttachmentUse) int {
			if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
				return c
			}
			if c := cmp.Compare(a.Entity, b.Entity); c != 0 {
				return c
			}
			return cmp.Compare(a.Name, b.Name)
		})
		out = append(out, *obj)
	}
	slices.SortFunc(out, func(a, b ReferencedObject) int { return cmp.Compare(a.OID, b.OID) })
	return out
}
