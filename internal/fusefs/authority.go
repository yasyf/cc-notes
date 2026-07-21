package fusefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
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
	deletes []sourcedriver.LogicalID
}

type authorityObject struct {
	key        string
	parent     string
	name       string
	kind       catalog.Kind
	mode       uint32
	content    []byte
	attachment string
	size       int64
	hash       catalog.ContentHash
	linkTarget string
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

// BuildAuthoritySnapshotAt renders the exact immutable entity tips sealed by
// a source revision. It never resolves mutable entity refs while projecting.
func BuildAuthoritySnapshotAt(ctx context.Context, source *store.Store, manifest gitobj.SourceManifest) (AuthoritySnapshot, error) {
	if source == nil {
		return AuthoritySnapshot{}, errors.New("cc-notes authority: store is required")
	}
	projection := projectionBuilder{ctx: ctx, store: source, manifest: manifest}
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
			result.deletes = append(result.deletes, sourcedriver.LogicalID(key))
		}
	}
	slices.SortFunc(result.deletes, func(a, z sourcedriver.LogicalID) int {
		return strings.Compare(string(a), string(z))
	})
	return result
}

// Len returns the number of upserts and deletes in the delta.
func (d AuthorityDelta) Len() int { return len(d.objects) + len(d.deletes) }

// AuthorityForTenant returns cc-notes' default isolated source authority for tenant.
func AuthorityForTenant(tenant catalog.TenantID) causal.SourceAuthorityID {
	return causal.SourceAuthorityID("cc-notes:" + string(tenant))
}

// RootKeyForTenant returns cc-notes' stable authority-owned catalog root key.
func RootKeyForTenant(tenant catalog.TenantID) string {
	return "root:" + string(tenant)
}

// Tenant is one cc-notes repository's immutable FuseKit identity.
type Tenant struct {
	ID         catalog.TenantID
	Generation catalog.Generation
	Authority  causal.SourceAuthorityID
	RouteName  string
	RepoRoot   string
}

// Validate rejects identities that cannot be represented by the hard runtime.
func (t Tenant) Validate() error {
	if _, err := catalog.NewTenantID(string(t.ID)); err != nil {
		return fmt.Errorf("cc-notes authority: tenant id: %w", err)
	}
	tenantID := string(t.ID)
	rootKey := RootKeyForTenant(t.ID)
	authorityID := string(t.Authority)
	switch {
	case len(tenantID) > 255 || !utf8.ValidString(tenantID) || strings.IndexFunc(tenantID, unicode.IsControl) >= 0:
		return fmt.Errorf("cc-notes authority: tenant id %q is not source-driver representable", t.ID)
	case len(rootKey) > sourcedriver.LogicalIDMaxBytes || strings.ContainsAny(rootKey, "/\\") ||
		strings.IndexFunc(rootKey, unicode.IsControl) >= 0:
		return fmt.Errorf("cc-notes authority: derived root key for tenant %q is not representable", t.ID)
	case t.Generation == 0:
		return errors.New("cc-notes authority: generation is required")
	case causal.ValidateSourceAuthorityID(t.Authority) != nil || strings.IndexFunc(authorityID, unicode.IsControl) >= 0:
		return fmt.Errorf("cc-notes authority: invalid source authority %q", t.Authority)
	case t.RouteName == "" || len(t.RouteName) > 255 || !utf8.ValidString(t.RouteName) ||
		strings.IndexFunc(t.RouteName, unicode.IsControl) >= 0 || filepath.Base(t.RouteName) != t.RouteName ||
		t.RouteName == "." || t.RouteName == "..":
		return fmt.Errorf("cc-notes authority: invalid route name %q", t.RouteName)
	case !exactAbsolutePath(t.RepoRoot):
		return fmt.Errorf("cc-notes authority: repository root %q is not an exact absolute path", t.RepoRoot)
	default:
		return nil
	}
}

type projectionBuilder struct {
	ctx      context.Context
	store    *store.Store
	manifest gitobj.SourceManifest
	objects  []authorityObject
	snaps    map[model.Kind][]model.Snapshot
}

