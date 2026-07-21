package fusefs

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/contentstream"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// GitDriver exposes cc-notes' immutable Git source without lifecycle or
// catalog access.
type GitDriver struct {
	authority           causal.SourceAuthorityID
	authorityGeneration causal.Generation
	declarationDigest   [sha256.Size]byte
	repoRoot            string

	targetMu   sync.Mutex
	targetSets map[sourcedriver.TargetSetID]*declaredTargetSet

	cacheMu        sync.Mutex
	snapshots      map[sourcedriver.RevisionToken]*cachedAuthoritySnapshot
	snapshotOrder  []sourcedriver.RevisionToken
	deltas         map[authorityDeltaKey]*cachedAuthorityDelta
	deltaOrder     []authorityDeltaKey
	snapshotBytes  int64
	deltaBytes     int64
	snapshotBuilds atomic.Uint64
	continuations  atomic.Uint64
}

const (
	maxGitDriverSnapshotEntries = 4
	maxGitDriverDeltaEntries    = 4
	maxGitDriverSnapshotBytes   = 48 << 20
	maxGitDriverDeltaBytes      = 16 << 20
)

type cachedAuthoritySnapshot struct {
	objects []authorityObject
	byID    map[string]authorityObject
	weight  int64
}

type authorityDeltaKey struct {
	from sourcedriver.RevisionToken
	to   sourcedriver.RevisionToken
}

type authorityDeltaValue struct {
	id     sourcedriver.LogicalID
	object *authorityObject
}

type cachedAuthorityDelta struct {
	values []authorityDeltaValue
	weight int64
}

type pageBoundary struct {
	start int
	end   int
}

type declaredTargetSet struct {
	state   sourcedriver.TargetSetState
	targets []sourcedriver.TargetDeclaration
	pages   map[uint32]declaredTargetPage
}

type declaredTargetPage struct {
	digest [sha256.Size]byte
	before sourcedriver.TargetSetState
	state  sourcedriver.TargetSetState
}

// NewGitDriver binds one source authority to its immutable Git repository.
// FuseKit owns and supplies the exact generation-fenced target declaration on
// every snapshot or delta request.
func NewGitDriver(
	authority causal.SourceAuthorityID,
	authorityGeneration causal.Generation,
	declarationDigest [sha256.Size]byte,
	repoRoot string,
) (*GitDriver, error) {
	if err := causal.ValidateSourceAuthorityID(authority); err != nil {
		return nil, fmt.Errorf("cc-notes source: authority: %w", err)
	}
	if authorityGeneration == 0 || declarationDigest == ([sha256.Size]byte{}) ||
		!exactAbsolutePath(repoRoot) || strings.IndexFunc(repoRoot, unicode.IsControl) >= 0 {
		return nil, errors.New("cc-notes source: repository root is not an exact representable path")
	}
	return &GitDriver{
		authority: authority, authorityGeneration: authorityGeneration,
		declarationDigest: declarationDigest, repoRoot: repoRoot,
		targetSets: make(map[sourcedriver.TargetSetID]*declaredTargetSet),
		snapshots:  make(map[sourcedriver.RevisionToken]*cachedAuthoritySnapshot),
		deltas:     make(map[authorityDeltaKey]*cachedAuthorityDelta),
	}, nil
}

// InspectTargetSet returns one exact in-process declaration state.
func (d *GitDriver) InspectTargetSet(
	_ context.Context,
	authority causal.SourceAuthorityID,
	ref sourcedriver.TargetSetRef,
) (sourcedriver.TargetSetState, error) {
	if err := d.validateTargetSetRef(authority, ref); err != nil {
		return sourcedriver.TargetSetState{}, err
	}
	d.targetMu.Lock()
	defer d.targetMu.Unlock()
	declared := d.targetSets[ref.ID]
	if declared == nil {
		return sourcedriver.TargetSetState{}, sourcedriver.ErrNotFound
	}
	if declared.state.Ref != ref {
		return sourcedriver.TargetSetState{}, sourcedriver.ErrConflict
	}
	return declared.state, nil
}

