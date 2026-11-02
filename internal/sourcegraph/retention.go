package sourcegraph

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

const (
	retentionNodeFormat = "cc-notes-retention-node-v1"
	retentionLeafFormat = "cc-notes-retention-leaf-v1"
	retentionAttrs      = "retained-lfs-* filter=lfs diff=lfs merge=lfs -text\n"
)

// RetentionTime keeps aggregate pointer commits in Git-LFS's recent-ref view.
var RetentionTime = time.Unix((1<<34)-1, 0).UTC()

// RetentionRoot separates permanent index metadata from content-retaining edges.
type RetentionRoot struct {
	Index  model.SHA
	Commit model.SHA
}

// RetainedEntity binds raw content and optional sealed import blobs to one key.
type RetainedEntity struct {
	Tip   model.SHA
	LFS   []gitobj.SourceOperationLFS
	Blobs []model.SHA
}

type retentionNode struct {
	Format   string        `json:"format"`
	Prefix   string        `json:"prefix"`
	Count    uint64        `json:"count"`
	Value    model.SHA     `json:"value"`
	Children [16]model.SHA `json:"children"`
}

type retentionLeaf struct {
	Format string         `json:"format"`
	KeyHex string         `json:"key_hex"`
	Entity RetainedEntity `json:"entity"`
}

// Retention updates shared aggregate pins without parenting previous versions.
type Retention struct{ index *Map }

// NewRetention shares the caller's operation scope with immutable metadata maps.
func NewRetention(objects *gitobj.SourceObjects) *Retention {
	index := New(objects)
	index.retention = true
	return &Retention{index: index}
}

// Stats returns the operation's retention-index object work.
func (r *Retention) Stats() Work { return r.index.Stats() }

// Put replaces one retained entity while sharing every untouched subtree.
func (r *Retention) Put(root RetentionRoot, key string, entity RetainedEntity) (RetentionRoot, error) {
	if err := validKey(key); err != nil {
		return RetentionRoot{}, err
	}
	if err := r.check(root); err != nil {
		return RetentionRoot{}, err
	}
	leaf, err := r.writeLeaf(key, entity)
	if err != nil {
		return RetentionRoot{}, err
	}
	value, err := r.index.objects.WriteBlob([]byte(leaf))
	if err != nil {
		return RetentionRoot{}, err
	}
	r.index.work.ValueWrites++
	next, err := r.index.mutate(root.Index, hexKey(key), value, leaf, false)
	if err != nil {
		return RetentionRoot{}, err
	}
	return r.root(next)
}

// Delete removes one retained entity without retaining its replaced graph.
func (r *Retention) Delete(root RetentionRoot, key string) (RetentionRoot, error) {
	if err := r.check(root); err != nil {
		return RetentionRoot{}, err
	}
	next, err := r.index.Delete(root.Index, key)
	if err != nil {
		return RetentionRoot{}, err
	}
	return r.root(next)
}

// Get reads one exact entity-retention declaration.
func (r *Retention) Get(root RetentionRoot, key string) (RetainedEntity, bool, error) {
	if err := r.check(root); err != nil {
		return RetainedEntity{}, false, err
	}
	data, found, err := r.index.Get(root.Index, key)
	if err != nil || !found {
		return RetainedEntity{}, found, err
	}
	leaf, err := r.readLeaf(model.SHA(data), key)
	return leaf.Entity, err == nil, err
}

// Count returns the number of retained entity keys.
func (r *Retention) Count(root RetentionRoot) (uint64, error) {
	if err := r.check(root); err != nil {
		return 0, err
	}
	return r.index.Count(root.Index)
}

// Validate explicitly checks all metadata edges and content-parent identities.
func (r *Retention) Validate(root RetentionRoot) error {
	if err := r.check(root); err != nil {
		return err
	}
	if err := r.index.Validate(root.Index); err != nil {
		return err
	}
	return r.validateNode(root.Index)
}

func (r *Retention) check(root RetentionRoot) error {
	if err := r.index.objects.Err(); err != nil {
		return err
	}
	if root.Index == "" {
		if root.Commit != "" {
			return fmt.Errorf("%w: empty retention index has a commit", ErrCorrupt)
		}
		return nil
	}
	n, err := r.index.load(root.Index)
	if err != nil {
		return err
	}
	if n.Aggregate != root.Commit {
		return fmt.Errorf("%w: retention root differs", ErrCorrupt)
	}
	return nil
}

func (r *Retention) root(index model.SHA) (RetentionRoot, error) {
	if index == "" {
		return RetentionRoot{}, nil
	}
	n, err := r.index.load(index)
	if err != nil {
		return RetentionRoot{}, err
	}
	return RetentionRoot{Index: index, Commit: n.Aggregate}, nil
}