func (b *projectionBuilder) build() error {
	b.snaps = make(map[model.Kind][]model.Snapshot, len(model.Kinds()))
	for _, kind := range model.Kinds() {
		rooted, err := b.rootedSnapshots(kind)
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

func (b *projectionBuilder) rootedSnapshots(kind model.Kind) ([]store.RootedSnapshot, error) {
	if b.manifest == nil {
		return b.store.ListRootedSnapshots(b.ctx, kind, store.ListOpts{})
	}
	result := make([]store.RootedSnapshot, 0)
	for _, entry := range b.manifest {
		parsed, err := refs.Parse(entry.Ref)
		if err != nil {
			return nil, err
		}
		if parsed.Kind != kind {
			continue
		}
		rooted, err := b.store.LoadRootedAt(b.ctx, entry.Tip)
		if err != nil {
			return nil, fmt.Errorf("load %s at %s: %w", entry.Ref, entry.Tip, err)
		}
		meta := rooted.Snapshot.Meta()
		if meta.Deleted || meta.Superseded {
			continue
		}
		if rooted.Snapshot.EntityID() != parsed.ID {
			return nil, fmt.Errorf("source ref %s identifies %s but folds to %s", entry.Ref, parsed.ID, rooted.Snapshot.EntityID())
		}
		result = append(result, rooted)
	}
	return result, nil
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
			hash, err := contentHash(attachment.OID)
			if err != nil {
				return fmt.Errorf("cc-notes authority: attachment %s/%s: %w", snapshot.EntityID(), attachment.Name, err)
			}
			if attachment.Size < 0 {
				return fmt.Errorf("cc-notes authority: attachment %s/%s has negative size", snapshot.EntityID(), attachment.Name)
			}
			b.fileAttachment(
				attachmentKey(snapshot.EntityID(), attachment.Name),
				parent,
				attachment.Name,
				0o444,
				attachment.Size,
				hash,
				attachment.OID,
			)
		}
	}
	return nil
}

func (b *projectionBuilder) dir(key, parent, name string) {
	b.objects = append(b.objects, authorityObject{key: key, parent: parent, name: name, kind: catalog.KindDirectory, mode: 0o755})
}

func (b *projectionBuilder) file(key, parent, name string, mode uint32, content []byte) {
	hash := sha256.Sum256(content)
	b.objects = append(b.objects, authorityObject{
		key: key, parent: parent, name: name, kind: catalog.KindFile, mode: mode,
		content: content, size: int64(len(content)), hash: catalog.ContentHash(hash),
	})
}

func (b *projectionBuilder) fileAttachment(key, parent, name string, mode uint32, size int64, hash catalog.ContentHash, oid string) {
	b.objects = append(b.objects, authorityObject{
		key: key, parent: parent, name: name, kind: catalog.KindFile,
		mode: mode, attachment: oid, size: size, hash: hash,
	})
}

func (b *projectionBuilder) symlink(key, parent, name, target string) {
	b.objects = append(b.objects, authorityObject{key: key, parent: parent, name: name, kind: catalog.KindSymlink, mode: 0o777, linkTarget: target})
}

func contentHash(value string) (catalog.ContentHash, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size {
		return catalog.ContentHash{}, errors.New("content hash is not canonical SHA-256")
	}
	var hash catalog.ContentHash
	copy(hash[:], raw)
	return hash, nil
}

func browseKey(kind model.Kind, id model.EntityID) string {
	return "browse:" + string(kind) + ":" + string(id)
}

func attachmentKey(id model.EntityID, name string) string {
	digest := sha256.Sum256([]byte(name))
	return "attachment:" + string(id) + ":" + hex.EncodeToString(digest[:])
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
		if !validAuthorityKey(object.key) || object.name == "" || object.name == "." || object.name == ".." || strings.ContainsAny(object.name, "/\x00") {
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
			if !validAuthorityKey(object.parent) {
				return fmt.Errorf("cc-notes authority: invalid parent key %q", object.parent)
			}
			if _, found := keys[object.parent]; !found {
				return fmt.Errorf("cc-notes authority: parent %q is not ordered before %q", object.parent, object.key)
			}
		}
	}
	return nil
}

func validAuthorityKey(value string) bool {
	return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "/\\") &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func sameAuthorityObject(a, b authorityObject) bool {
	return a.key == b.key && a.parent == b.parent && a.name == b.name && a.kind == b.kind &&
		a.mode == b.mode && a.attachment == b.attachment && a.size == b.size && a.hash == b.hash &&
		a.linkTarget == b.linkTarget && bytes.Equal(a.content, b.content)
}

func exactAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}