// DeclareTargetSet validates and records one immutable declaration page.
func (d *GitDriver) DeclareTargetSet(
	_ context.Context,
	authority causal.SourceAuthorityID,
	page sourcedriver.TargetSetPage,
) (sourcedriver.TargetSetState, error) {
	if err := d.validateTargetSetRef(authority, page.Ref); err != nil {
		return sourcedriver.TargetSetState{}, err
	}
	if err := sourcedriver.ValidateTargetSetPage(authority, page); err != nil {
		return sourcedriver.TargetSetState{}, err
	}
	d.targetMu.Lock()
	defer d.targetMu.Unlock()
	declared := d.targetSets[page.Ref.ID]
	if declared == nil {
		state, err := sourcedriver.NewTargetSetState(authority, page.Ref)
		if err != nil {
			return sourcedriver.TargetSetState{}, err
		}
		declared = &declaredTargetSet{state: state, pages: make(map[uint32]declaredTargetPage)}
		d.targetSets[page.Ref.ID] = declared
	} else if declared.state.Ref != page.Ref {
		return sourcedriver.TargetSetState{}, sourcedriver.ErrConflict
	}
	if prior, found := declared.pages[page.Sequence]; found {
		replayed, err := sourcedriver.ApplyTargetSetPage(prior.before, page)
		if err != nil {
			return sourcedriver.TargetSetState{}, err
		}
		if prior.digest != page.PageDigest || replayed != prior.state {
			return sourcedriver.TargetSetState{}, sourcedriver.ErrIntegrity
		}
		return prior.state, nil
	}
	before := declared.state
	next, err := sourcedriver.ApplyTargetSetPage(declared.state, page)
	if err != nil {
		return sourcedriver.TargetSetState{}, err
	}
	declared.targets = append(declared.targets, page.Targets...)
	declared.state = next
	declared.pages[page.Sequence] = declaredTargetPage{digest: page.PageDigest, before: before, state: next}
	return next, nil
}

// Refresh seals the current entity-ref manifest as one immutable revision.
func (d *GitDriver) Refresh(ctx context.Context, authority causal.SourceAuthorityID) (sourcedriver.Head, error) {
	_, index, err := d.open(ctx, authority)
	if err != nil {
		return sourcedriver.Head{}, err
	}
	revision, err := index.Refresh(ctx)
	if err != nil {
		return sourcedriver.Head{}, err
	}
	head := sourcedriver.Head{Revision: sourcedriver.RevisionToken(revision)}
	if err := sourcedriver.ValidateHead(head); err != nil {
		return sourcedriver.Head{}, err
	}
	return head, nil
}

// Snapshot returns one bounded logical-ID-ordered page at an immutable Git
// source revision.
func (d *GitDriver) Snapshot(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	request sourcedriver.SnapshotRequest,
) (sourcedriver.SnapshotPage, error) {
	if err := sourcedriver.ValidateSnapshotRequest(request); err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	tenants, err := d.requestTargets(authority, request.TargetSet)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	source, index, err := d.open(ctx, authority)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	snapshot, err := d.loadSnapshot(ctx, source, index, request.Revision)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	start, err := d.projectionStart(request, snapshot, tenants)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	page, err := snapshotPage(request, snapshot, tenants, start)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	if err := sourcedriver.ValidateSnapshotPage(request, page); err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	return page, nil
}

// ChangesSince returns one bounded logical delta page between exact ancestor
// revisions.
func (d *GitDriver) ChangesSince(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	request sourcedriver.ChangesRequest,
) (sourcedriver.ChangePage, error) {
	if err := sourcedriver.ValidateChangesRequest(request); err != nil {
		return sourcedriver.ChangePage{}, err
	}
	tenants, err := d.requestTargets(authority, request.TargetSet)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	source, index, err := d.open(ctx, authority)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	from, err := sourceSHA(request.From)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	to, err := sourceSHA(request.To)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	if _, err := index.ChangesSince(ctx, from, to); err != nil {
		if errors.Is(err, sourceindex.ErrNotAncestor) {
			return sourcedriver.ChangePage{}, &sourcedriver.SnapshotRequiredError{From: request.From, Head: request.To}
		}
		return sourcedriver.ChangePage{}, err
	}
	delta, err := d.loadDelta(ctx, source, index, request.From, request.To)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	start, err := d.changeStart(request, delta, tenants)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	page, err := changePage(request, delta, tenants, start)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	if err := sourcedriver.ValidateChangePage(request, page); err != nil {
		return sourcedriver.ChangePage{}, err
	}
	return page, nil
}

