package gitobj

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

const (
	sourceManifestFile   = "source-index"
	sourceManifestHeader = "cc-notes-source-index-v1\n"
)

var ErrCorruptSourceManifest = errors.New("corrupt source manifest")

// SourceRefTip is one entity ref and its immutable commit tip.
type SourceRefTip struct {
	Ref string
	Tip model.SHA
}

// SourceManifest is one canonical, ref-sorted view of every live entity tip.
type SourceManifest []SourceRefTip

// NewSourceManifest validates and canonicalizes tips.
func NewSourceManifest(tips map[string]model.SHA) (SourceManifest, error) {
	manifest := make(SourceManifest, 0, len(tips))
	for ref, tip := range tips {
		if err := validateSourceRefTip(SourceRefTip{Ref: ref, Tip: tip}); err != nil {
			return nil, err
		}
		manifest = append(manifest, SourceRefTip{Ref: ref, Tip: tip})
	}
	slices.SortFunc(manifest, func(a, b SourceRefTip) int { return strings.Compare(a.Ref, b.Ref) })
	return manifest, nil
}

// Tips returns a detached ref-to-tip map.
func (m SourceManifest) Tips() map[string]model.SHA {
	tips := make(map[string]model.SHA, len(m))
	for _, entry := range m {
		tips[entry.Ref] = entry.Tip
	}
	return tips
}

// Equal reports whether two canonical manifests name the same exact tips.
func (m SourceManifest) Equal(other SourceManifest) bool { return slices.Equal(m, other) }

// WriteSourceManifestCommit stores a deterministic derived source revision.
// It writes objects only; the caller moves the source-index ref atomically.
func (r *Repo) WriteSourceManifestCommit(ctx context.Context, parent model.SHA, manifest SourceManifest) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := encodeSourceManifest(manifest)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	blob, err := r.writeBlob(data)
	if err != nil {
		return "", fmt.Errorf("write source manifest blob: %w", err)
	}
	tree, err := r.writeObject(&object.Tree{Entries: []object.TreeEntry{{Name: sourceManifestFile, Mode: filemode.Regular, Hash: blob}}})
	if err != nil {
		return "", fmt.Errorf("write source manifest tree: %w", err)
	}
	parents := []plumbing.Hash(nil)
	if parent != "" {
		if !plumbing.IsHash(string(parent)) {
			return "", fmt.Errorf("write source manifest: invalid parent sha %q", parent)
		}
		parents = []plumbing.Hash{plumbing.NewHash(string(parent))}
	}
	identity := object.Signature{
		Name:  "CC Notes Source Driver",
		Email: "source-driver@cc-notes.invalid",
		When:  time.Unix(0, 0).UTC(),
	}
	sha, err := r.writeObject(&object.Commit{
		Author: identity, Committer: identity, Message: "cc-notes: source revision",
		TreeHash: tree, ParentHashes: parents,
	})
	if err != nil {
		return "", fmt.Errorf("write source manifest commit: %w", err)
	}
	return model.SHA(sha.String()), nil
}

// ReadSourceManifestCommit reads and strictly validates one derived source
// revision and returns its single predecessor, if any.
func (r *Repo) ReadSourceManifestCommit(ctx context.Context, sha model.SHA) (SourceManifest, model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	commit, err := r.commit(sha)
	if err != nil {
		return nil, "", fmt.Errorf("read source manifest commit %s: %w", sha, err)
	}
	if len(commit.ParentHashes) > 1 {
		return nil, "", fmt.Errorf("%w: source revision %s has %d parents", ErrCorruptSourceManifest, sha, len(commit.ParentHashes))
	}
	file, err := commit.File(sourceManifestFile)
	if err != nil {
		return nil, "", fmt.Errorf("%w: source revision %s: %v", ErrCorruptSourceManifest, sha, err)
	}
	reader, err := file.Reader()
	if err != nil {
		return nil, "", fmt.Errorf("%w: source revision %s reader: %v", ErrCorruptSourceManifest, sha, err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, "", fmt.Errorf("%w: source revision %s read: %v", ErrCorruptSourceManifest, sha, readErr)
	}
	if closeErr != nil {
		return nil, "", fmt.Errorf("%w: source revision %s close: %v", ErrCorruptSourceManifest, sha, closeErr)
	}
	manifest, err := decodeSourceManifest(data)
	if err != nil {
		return nil, "", fmt.Errorf("source revision %s: %w", sha, err)
	}
	var parent model.SHA
	if len(commit.ParentHashes) == 1 {
		parent = model.SHA(commit.ParentHashes[0].String())
	}
	return manifest, parent, nil
}

func encodeSourceManifest(manifest SourceManifest) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(sourceManifestHeader)
	previous := ""
	for _, entry := range manifest {
		if err := validateSourceRefTip(entry); err != nil {
			return nil, err
		}
		if previous != "" && entry.Ref <= previous {
			return nil, fmt.Errorf("source manifest refs are not strictly sorted: %q after %q", entry.Ref, previous)
		}
		previous = entry.Ref
		buffer.WriteString(entry.Ref)
		buffer.WriteByte('\t')
		buffer.WriteString(string(entry.Tip))
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

func decodeSourceManifest(data []byte) (SourceManifest, error) {
	if !bytes.HasPrefix(data, []byte(sourceManifestHeader)) || len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("%w: invalid header or terminator", ErrCorruptSourceManifest)
	}
	body := strings.TrimSuffix(string(data[len(sourceManifestHeader):]), "\n")
	if body == "" {
		return SourceManifest{}, nil
	}
	lines := strings.Split(body, "\n")
	manifest := make(SourceManifest, 0, len(lines))
	previous := ""
	for index, line := range lines {
		ref, tip, ok := strings.Cut(line, "\t")
		entry := SourceRefTip{Ref: ref, Tip: model.SHA(tip)}
		if !ok || strings.ContainsRune(tip, '\t') {
			return nil, fmt.Errorf("%w: malformed line %d", ErrCorruptSourceManifest, index)
		}
		if err := validateSourceRefTip(entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrCorruptSourceManifest, index, err)
		}
		if previous != "" && entry.Ref <= previous {
			return nil, fmt.Errorf("%w: refs are not strictly sorted", ErrCorruptSourceManifest)
		}
		previous = entry.Ref
		manifest = append(manifest, entry)
	}
	return manifest, nil
}

func validateSourceRefTip(entry SourceRefTip) error {
	switch {
	case entry.Ref == "":
		return errors.New("source manifest: empty ref")
	case strings.ContainsAny(entry.Ref, "\x00\n\r\t"):
		return fmt.Errorf("source manifest: invalid ref %q", entry.Ref)
	case !plumbing.IsHash(string(entry.Tip)):
		return fmt.Errorf("source manifest: invalid tip %q", entry.Tip)
	default:
		return nil
	}
}
