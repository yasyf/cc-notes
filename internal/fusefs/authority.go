package fusefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/catalogservice"
	"github.com/yasyf/fusekit/convergence"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"
	"github.com/yasyf/fusekit/mountservice"
	"github.com/yasyf/fusekit/transportproto"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// AuthoritySnapshot is one complete, immutable Git-derived namespace.
type AuthoritySnapshot struct {
	objects []authorityObject
}

// AuthorityDelta is one predecessor-fenced set of authoritative upserts and
// deletes derived from two complete snapshots.
type AuthorityDelta struct {
	objects []authorityObject
	deletes []catalogproto.SourceDeleteRecord
}

type authorityObject struct {
	key         string
	parent      string
	name        string
	kind        catalogproto.ObjectKind
	mode        uint32
	content     []byte
	contentPath string
	size        uint64
	hash        string
	linkTarget  string
}

// BuildAuthoritySnapshot renders one repository without starting a watcher,
// bridge, spool, or filesystem process.
func BuildAuthoritySnapshot(ctx context.Context, source *store.Store) (AuthoritySnapshot, error) {
	if source == nil {
		return AuthoritySnapshot{}, errors.New("cc-notes authority: store is required")
	}
	projection := projectionBuilder{ctx: ctx, store: source}
	if err := projection.build(); err != nil {
		return AuthoritySnapshot{}, err
	}
	if err := validateAuthorityObjects(projection.objects); err != nil {
		return AuthoritySnapshot{}, err
	}
	return AuthoritySnapshot{objects: projection.objects}, nil
}

// Len returns the exact number of authoritative objects, excluding FuseKit's
// catalog-owned stable tenant root.
func (s AuthoritySnapshot) Len() int { return len(s.objects) }

// DiffAuthoritySnapshots returns the exact path-independent changes from
// previous to next.
func DiffAuthoritySnapshots(previous, next AuthoritySnapshot) AuthorityDelta {
	before := make(map[string]authorityObject, len(previous.objects))
	after := make(map[string]authorityObject, len(next.objects))
	for _, object := range previous.objects {
		before[object.key] = object
	}
	for _, object := range next.objects {
		after[object.key] = object
	}
	result := AuthorityDelta{}
	for _, object := range next.objects {
		if old, found := before[object.key]; !found || !sameAuthorityObject(old, object) {
			result.objects = append(result.objects, object)
		}
	}
	for key := range before {
		if _, found := after[key]; !found {
			result.deletes = append(result.deletes, catalogproto.SourceDeleteRecord{SourceKey: key})
		}
	}
	slices.SortFunc(result.deletes, func(a, z catalogproto.SourceDeleteRecord) int {
		return strings.Compare(a.SourceKey, z.SourceKey)
	})
	return result
}

// Len returns the number of upserts and deletes in the delta.
func (d AuthorityDelta) Len() int { return len(d.objects) + len(d.deletes) }

func sourceInput(
	tenant Tenant,
	revision uint64,
	values []authorityObject,
	deletes []catalogproto.SourceDeleteRecord,
) (catalogservice.SourceTenantInput, []io.Closer, error) {
	objectCount, err := catalogCount("objects", len(values))
	if err != nil {
		return catalogservice.SourceTenantInput{}, nil, err
	}
	deleteCount, err := catalogCount("deletes", len(deletes))
	if err != nil {
		return catalogservice.SourceTenantInput{}, nil, err
	}
	objects := make([]catalogservice.SourceObjectInput, 0, len(values))
	closers := make([]io.Closer, 0)
	for _, object := range values {
		record := catalogproto.SourceObjectRecord{
			SourceKey: object.key, ParentKey: object.parent, Name: object.name,
			Kind: object.kind, Mode: object.mode, LinkTarget: object.linkTarget,
			MountVisible: true,
		}
		var content io.Reader
		switch object.kind {
		case catalogproto.ObjectKindFile:
			record.ContentRevision = revision
			if object.contentPath == "" {
				hash := sha256.Sum256(object.content)
				record.Size = uint64(len(object.content))
				record.Hash = hex.EncodeToString(hash[:])
				content = bytes.NewReader(object.content)
			} else {
				file, err := os.Open(object.contentPath)
				if err != nil {
					closeAll(closers)
					return catalogservice.SourceTenantInput{}, nil, fmt.Errorf("cc-notes authority: open %s: %w", object.key, err)
				}
				closers = append(closers, file)
				record.Size, record.Hash, content = object.size, object.hash, file
			}
		case catalogproto.ObjectKindSymlink:
			record.ContentRevision = revision
		}
		objects = append(objects, catalogservice.SourceObjectInput{Record: record, Content: content})
	}
	return catalogservice.SourceTenantInput{
		Record: catalogproto.SourceTenantRecord{
			TenantID: catalogproto.TenantID(tenant.ID), Generation: uint64(tenant.Generation),
			RootKey: RootKeyForTenant(tenant.ID), ObjectCount: objectCount, DeleteCount: deleteCount,
		},
		Objects: objects, Deletes: deletes,
	}, closers, nil
}