// OpenContent resolves and verifies a durable revision-plus-logical-ID
// reference. No path crosses the driver boundary.
func (d *GitDriver) OpenContent(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	ref sourcedriver.ContentRef,
) (contentstream.Source, error) {
	if err := sourcedriver.ValidateContentRef(ref); err != nil {
		return nil, err
	}
	tenant, err := d.target(authority, ref.Tenant, ref.Generation)
	if err != nil {
		return nil, err
	}
	source, index, err := d.open(ctx, authority)
	if err != nil {
		return nil, err
	}
	if ref.Tenant != tenant.ID || ref.Generation != causal.Generation(tenant.Generation) {
		return nil, sourcedriver.ErrNotFound
	}
	snapshot, err := d.loadSnapshot(ctx, source, index, ref.Revision)
	if err != nil {
		return nil, err
	}
	object, found := snapshot.byID[string(ref.Object)]
	if !found || object.kind != catalog.KindFile || object.size != ref.Size || object.hash != ref.Hash {
		return nil, sourcedriver.ErrNotFound
	}
	if object.attachment == "" {
		return newVerifiedSource(io.NopCloser(bytes.NewReader(object.content)), ref.Size, ref.Hash), nil
	}
	lfs, err := source.LFS(ctx)
	if err != nil {
		return nil, err
	}
	file, err := lfs.Open(object.attachment)
	if err != nil {
		return nil, fmt.Errorf("cc-notes source: open attachment %s: %w", object.attachment, err)
	}
	return newVerifiedSource(file, ref.Size, ref.Hash), nil
}

func (d *GitDriver) open(
	ctx context.Context,
	authority causal.SourceAuthorityID,
) (*store.Store, sourceindex.Index, error) {
	if authority != d.authority {
		return nil, sourceindex.Index{}, sourcedriver.ErrNotFound
	}
	source, err := store.OpenContext(ctx, d.repoRoot)
	if err != nil {
		return nil, sourceindex.Index{}, fmt.Errorf("cc-notes source: open repository: %w", err)
	}
	return source, sourceindex.Index{Repo: source.Repo, Git: source.Git}, nil
}

func (d *GitDriver) requestTargets(
	authority causal.SourceAuthorityID,
	ref sourcedriver.TargetSetRef,
) ([]Tenant, error) {
	if err := d.validateTargetSetRef(authority, ref); err != nil {
		return nil, err
	}
	d.targetMu.Lock()
	declared := d.targetSets[ref.ID]
	if declared == nil {
		d.targetMu.Unlock()
		return nil, sourcedriver.ErrNotFound
	}
	if declared.state.Ref != ref || !declared.state.Complete || uint64(len(declared.targets)) != ref.TargetCount {
		d.targetMu.Unlock()
		return nil, sourcedriver.ErrConflict
	}
	targets := slices.Clone(declared.targets)
	d.targetMu.Unlock()
	result := make([]Tenant, len(targets))
	for index, target := range targets {
		result[index] = Tenant{
			ID: target.Tenant, Generation: catalog.Generation(target.Generation),
			Authority: d.authority, RepoRoot: d.repoRoot,
		}
	}
	return result, nil
}

func (d *GitDriver) targetInSet(
	authority causal.SourceAuthorityID,
	ref sourcedriver.TargetSetRef,
	tenantID catalog.TenantID,
	generation causal.Generation,
) (Tenant, error) {
	targets, err := d.requestTargets(authority, ref)
	if err != nil {
		return Tenant{}, err
	}
	for _, tenant := range targets {
		if tenant.ID == tenantID && causal.Generation(tenant.Generation) == generation {
			return tenant, nil
		}
	}
	return Tenant{}, sourcedriver.ErrNotFound
}

func (d *GitDriver) target(
	authority causal.SourceAuthorityID,
	tenantID catalog.TenantID,
	generation causal.Generation,
) (Tenant, error) {
	if authority != d.authority {
		return Tenant{}, sourcedriver.ErrNotFound
	}
	if _, err := catalog.NewTenantID(string(tenantID)); err != nil || generation == 0 {
		return Tenant{}, fmt.Errorf("%w: source target is invalid", sourcedriver.ErrInvalidValue)
	}
	return Tenant{
		ID: tenantID, Generation: catalog.Generation(generation),
		Authority: d.authority, RepoRoot: d.repoRoot,
	}, nil
}

func (d *GitDriver) validateDeclaration(generation causal.Generation, digest [sha256.Size]byte) error {
	if generation != d.authorityGeneration || digest != d.declarationDigest {
		return fmt.Errorf("%w: source driver declaration differs", sourcedriver.ErrConflict)
	}
	return nil
}