func (m *Map) writeAggregate(n *node) (model.SHA, error) {
	value := retentionNode{Format: retentionNodeFormat, Prefix: n.Prefix, Count: n.Count, Value: n.ValueRetention}
	parents := make([]model.SHA, 0, 17)
	if n.value != "" {
		parents = append(parents, n.ValueRetention)
	}
	for i, next := range n.Children {
		value.Children[i] = next.Retention
		if next.Hash != "" {
			parents = append(parents, next.Retention)
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return writeRetentionCommit(m.objects, data, parents, nil)
}

func (r *Retention) writeLeaf(key string, entity RetainedEntity) (model.SHA, error) {
	if err := requireRetentionType(r.index.objects, entity.Tip, plumbing.CommitObject); err != nil {
		return "", err
	}
	entity.LFS = slices.Clone(entity.LFS)
	slices.SortFunc(entity.LFS, func(a, b gitobj.SourceOperationLFS) int {
		if order := cmp.Compare(a.OID, b.OID); order != 0 {
			return order
		}
		return cmp.Compare(a.Size, b.Size)
	})
	entity.LFS = slices.Compact(entity.LFS)
	if len(entity.LFS) == 0 {
		entity.LFS = nil
	}
	entity.Blobs = slices.Clone(entity.Blobs)
	slices.Sort(entity.Blobs)
	entity.Blobs = slices.Compact(entity.Blobs)
	if len(entity.Blobs) == 0 {
		entity.Blobs = nil
	}
	entries := make([]object.TreeEntry, 0, len(entity.LFS)+len(entity.Blobs)+1)
	pointers := 0
	for _, retained := range entity.LFS {
		if !model.ValidAttachmentOID(retained.OID) {
			return "", fmt.Errorf("invalid retained LFS oid %q", retained.OID)
		}
		if retained.Size <= 0 {
			continue
		}
		data := retentionPointer(retained)
		hash, err := r.index.objects.WriteBlob(data)
		if err != nil {
			return "", err
		}
		name := "retained-lfs-" + retained.OID + "-" + strconv.FormatInt(retained.Size, 10)
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: plumbing.NewHash(string(hash))})
		pointers++
	}
	if pointers != 0 {
		hash, err := r.index.objects.WriteBlob([]byte(retentionAttrs))
		if err != nil {
			return "", err
		}
		entries = append(entries, object.TreeEntry{Name: ".gitattributes", Mode: filemode.Regular, Hash: plumbing.NewHash(string(hash))})
	}
	for _, blob := range entity.Blobs {
		if err := requireRetentionType(r.index.objects, blob, plumbing.BlobObject); err != nil {
			return "", err
		}
		entries = append(entries, object.TreeEntry{Name: "sealed-" + string(blob), Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))})
	}
	data, err := json.Marshal(retentionLeaf{Format: retentionLeafFormat, KeyHex: hexKey(key), Entity: entity})
	if err != nil {
		return "", err
	}
	return writeRetentionCommit(r.index.objects, data, []model.SHA{entity.Tip}, entries)
}

func writeRetentionCommit(objects *gitobj.SourceObjects, data []byte, parents []model.SHA, entries []object.TreeEntry) (model.SHA, error) {
	blob, err := objects.WriteBlob(data)
	if err != nil {
		return "", err
	}
	entries = append(entries, object.TreeEntry{Name: "retention", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))})
	tree, err := objects.WriteTree(entries)
	if err != nil {
		return "", err
	}
	sig := gitobj.Signature{Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: RetentionTime}
	return objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Parents: parents, Author: sig, Committer: sig, Message: "cc-notes: source retention"})
}

func (r *Retention) readLeaf(hash model.SHA, key string) (retentionLeaf, error) {
	commit, data, err := readRetention(r.index.objects, hash)
	if err != nil {
		return retentionLeaf{}, err
	}
	var leaf retentionLeaf
	if err := json.Unmarshal(data, &leaf); err != nil {
		return retentionLeaf{}, fmt.Errorf("%w: retention leaf: %w", ErrCorrupt, err)
	}
	canonical, err := json.Marshal(leaf)
	if err != nil || !bytes.Equal(canonical, data) || leaf.Format != retentionLeafFormat || leaf.KeyHex != hexKey(key) || len(commit.Parents) != 1 || commit.Parents[0] != leaf.Entity.Tip {
		return retentionLeaf{}, fmt.Errorf("%w: retention leaf identity", ErrCorrupt)
	}
	if err := requireRetentionType(r.index.objects, leaf.Entity.Tip, plumbing.CommitObject); err != nil {
		return retentionLeaf{}, err
	}
	expected := map[string]plumbing.Hash{"retention": plumbing.ComputeHash(plumbing.BlobObject, data)}
	pointers := false
	for i, retained := range leaf.Entity.LFS {
		if !model.ValidAttachmentOID(retained.OID) || (i > 0 && (leaf.Entity.LFS[i-1].OID > retained.OID || (leaf.Entity.LFS[i-1].OID == retained.OID && leaf.Entity.LFS[i-1].Size >= retained.Size))) {
			return retentionLeaf{}, fmt.Errorf("%w: retained LFS order or identity", ErrCorrupt)
		}
		if retained.Size > 0 {
			name := "retained-lfs-" + retained.OID + "-" + strconv.FormatInt(retained.Size, 10)
			expected[name] = plumbing.ComputeHash(plumbing.BlobObject, retentionPointer(retained))
			pointers = true
		}
	}
	if pointers {
		expected[".gitattributes"] = plumbing.ComputeHash(plumbing.BlobObject, []byte(retentionAttrs))
	}
	for i, blob := range leaf.Entity.Blobs {
		if !plumbing.IsHash(string(blob)) || (i > 0 && leaf.Entity.Blobs[i-1] >= blob) {
			return retentionLeaf{}, fmt.Errorf("%w: sealed blob order or identity", ErrCorrupt)
		}
		if err := requireRetentionType(r.index.objects, blob, plumbing.BlobObject); err != nil {
			return retentionLeaf{}, err
		}
		expected["sealed-"+string(blob)] = plumbing.NewHash(string(blob))
	}
	entries, err := r.index.objects.ReadTree(commit.Tree)
	if err != nil {
		return retentionLeaf{}, err
	}
	if len(entries) != len(expected) {
		return retentionLeaf{}, fmt.Errorf("%w: retention leaf tree differs", ErrCorrupt)
	}
	for _, entry := range entries {
		if hash, found := expected[entry.Name]; !found || entry.Mode != filemode.Regular || entry.Hash != hash {
			return retentionLeaf{}, fmt.Errorf("%w: retention pointer or sealed blob differs", ErrCorrupt)
		}
		delete(expected, entry.Name)
	}
	return leaf, nil
}