func catalogCount(name string, value int) (uint32, error) {
	if value > math.MaxUint32 {
		return 0, fmt.Errorf("cc-notes authority: too many %s", name)
	}
	return uint32(value), nil //nolint:gosec // explicit uint32 protocol bound above
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

// AuthorityForTenant returns cc-notes' unique source authority for tenant.
func AuthorityForTenant(tenant catalog.TenantID) catalogproto.SourceAuthorityID {
	return catalogproto.SourceAuthorityID("cc-notes:" + string(tenant))
}

// RootKeyForTenant returns cc-notes' stable authority-owned catalog root key.
func RootKeyForTenant(tenant catalog.TenantID) string {
	return "root:" + string(tenant)
}

// Tenant is one cc-notes repository's immutable FuseKit identity.
type Tenant struct {
	ID         catalog.TenantID
	Generation catalog.Generation
	Authority  catalogproto.SourceAuthorityID
	Domain     catalogproto.DomainID
	RouteName  string
	RepoRoot   string
}

// Validate rejects identities that cannot be represented by the hard runtime.
func (t Tenant) Validate(plan holder.Plan) error {
	if _, err := catalog.NewTenantID(string(t.ID)); err != nil {
		return fmt.Errorf("cc-notes authority: tenant id: %w", err)
	}
	switch {
	case t.Generation == 0:
		return errors.New("cc-notes authority: generation is required")
	case t.Authority != AuthorityForTenant(t.ID):
		return fmt.Errorf("cc-notes authority: source authority %q does not match tenant %q", t.Authority, t.ID)
	case t.Domain == "":
		return errors.New("cc-notes authority: domain is required")
	case t.RouteName == "" || filepath.Base(t.RouteName) != t.RouteName || t.RouteName == "." || t.RouteName == "..":
		return fmt.Errorf("cc-notes authority: invalid route name %q", t.RouteName)
	case !exactAbsolutePath(t.RepoRoot):
		return fmt.Errorf("cc-notes authority: repository root %q is not an exact absolute path", t.RepoRoot)
	case plan.Paths().PresentationRoot == "":
		return errors.New("cc-notes authority: holder plan is required")
	default:
		return nil
	}
}

// NewHolderPlan binds cc-notes to a caller-supplied, already-registered signed
// application. Bundle identity and signing credentials remain consumer-owned.
func NewHolderPlan(application holder.SignedApplication, runtimeDirectory string) (holder.Plan, error) {
	return holder.NewPlan(holder.PlanSpec{Application: application, RuntimeDirectory: runtimeDirectory})
}

// RuntimeClient owns one exact persistent session shared by the mount and
// catalog protocols.
type RuntimeClient struct {
	wire    *wire.Client
	mount   *mountservice.Client
	catalog *catalogservice.Client
	plan    holder.Plan
}

// NewRuntimeClient connects to the fixed signed holder named by plan.
func NewRuntimeClient(ctx context.Context, plan holder.Plan) (*RuntimeClient, error) {
	if plan.Paths().Socket == "" {
		return nil, errors.New("cc-notes authority: holder plan is required")
	}
	session, err := wire.NewClient(ctx, wire.ClientConfig{
		Dial: wire.UnixDialer(plan.Paths().Socket), Build: transportproto.Build,
	})
	if err != nil {
		return nil, err
	}
	mountClient, err := mountservice.NewClientOn(session)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	catalogClient, err := catalogservice.NewClientOn(session)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return &RuntimeClient{wire: session, mount: mountClient, catalog: catalogClient, plan: plan}, nil
}

// Close settles the shared persistent session.
func (c *RuntimeClient) Close() error {
	if c == nil || c.wire == nil {
		return nil
	}
	return c.wire.Close()
}

// ProvisionTenant durably creates one exact repository tenant.
func (c *RuntimeClient) ProvisionTenant(ctx context.Context, tenant Tenant) error {
	if err := tenant.Validate(c.plan); err != nil {
		return err
	}
	_, err := c.mount.ProvisionTenant(ctx, tenant.ID, tenantDefinition(c.plan, tenant))
	return err
}

func tenantDefinition(plan holder.Plan, tenant Tenant) mountproto.TenantDefinition {
	return mountproto.TenantDefinition{
		PresentationRoot: filepath.Join(plan.Paths().PresentationRoot, tenant.RouteName),
		BackingRoot:      tenant.RepoRoot, ContentSourceID: string(tenant.Authority),
		AccessMode: mountproto.AccessModeReadWrite, CasePolicy: mountproto.CasePolicySensitive,
		Presentations: []mountproto.Presentation{mountproto.PresentationMount},
		Generation:    uint64(tenant.Generation),
	}
}

// PublishSnapshot commits one complete authority revision, then derives and
// waits for the exact PrepareTenant proof from that commit.
func (c *RuntimeClient) PublishSnapshot(
	ctx context.Context,
	tenant Tenant,
	revision uint64,
	snapshot AuthoritySnapshot,
	cause catalogproto.ConvergenceCause,
) (catalogproto.PreparationProof, error) {
	return c.publishAndPrepare(ctx, tenant, revision, 0, catalogproto.SourceModeSnapshot, snapshot.objects, nil, cause)
}

// PublishDelta commits one exact successor revision and waits for its derived
// PrepareTenant proof. Skipped or mismatched predecessors fail locally.
func (c *RuntimeClient) PublishDelta(
	ctx context.Context,
	tenant Tenant,
	revision, predecessor uint64,
	delta AuthorityDelta,
	cause catalogproto.ConvergenceCause,
) (catalogproto.PreparationProof, error) {
	if predecessor == 0 || revision != predecessor+1 {
		return catalogproto.PreparationProof{}, errors.New("cc-notes authority: delta must be the exact successor of a nonzero predecessor")
	}
	return c.publishAndPrepare(ctx, tenant, revision, predecessor, catalogproto.SourceModeDelta, delta.objects, delta.deletes, cause)
}

func (c *RuntimeClient) publishAndPrepare(
	ctx context.Context,
	tenant Tenant,
	revision, predecessor uint64,
	mode catalogproto.SourceMode,
	objects []authorityObject,
	deletes []catalogproto.SourceDeleteRecord,
	cause catalogproto.ConvergenceCause,
) (catalogproto.PreparationProof, error) {
	if err := tenant.Validate(c.plan); err != nil {
		return catalogproto.PreparationProof{}, err
	}
	if revision == 0 {
		return catalogproto.PreparationProof{}, errors.New("cc-notes authority: source revision is required")
	}
	changeID, err := convergence.NewChangeID()
	if err != nil {
		return catalogproto.PreparationProof{}, err
	}
	operationID, err := convergence.NewOperationID()
	if err != nil {
		return catalogproto.PreparationProof{}, err
	}
	change := catalogproto.ChangeID(hex.EncodeToString(changeID[:]))
	operation := catalogproto.MutationID(hex.EncodeToString(operationID[:]))
	input, closers, err := sourceInput(tenant, revision, objects, deletes)
	if err != nil {
		return catalogproto.PreparationProof{}, err
	}
	defer closeAll(closers)
	response, err := c.catalog.ReconcileSource(ctx, catalogproto.SourceReconcileRequest{
		Protocol: catalogproto.Version, Mode: mode,
		SourceAuthority: tenant.Authority, SourceRevision: revision, PredecessorRevision: predecessor,
		ChangeID: change, OperationID: operation, Cause: cause,
		AffectedKeys: affectedKeys(objects, deletes), TenantCount: 1,
	}, []catalogservice.SourceTenantInput{input})
	if err != nil {
		return catalogproto.PreparationProof{}, err
	}
	if len(response.Commits) != 1 || response.Commits[0].TenantID != catalogproto.TenantID(tenant.ID) {
		return catalogproto.PreparationProof{}, errors.New("cc-notes authority: source response did not commit the requested tenant")
	}
	prepared, err := c.catalog.PrepareTenant(ctx, catalogproto.TenantID(tenant.ID), prepareRequest(tenant, response))
	if err != nil {
		return catalogproto.PreparationProof{}, err
	}
	if prepared.Proof == nil {
		return catalogproto.PreparationProof{}, errors.New("cc-notes authority: PrepareTenant returned no proof")
	}
	return *prepared.Proof, nil
}

func affectedKeys(objects []authorityObject, deletes []catalogproto.SourceDeleteRecord) []string {
	keys := make([]string, 0, len(objects)+len(deletes))
	for _, object := range objects {
		keys = append(keys, object.key)
	}
	for _, record := range deletes {
		keys = append(keys, record.SourceKey)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func prepareRequest(tenant Tenant, response catalogproto.SourceReconcileResponse) catalogproto.PrepareTenantRequest {
	return catalogproto.PrepareTenantRequest{
		Protocol: catalogproto.Version, DomainID: tenant.Domain, Generation: uint64(tenant.Generation),
		CatalogRevision: response.Commits[0].CatalogRevision,
		SourceAuthority: response.SourceAuthority, SourceRevision: response.SourceRevision,
		ChangeID: response.ChangeID, OperationID: response.OperationID,
	}
}

type projectionBuilder struct {
	ctx     context.Context
	store   *store.Store
	objects []authorityObject
	snaps   map[model.Kind][]model.Snapshot
}

func (b *projectionBuilder) build() error {
	b.snaps = make(map[model.Kind][]model.Snapshot, len(model.Kinds()))
	for _, kind := range model.Kinds() {
		rooted, err := b.store.ListRootedSnapshots(b.ctx, kind, store.ListOpts{})
		if err != nil {
			return fmt.Errorf("cc-notes authority: list %s: %w", kind, err)
		}
		snapshots := make([]model.Snapshot, len(rooted))
		keys := make(map[model.EntityID]string, len(rooted))
		for index, value := range rooted {
			snapshots[index] = value.Snapshot
			key, err := entitySourceKey(kind, value.Root)
			if err != nil {
				return fmt.Errorf("cc-notes authority: source key for %s %s: %w", kind, value.Snapshot.EntityID(), err)
			}
			keys[value.Snapshot.EntityID()] = key
		}
		slices.SortFunc(snapshots, func(a, z model.Snapshot) int { return strings.Compare(string(a.EntityID()), string(z.EntityID())) })
		b.snaps[kind] = snapshots
		layout := layouts[kind]
		parent := "kind:" + string(kind)
		b.dir(parent, "", strings.TrimPrefix(layout.dir, "/"))
		for _, snapshot := range snapshots {
			body := codecOf(kind).Render(snapshot)
			mode := uint32(0o644)
			if codecOf(kind).ReadOnly() {
				mode = 0o444
			}
			b.file(keys[snapshot.EntityID()], parent, Filename(snapshot), mode, body)
			if codecOf(kind).Browsable() {
				b.dir(browseKey(kind, snapshot.EntityID()), parent, snapshot.EntityID().Short())
			}
		}
	}
	b.buildBrowseTrees()
	return b.buildAttachments()
}

func (b *projectionBuilder) buildBrowseTrees() {
	tasks := typedSnapshots[model.Task](b.snaps[model.KindTask])
	sprints := typedSnapshots[model.Sprint](b.snaps[model.KindSprint])
	projects := typedSnapshots[model.Project](b.snaps[model.KindProject])
	for _, sprint := range sprints {
		base := browseKey(model.KindSprint, sprint.ID)
		tasksKey := base + ":tasks"
		b.dir(tasksKey, base, "tasks")
		for _, task := range tasks {
			if task.Sprint == sprint.ID {
				linkPath := path.Join("/sprints", sprint.ID.Short(), "tasks", Filename(task))
				b.symlink("sprint-task:"+string(sprint.ID)+":"+string(task.ID), tasksKey, Filename(task), SymlinkTarget(linkPath, "tasks/"+Filename(task)))
			}
		}
	}
	for _, project := range projects {
		base := browseKey(model.KindProject, project.ID)
		sprintsKey, tasksKey := base+":sprints", base+":tasks"
		b.dir(sprintsKey, base, "sprints")
		b.dir(tasksKey, base, "tasks")
		projectSprints := make(map[model.EntityID]bool)
		for _, sprint := range sprints {
			if sprint.Project != project.ID {
				continue
			}
			projectSprints[sprint.ID] = true
			sprintKey := base + ":sprint:" + string(sprint.ID)
			sprintTasksKey := sprintKey + ":tasks"
			b.dir(sprintKey, sprintsKey, sprint.ID.Short())
			b.dir(sprintTasksKey, sprintKey, "tasks")
			for _, task := range tasks {
				if task.Sprint == sprint.ID {
					linkPath := path.Join("/projects", project.ID.Short(), "sprints", sprint.ID.Short(), "tasks", Filename(task))
					b.symlink("project-sprint-task:"+string(project.ID)+":"+string(sprint.ID)+":"+string(task.ID), sprintTasksKey, Filename(task), SymlinkTarget(linkPath, "tasks/"+Filename(task)))
				}
			}
		}
		for _, task := range tasks {
			if task.Project != project.ID && !projectSprints[task.Sprint] {
				continue
			}
			linkPath := path.Join("/projects", project.ID.Short(), "tasks", Filename(task))
			b.symlink("project-task:"+string(project.ID)+":"+string(task.ID), tasksKey, Filename(task), SymlinkTarget(linkPath, "tasks/"+Filename(task)))
		}
	}
}

func (b *projectionBuilder) buildAttachments() error {
	root := "attachments"
	b.dir(root, "", "attachments")
	byShort := make(map[string][]model.Snapshot)
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindLog, model.KindInvestigation} {
		for _, snapshot := range b.snaps[kind] {
			if len(snapshot.Meta().Attachments) != 0 {
				byShort[snapshot.EntityID().Short()] = append(byShort[snapshot.EntityID().Short()], snapshot)
			}
		}
	}
	lfs, err := b.store.LFS(b.ctx)
	if err != nil {
		return fmt.Errorf("cc-notes authority: resolve attachment store: %w", err)
	}
	shorts := make([]string, 0, len(byShort))
	for short := range byShort {
		shorts = append(shorts, short)
	}
	slices.Sort(shorts)
	for _, short := range shorts {
		matches := byShort[short]
		if len(matches) != 1 {
			continue
		}
		snapshot := matches[0]
		parent := "attachments:" + string(snapshot.EntityID())
		b.dir(parent, root, short)
		for _, attachment := range snapshot.Meta().Attachments {
			attachmentPath := lfs.Path(attachment.OID)
			file, err := lfs.Open(attachment.OID)
			if err != nil {
				return fmt.Errorf("cc-notes authority: read attachment %s/%s: %w", snapshot.EntityID(), attachment.Name, err)
			}
			digest := sha256.New()
			size, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("cc-notes authority: hash attachment %s/%s: %w", snapshot.EntityID(), attachment.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("cc-notes authority: close attachment %s/%s: %w", snapshot.EntityID(), attachment.Name, closeErr)
			}
			if size != attachment.Size || hex.EncodeToString(digest.Sum(nil)) != attachment.OID {
				return fmt.Errorf("cc-notes authority: attachment %s/%s failed size/hash verification", snapshot.EntityID(), attachment.Name)
			}
			contentSize, err := nonnegativeSize(size)
			if err != nil {
				return fmt.Errorf("cc-notes authority: attachment %s/%s: %w", snapshot.EntityID(), attachment.Name, err)
			}
			b.filePath(
				"attachment:"+string(snapshot.EntityID())+":"+attachment.Name,
				parent,
				attachment.Name,
				0o444,
				attachmentPath,
				contentSize,
				attachment.OID,
			)
		}
	}
	return nil
}

func nonnegativeSize(value int64) (uint64, error) {
	if value < 0 {
		return 0, errors.New("negative content size")
	}
	return uint64(value), nil //nolint:gosec // nonnegative value proved above
}

func (b *projectionBuilder) dir(key, parent, name string) {
	b.objects = append(b.objects, authorityObject{key: key, parent: parent, name: name, kind: catalogproto.ObjectKindDirectory, mode: 0o755})
}

func (b *projectionBuilder) file(key, parent, name string, mode uint32, content []byte) {
	b.objects = append(b.objects, authorityObject{key: key, parent: parent, name: name, kind: catalogproto.ObjectKindFile, mode: mode, content: content})
}

func (b *projectionBuilder) filePath(key, parent, name string, mode uint32, contentPath string, size uint64, hash string) {
	b.objects = append(b.objects, authorityObject{
		key: key, parent: parent, name: name, kind: catalogproto.ObjectKindFile,
		mode: mode, contentPath: contentPath, size: size, hash: hash,
	})
}

func (b *projectionBuilder) symlink(key, parent, name, target string) {
	b.objects = append(b.objects, authorityObject{key: key, parent: parent, name: name, kind: catalogproto.ObjectKindSymlink, mode: 0o777, linkTarget: target})
}

func browseKey(kind model.Kind, id model.EntityID) string {
	return "browse:" + string(kind) + ":" + string(id)
}

func typedSnapshots[T model.Snapshot](values []model.Snapshot) []T {
	result := make([]T, len(values))
	for index, value := range values {
		result[index] = value.(T)
	}
	return result
}

func validateAuthorityObjects(objects []authorityObject) error {
	keys := make(map[string]struct{}, len(objects))
	names := make(map[string]struct{}, len(objects))
	for _, object := range objects {
		if object.key == "" || object.name == "" || object.name == "." || object.name == ".." || strings.ContainsAny(object.name, "/\x00") {
			return fmt.Errorf("cc-notes authority: invalid object %q/%q", object.key, object.name)
		}
		if _, found := keys[object.key]; found {
			return fmt.Errorf("cc-notes authority: duplicate source key %q", object.key)
		}
		keys[object.key] = struct{}{}
		nameKey := object.parent + "\x00" + object.name
		if _, found := names[nameKey]; found {
			return fmt.Errorf("cc-notes authority: duplicate name %q below %q", object.name, object.parent)
		}
		names[nameKey] = struct{}{}
		if object.parent != "" {
			if _, found := keys[object.parent]; !found {
				return fmt.Errorf("cc-notes authority: parent %q is not ordered before %q", object.parent, object.key)
			}
		}
	}
	return nil
}

func sameAuthorityObject(a, b authorityObject) bool {
	return a.key == b.key && a.parent == b.parent && a.name == b.name && a.kind == b.kind &&
		a.mode == b.mode && a.contentPath == b.contentPath && a.size == b.size && a.hash == b.hash &&
		a.linkTarget == b.linkTarget && bytes.Equal(a.content, b.content)
}

func exactAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}