func (d *GitDriver) validateTargetSetRef(authority causal.SourceAuthorityID, ref sourcedriver.TargetSetRef) error {
	if authority != d.authority {
		return sourcedriver.ErrNotFound
	}
	if err := sourcedriver.ValidateTargetSetRef(authority, ref); err != nil {
		return err
	}
	return d.validateDeclaration(ref.AuthorityGeneration, ref.DeclarationDigest)
}

func (d *GitDriver) loadSnapshot(
	ctx context.Context,
	source *store.Store,
	index sourceindex.Index,
	revision sourcedriver.RevisionToken,
) (*cachedAuthoritySnapshot, error) {
	if _, err := sourceSHA(revision); err != nil {
		return nil, err
	}
	d.cacheMu.Lock()
	if cached := d.snapshots[revision]; cached != nil {
		d.cacheMu.Unlock()
		return cached, nil
	}
	d.cacheMu.Unlock()

	sha, _ := sourceSHA(revision)
	manifest, err := index.Snapshot(ctx, sha)
	if err != nil {
		return nil, err
	}
	d.snapshotBuilds.Add(1)
	snapshot, err := BuildAuthoritySnapshotAt(ctx, source, manifest)
	if err != nil {
		return nil, err
	}
	objects := slices.Clone(snapshot.objects)
	slices.SortFunc(objects, func(a, b authorityObject) int { return strings.Compare(a.key, b.key) })
	byID := make(map[string]authorityObject, len(objects))
	weight := int64(0)
	for _, object := range objects {
		byID[object.key] = object
		weight = saturatingAdd(weight, authorityObjectWeight(object))
	}
	cached := &cachedAuthoritySnapshot{objects: objects, byID: byID, weight: weight}
	return d.cacheSnapshot(revision, cached), nil
}

func (d *GitDriver) loadDelta(
	ctx context.Context,
	source *store.Store,
	index sourceindex.Index,
	from, to sourcedriver.RevisionToken,
) (*cachedAuthorityDelta, error) {
	key := authorityDeltaKey{from: from, to: to}
	d.cacheMu.Lock()
	if cached := d.deltas[key]; cached != nil {
		d.cacheMu.Unlock()
		return cached, nil
	}
	d.cacheMu.Unlock()
	before, err := d.loadSnapshot(ctx, source, index, from)
	if err != nil {
		return nil, err
	}
	after, err := d.loadSnapshot(ctx, source, index, to)
	if err != nil {
		return nil, err
	}
	delta := diffCachedSnapshots(before, after)
	values := make([]authorityDeltaValue, 0, delta.Len())
	weight := int64(0)
	for _, object := range delta.objects {
		value := object
		value.content = nil
		values = append(values, authorityDeltaValue{id: sourcedriver.LogicalID(value.key), object: &value})
		weight = saturatingAdd(weight, authorityObjectWeight(value))
	}
	for _, id := range delta.deletes {
		values = append(values, authorityDeltaValue{id: id})
		weight = saturatingAdd(weight, int64(len(id))+32)
	}
	slices.SortFunc(values, func(a, b authorityDeltaValue) int { return stringCompare(a.id, b.id) })
	cached := &cachedAuthorityDelta{values: values, weight: weight}
	return d.cacheDelta(key, cached), nil
}

func diffCachedSnapshots(previous, next *cachedAuthoritySnapshot) AuthorityDelta {
	result := AuthorityDelta{}
	for _, object := range next.objects {
		if old, found := previous.byID[object.key]; !found || !sameAuthorityObject(old, object) {
			result.objects = append(result.objects, object)
		}
	}
	for key := range previous.byID {
		if _, found := next.byID[key]; !found {
			result.deletes = append(result.deletes, sourcedriver.LogicalID(key))
		}
	}
	return result
}

func (d *GitDriver) cacheSnapshot(
	revision sourcedriver.RevisionToken,
	candidate *cachedAuthoritySnapshot,
) *cachedAuthoritySnapshot {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	if cached := d.snapshots[revision]; cached != nil {
		return cached
	}
	if candidate.weight > maxGitDriverSnapshotBytes {
		return candidate
	}
	for len(d.snapshotOrder) >= maxGitDriverSnapshotEntries ||
		d.snapshotBytes+candidate.weight > maxGitDriverSnapshotBytes {
		oldest := d.snapshotOrder[0]
		d.snapshotOrder = d.snapshotOrder[1:]
		d.snapshotBytes -= d.snapshots[oldest].weight
		delete(d.snapshots, oldest)
	}
	d.snapshots[revision] = candidate
	d.snapshotOrder = append(d.snapshotOrder, revision)
	d.snapshotBytes += candidate.weight
	return candidate
}