func (r *Retention) validateNode(hash model.SHA) error {
	if hash == "" {
		return nil
	}
	n, err := r.index.load(hash)
	if err != nil {
		return err
	}
	commit, data, err := readRetention(r.index.objects, n.Aggregate)
	if err != nil {
		return err
	}
	want := retentionNode{Format: retentionNodeFormat, Prefix: n.Prefix, Count: n.Count, Value: n.ValueRetention}
	parents := make([]model.SHA, 0, 17)
	if n.value != "" {
		parents = append(parents, n.ValueRetention)
		value, err := r.index.value(n.value)
		if err != nil {
			return err
		}
		if model.SHA(value) != n.ValueRetention {
			return fmt.Errorf("%w: retention terminal value", ErrCorrupt)
		}
		key, err := decodeKey(n.Prefix)
		if err != nil {
			return err
		}
		if _, err := r.readLeaf(n.ValueRetention, key); err != nil {
			return err
		}
	}
	for i, next := range n.Children {
		want.Children[i] = next.Retention
		if next.Hash != "" {
			parents = append(parents, next.Retention)
		}
		if err := r.validateNode(next.Hash); err != nil {
			return err
		}
	}
	canonical, err := json.Marshal(want)
	if err != nil || !bytes.Equal(data, canonical) || !slices.Equal(commit.Parents, parents) {
		return fmt.Errorf("%w: retention parent graph differs", ErrCorrupt)
	}
	entries, err := r.index.objects.ReadTree(commit.Tree)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name != "retention" || entries[0].Mode != filemode.Regular {
		return fmt.Errorf("%w: aggregate tree retains unexpected content", ErrCorrupt)
	}
	return nil
}

func retentionPointer(retained gitobj.SourceOperationLFS) []byte {
	return []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + retained.OID + "\nsize " + strconv.FormatInt(retained.Size, 10) + "\n")
}

func requireRetentionType(objects *gitobj.SourceObjects, sha model.SHA, want plumbing.ObjectType) error {
	actual, err := objects.ObjectType(sha)
	if err != nil {
		return fmt.Errorf("%w: retained object %s: %w", ErrCorrupt, sha, err)
	}
	if actual != want {
		return fmt.Errorf("%w: retained object %s is %s, want %s", ErrCorrupt, sha, actual, want)
	}
	return nil
}

func readRetention(objects *gitobj.SourceObjects, hash model.SHA) (gitobj.SourceCommit, []byte, error) {
	commit, err := objects.ReadCommit(hash)
	if err != nil {
		return gitobj.SourceCommit{}, nil, err
	}
	if commit.Message != "cc-notes: source retention" || commit.Author.Name != "CC Notes Source Driver" || commit.Committer.Name != commit.Author.Name || commit.Author.Email != "source-driver@cc-notes.invalid" || commit.Committer.Email != commit.Author.Email || !commit.Author.When.Equal(RetentionTime) || !commit.Committer.When.Equal(RetentionTime) {
		return gitobj.SourceCommit{}, nil, fmt.Errorf("%w: retention commit identity", ErrCorrupt)
	}
	entries, err := objects.ReadTree(commit.Tree)
	if err != nil {
		return gitobj.SourceCommit{}, nil, err
	}
	for _, entry := range entries {
		if entry.Name == "retention" && entry.Mode == filemode.Regular {
			data, err := objects.ReadBlob(model.SHA(entry.Hash.String()))
			return commit, data, err
		}
	}
	return gitobj.SourceCommit{}, nil, fmt.Errorf("%w: missing retention record", ErrCorrupt)
}