func (d *GitDriver) cacheDelta(key authorityDeltaKey, candidate *cachedAuthorityDelta) *cachedAuthorityDelta {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	if cached := d.deltas[key]; cached != nil {
		return cached
	}
	if candidate.weight > maxGitDriverDeltaBytes {
		return candidate
	}
	for len(d.deltaOrder) >= maxGitDriverDeltaEntries || d.deltaBytes+candidate.weight > maxGitDriverDeltaBytes {
		oldest := d.deltaOrder[0]
		d.deltaOrder = d.deltaOrder[1:]
		d.deltaBytes -= d.deltas[oldest].weight
		delete(d.deltas, oldest)
	}
	d.deltas[key] = candidate
	d.deltaOrder = append(d.deltaOrder, key)
	d.deltaBytes += candidate.weight
	return candidate
}

func authorityObjectWeight(object authorityObject) int64 {
	weight := int64(256)
	for _, value := range []string{
		object.key, object.parent, object.name, object.attachment, object.linkTarget,
	} {
		weight = saturatingAdd(weight, int64(len(value)))
	}
	return saturatingAdd(weight, int64(len(object.content)))
}

func saturatingAdd(left, right int64) int64 {
	if right < 0 || left > maxGitDriverSnapshotBytes-right {
		return maxGitDriverSnapshotBytes + 1
	}
	return left + right
}

func (o authorityObject) projection(tenant Tenant, revision sourcedriver.RevisionToken) sourcedriver.Projection {
	result := sourcedriver.Projection{
		Tenant: tenant.ID, Generation: causal.Generation(tenant.Generation),
		ID: sourcedriver.LogicalID(o.key), Parent: sourcedriver.LogicalID(o.parent),
		Name: o.name, Kind: o.kind, Mode: o.mode, LinkTarget: o.linkTarget,
		Visibility: catalog.Visibility{Mount: true}, Size: o.size, Hash: o.hash,
	}
	if o.kind == catalog.KindFile {
		result.Content = &sourcedriver.ContentRef{
			Revision: revision, Tenant: result.Tenant, Generation: result.Generation,
			Object: result.ID, Size: result.Size, Hash: result.Hash,
		}
	}
	return result
}

func compareProjection(a, b sourcedriver.Projection) int {
	if order := cmp.Compare(a.Tenant, b.Tenant); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Generation, b.Generation); order != 0 {
		return order
	}
	return stringCompare(a.ID, b.ID)
}

func compareChange(a, b sourcedriver.Change) int {
	if order := cmp.Compare(a.Tenant, b.Tenant); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Generation, b.Generation); order != 0 {
		return order
	}
	if order := cmp.Compare(a.Sequence, b.Sequence); order != 0 {
		return order
	}
	return stringCompare(a.ID, b.ID)
}

func snapshotPage(
	request sourcedriver.SnapshotRequest,
	snapshot *cachedAuthoritySnapshot,
	tenants []Tenant,
	start int,
) (sourcedriver.SnapshotPage, error) {
	objects, end, digest, err := fitSnapshotPage(request.Revision, snapshot, tenants, start, request.Limit)
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	page := sourcedriver.SnapshotPage{Revision: request.Revision, Objects: objects, Digest: digest}
	total, err := tupleCount(len(snapshot.objects), len(tenants))
	if err != nil {
		return sourcedriver.SnapshotPage{}, err
	}
	if end < total {
		last := objects[len(objects)-1]
		next, err := sourcedriver.NewPageCursor(
			request.TargetSet, sourcedriver.PageSnapshot,
			"", request.Revision, nextPage(request.Cursor), request.Limit,
			sourcedriver.PagePosition{Tenant: last.Tenant, Generation: last.Generation, ID: last.ID},
			pageContinuation(start, end), digest,
		)
		if err != nil {
			return sourcedriver.SnapshotPage{}, err
		}
		page.Next = &next
	}
	return page, nil
}

func changePage(
	request sourcedriver.ChangesRequest,
	delta *cachedAuthorityDelta,
	tenants []Tenant,
	start int,
) (sourcedriver.ChangePage, error) {
	changes, end, digest, err := fitChangePage(request.From, request.To, delta, tenants, start, request.Limit)
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	page := sourcedriver.ChangePage{From: request.From, To: request.To, Changes: changes, Digest: digest}
	total, err := tupleCount(len(delta.values), len(tenants))
	if err != nil {
		return sourcedriver.ChangePage{}, err
	}
	if end < total {
		last := changes[len(changes)-1]
		next, err := sourcedriver.NewPageCursor(
			request.TargetSet, sourcedriver.PageChanges,
			request.From, request.To, nextPage(request.Cursor), request.Limit,
			sourcedriver.PagePosition{
				Tenant: last.Tenant, Generation: last.Generation, Sequence: last.Sequence, ID: last.ID,
			},
			pageContinuation(start, end), digest,
		)
		if err != nil {
			return sourcedriver.ChangePage{}, err
		}
		page.Next = &next
	}
	return page, nil
}

func fitSnapshotPage(
	revision sourcedriver.RevisionToken,
	snapshot *cachedAuthoritySnapshot,
	tenants []Tenant,
	start, limit int,
) ([]sourcedriver.Projection, int, [sha256.Size]byte, error) {
	total, err := tupleCount(len(snapshot.objects), len(tenants))
	if err != nil {
		return nil, 0, [sha256.Size]byte{}, err
	}
	end := min(start+limit, total)
	for end > start {
		objects, err := snapshotValues(snapshot, tenants, revision, start, end)
		if err != nil {
			return nil, 0, [sha256.Size]byte{}, err
		}
		digest, digestErr := sourcedriver.SnapshotPageDigest(revision, objects)
		if digestErr == nil {
			return objects, end, digest, nil
		}
		end--
	}
	if start < total {
		return nil, 0, [sha256.Size]byte{}, fmt.Errorf("%w: source projection exceeds page budget", sourcedriver.ErrInvalidValue)
	}
	digest, err := sourcedriver.SnapshotPageDigest(revision, nil)
	return []sourcedriver.Projection{}, start, digest, err
}

func fitChangePage(
	from, to sourcedriver.RevisionToken,
	delta *cachedAuthorityDelta,
	tenants []Tenant,
	start, limit int,
) ([]sourcedriver.Change, int, [sha256.Size]byte, error) {
	total, err := tupleCount(len(delta.values), len(tenants))
	if err != nil {
		return nil, 0, [sha256.Size]byte{}, err
	}
	end := min(start+limit, total)
	for end > start {
		changes, err := changeValues(delta, tenants, to, start, end)
		if err != nil {
			return nil, 0, [sha256.Size]byte{}, err
		}
		digest, digestErr := sourcedriver.ChangePageDigest(from, to, changes)
		if digestErr == nil {
			return changes, end, digest, nil
		}
		end--
	}
	if start < total {
		return nil, 0, [sha256.Size]byte{}, fmt.Errorf("%w: source change exceeds page budget", sourcedriver.ErrInvalidValue)
	}
	digest, err := sourcedriver.ChangePageDigest(from, to, nil)
	return []sourcedriver.Change{}, start, digest, err
}

func snapshotValues(
	snapshot *cachedAuthoritySnapshot,
	tenants []Tenant,
	revision sourcedriver.RevisionToken,
	start, end int,
) ([]sourcedriver.Projection, error) {
	if start < 0 || end < start || len(snapshot.objects) == 0 && end != 0 {
		return nil, fmt.Errorf("%w: snapshot page range is invalid", sourcedriver.ErrInvalidValue)
	}
	result := make([]sourcedriver.Projection, 0, end-start)
	for index := start; index < end; index++ {
		tenant := tenants[index/len(snapshot.objects)]
		object := snapshot.objects[index%len(snapshot.objects)]
		projection := object.projection(tenant, revision)
		if err := sourcedriver.ValidateProjection(projection); err != nil {
			return nil, fmt.Errorf("cc-notes source: projection %s: %w", object.key, err)
		}
		result = append(result, projection)
	}
	return result, nil
}

func changeValues(
	delta *cachedAuthorityDelta,
	tenants []Tenant,
	revision sourcedriver.RevisionToken,
	start, end int,
) ([]sourcedriver.Change, error) {
	if start < 0 || end < start || len(delta.values) == 0 && end != 0 {
		return nil, fmt.Errorf("%w: change page range is invalid", sourcedriver.ErrInvalidValue)
	}
	result := make([]sourcedriver.Change, 0, end-start)
	for index := start; index < end; index++ {
		tenant := tenants[index/len(delta.values)]
		valueIndex := index % len(delta.values)
		value := delta.values[valueIndex]
		change := sourcedriver.Change{
			Kind: sourcedriver.ChangeDelete, Tenant: tenant.ID,
			Generation: causal.Generation(tenant.Generation),
			// #nosec G115 -- valueIndex is a nonnegative slice index.
			Sequence: uint64(valueIndex + 1), ID: value.id,
		}
		if value.object != nil {
			projection := value.object.projection(tenant, revision)
			change.Kind = sourcedriver.ChangeUpsert
			change.Object = &projection
		}
		if err := sourcedriver.ValidateChange(change); err != nil {
			return nil, err
		}
		result = append(result, change)
	}
	return result, nil
}

func tupleCount(values, tenants int) (int, error) {
	if values == 0 || tenants == 0 {
		return 0, nil
	}
	maxInt := int(^uint(0) >> 1)
	if values > maxInt/tenants {
		return 0, fmt.Errorf("%w: projected source cardinality overflows", sourcedriver.ErrInvalidValue)
	}
	return values * tenants, nil
}

func (d *GitDriver) projectionStart(
	request sourcedriver.SnapshotRequest,
	snapshot *cachedAuthoritySnapshot,
	tenants []Tenant,
) (int, error) {
	if request.Cursor == nil {
		return 0, nil
	}
	total, err := tupleCount(len(snapshot.objects), len(tenants))
	if err != nil {
		return 0, err
	}
	boundary, err := cursorBoundary(request.Cursor, total, request.Limit)
	if err != nil {
		return 0, fmt.Errorf("cc-notes source: snapshot cursor: %w", err)
	}
	objects, end, digest, err := fitSnapshotPage(
		request.Revision, snapshot, tenants, boundary.start, request.Limit,
	)
	if err != nil {
		return 0, err
	}
	if end != boundary.end || digest != request.Cursor.PreviousDigest || len(objects) == 0 {
		return 0, errors.New("cc-notes source: snapshot cursor does not identify the exact preceding page")
	}
	last := objects[len(objects)-1]
	expected, err := sourcedriver.NewPageCursor(
		request.TargetSet, sourcedriver.PageSnapshot,
		"", request.Revision, request.Cursor.Page, request.Limit,
		sourcedriver.PagePosition{Tenant: last.Tenant, Generation: last.Generation, ID: last.ID},
		pageContinuation(boundary.start, boundary.end), digest,
	)
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(expected, *request.Cursor) {
		return 0, errors.New("cc-notes source: snapshot cursor does not match exact page history")
	}
	d.continuations.Add(1)
	return boundary.end, nil
}

func (d *GitDriver) changeStart(
	request sourcedriver.ChangesRequest,
	delta *cachedAuthorityDelta,
	tenants []Tenant,
) (int, error) {
	if request.Cursor == nil {
		return 0, nil
	}
	total, err := tupleCount(len(delta.values), len(tenants))
	if err != nil {
		return 0, err
	}
	boundary, err := cursorBoundary(request.Cursor, total, request.Limit)
	if err != nil {
		return 0, fmt.Errorf("cc-notes source: change cursor: %w", err)
	}
	changes, end, digest, err := fitChangePage(
		request.From, request.To, delta, tenants, boundary.start, request.Limit,
	)
	if err != nil {
		return 0, err
	}
	if end != boundary.end || digest != request.Cursor.PreviousDigest || len(changes) == 0 {
		return 0, errors.New("cc-notes source: change cursor does not identify the exact preceding page")
	}
	last := changes[len(changes)-1]
	expected, err := sourcedriver.NewPageCursor(
		request.TargetSet, sourcedriver.PageChanges,
		request.From, request.To, request.Cursor.Page, request.Limit,
		sourcedriver.PagePosition{
			Tenant: last.Tenant, Generation: last.Generation, Sequence: last.Sequence, ID: last.ID,
		},
		pageContinuation(boundary.start, boundary.end), digest,
	)
	if err != nil {
		return 0, err
	}
	if !reflect.DeepEqual(expected, *request.Cursor) {
		return 0, errors.New("cc-notes source: change cursor does not match exact page history")
	}
	d.continuations.Add(1)
	return boundary.end, nil
}

func cursorBoundary(cursor *sourcedriver.PageCursor, total, limit int) (pageBoundary, error) {
	if cursor == nil || len(cursor.Continuation) != 16 || cursor.Page == 0 || limit < 1 {
		return pageBoundary{}, errors.New("continuation is malformed")
	}
	startValue := binary.BigEndian.Uint64(cursor.Continuation[:8])
	endValue := binary.BigEndian.Uint64(cursor.Continuation[8:])
	maxInt := uint64(^uint(0) >> 1)
	if startValue > maxInt || endValue > maxInt {
		return pageBoundary{}, errors.New("continuation range overflows")
	}
	start, end := int(startValue), int(endValue)
	if start < 0 || start >= end || end > total || end-start > limit || end == total {
		return pageBoundary{}, errors.New("continuation range is invalid or terminal")
	}
	precedingPages := uint64(cursor.Page - 1)
	if startValue < precedingPages || startValue > precedingPages*uint64(limit) {
		return pageBoundary{}, errors.New("continuation range is inconsistent with page count")
	}
	return pageBoundary{start: start, end: end}, nil
}

func pageContinuation(start, end int) []byte {
	if start < 0 || end < 0 {
		return nil
	}
	result := make([]byte, 16)
	// #nosec G115 -- negative values are rejected above and every int fits uint64.
	binary.BigEndian.PutUint64(result[:8], uint64(start))
	// #nosec G115 -- negative values are rejected above and every int fits uint64.
	binary.BigEndian.PutUint64(result[8:], uint64(end))
	return result
}

func nextPage(cursor *sourcedriver.PageCursor) uint32 {
	if cursor == nil {
		return 1
	}
	return cursor.Page + 1
}

func stringCompare[T ~string](a, b T) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func sourceSHA(token sourcedriver.RevisionToken) (model.SHA, error) {
	if !plumbing.IsHash(string(token)) {
		return "", fmt.Errorf("%w: source revision is not a canonical Git object ID", sourcedriver.ErrInvalidValue)
	}
	return model.SHA(token), nil
}

type verifiedSource struct {
	reader   io.ReadCloser
	hash     hash.Hash
	expected catalog.ContentHash
	size     int64
	read     int64
	verified atomic.Bool

	readMu  sync.Mutex
	readErr error

	settleOnce sync.Once
	mu         sync.Mutex
	settling   bool
	done       chan struct{}
	settleErr  error
	waitErr    error
}

func newVerifiedSource(reader io.ReadCloser, size int64, expected catalog.ContentHash) *verifiedSource {
	return &verifiedSource{
		reader: reader, hash: sha256.New(), expected: expected, size: size, done: make(chan struct{}),
	}
}

func (s *verifiedSource) Read(buffer []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.readErr != nil {
		return 0, s.readErr
	}
	s.mu.Lock()
	settling, settleErr := s.settling, s.settleErr
	s.mu.Unlock()
	if settling {
		if settleErr != nil {
			return 0, settleErr
		}
		return 0, io.ErrClosedPipe
	}
	remaining := s.size - s.read
	if remaining < 0 {
		s.readErr = sourcedriver.ErrIntegrity
		return 0, s.readErr
	}
	bounded := buffer
	if remaining < int64(len(bounded)) {
		bounded = bounded[:remaining+1]
	}
	n, err := s.reader.Read(bounded)
	if n < 0 || n > len(bounded) || s.read+int64(n) > s.size {
		s.readErr = sourcedriver.ErrIntegrity
		return 0, s.readErr
	}
	if n > 0 {
		s.read += int64(n)
		_, _ = s.hash.Write(buffer[:n])
	}
	if errors.Is(err, io.EOF) {
		var digest catalog.ContentHash
		copy(digest[:], s.hash.Sum(nil))
		if s.read != s.size || digest != s.expected {
			s.readErr = sourcedriver.ErrIntegrity
			return n, s.readErr
		}
		s.verified.Store(true)
	}
	return n, err
}

func (s *verifiedSource) Settle(settleErr error) error {
	if settleErr == nil && !s.verified.Load() {
		settleErr = errors.New("cc-notes source: content settled before complete verification")
	}
	s.settleOnce.Do(func() {
		s.mu.Lock()
		s.settling = true
		s.settleErr = settleErr
		s.mu.Unlock()
		go s.cleanup(settleErr)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleErr
}

func (s *verifiedSource) Wait(ctx context.Context) error {
	select {
	case <-s.done:
	case <-ctx.Done():
		_ = s.Settle(ctx.Err())
		<-s.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

func (s *verifiedSource) cleanup(cause error) {
	closeErr := s.reader.Close()
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.mu.Lock()
	s.waitErr = errors.Join(cause, closeErr)
	s.mu.Unlock()
	close(s.done)
}

var (
	_ sourcedriver.Driver  = (*GitDriver)(nil)
	_ contentstream.Source = (*verifiedSource)(nil)
)
