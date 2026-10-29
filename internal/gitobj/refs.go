package gitobj

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

const refReadBuffer = 512

type referenceLookup struct {
	storer.ReferenceStorer
	repo *Repo
	ctx  context.Context
}

func (l referenceLookup) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	return l.repo.reference(l.ctx, name)
}

type packedReferences struct {
	reader io.ReaderAt
	size   int64
	start  int64
	sorted bool
}

type packedReference struct {
	name       string
	hash       plumbing.Hash
	start, end int64
}

// ReferenceGeneration identifies metadata changed by atomic updates in a flat
// ref namespace. It does not detect in-place loose-ref rewrites.
type ReferenceGeneration struct {
	packed    referenceStamp
	loose     referenceStamp
	directory string
}

type referenceStamp struct {
	device, inode uint64
	ctime, mtime  int64
	size          int64
}

// Key encodes the generation for a persisted derived index.
func (g ReferenceGeneration) Key() string {
	return fmt.Sprintf("refs-v1 %q %d:%d:%d:%d:%d %d:%d:%d:%d:%d", g.directory,
		g.packed.device, g.packed.inode, g.packed.ctime, g.packed.mtime, g.packed.size,
		g.loose.device, g.loose.inode, g.loose.ctime, g.loose.mtime, g.loose.size)
}

// SettledAt reports whether the metadata predates the filesystem write window.
// taken is sampled before RefGeneration reads its metadata.
func (g ReferenceGeneration) SettledAt(taken time.Time) bool {
	latest := max(g.packed.ctime, g.packed.mtime, g.loose.ctime, g.loose.mtime)
	return taken.Sub(time.Unix(0, latest)) >= packsRacyWindow
}

// RefGeneration detects atomic ref updates without enumerating the namespace.
func (r *Repo) RefGeneration(ctx context.Context, prefix string) (ReferenceGeneration, error) {
	if err := ctx.Err(); err != nil {
		return ReferenceGeneration{}, err
	}
	if !strings.HasPrefix(prefix, "refs/") || !strings.HasSuffix(prefix, "/") {
		return ReferenceGeneration{}, fmt.Errorf("reference generation requires a ref directory: %q", prefix)
	}
	if err := plumbing.ReferenceName(strings.TrimSuffix(prefix, "/")).Validate(); err != nil {
		return ReferenceGeneration{}, err
	}
	packed, err := r.referenceStamp("packed-refs")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ReferenceGeneration{}, err
	}
	dir := strings.TrimSuffix(prefix, "/")
	for {
		loose, err := r.referenceStamp(dir)
		if err == nil {
			return ReferenceGeneration{packed: packed, loose: loose, directory: dir}, nil
		}
		if !errors.Is(err, os.ErrNotExist) || dir == "." {
			return ReferenceGeneration{}, err
		}
		dir = path.Dir(dir)
	}
}

func (r *Repo) referenceStamp(name string) (referenceStamp, error) {
	info, err := r.fs.Stat(name)
	if err != nil {
		return referenceStamp{}, err
	}
	device, inode := FileID(info)
	ctime, _ := StatIdentity(info)
	return referenceStamp{device: device, inode: inode, ctime: ctime, mtime: info.ModTime().UnixNano(), size: info.Size()}, nil
}

func openPackedReferences(reader io.ReaderAt, size int64) (*packedReferences, error) {
	p := &packedReferences{reader: reader, size: size}
	for p.start < p.size {
		line, end, err := p.line(p.start)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(line, "# pack-refs with:") {
			for _, flag := range strings.Fields(line) {
				p.sorted = p.sorted || flag == "sorted"
			}
		}
		p.start = end
	}
	return p, nil
}

func (p *packedReferences) line(start int64) (string, int64, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(p.reader, start, p.size-start), refReadBuffer)
	line, err := r.ReadString('\n')
	if errors.Is(err, io.EOF) && len(line) > 0 {
		err = nil
	}
	return strings.TrimSuffix(line, "\n"), start + int64(len(line)), err
}

func (p *packedReferences) entry(start int64) (packedReference, error) {
	line, end, err := p.line(start)
	if err != nil {
		return packedReference{}, err
	}
	entry, err := parsePackedReference(line)
	if err != nil {
		return packedReference{}, err
	}
	entry.start, entry.end = start, end
	if end < p.size {
		line, end, err = p.line(end)
		if err != nil {
			return packedReference{}, err
		}
		if strings.HasPrefix(line, "^") {
			entry.end = end
		}
	}
	return entry, nil
}

func parsePackedReference(line string) (packedReference, error) {
	hash, name, ok := strings.Cut(line, " ")
	if !ok || !plumbing.IsHash(hash) || !strings.HasPrefix(name, "refs/") {
		return packedReference{}, fmt.Errorf("invalid packed ref %q", line)
	}
	return packedReference{name: name, hash: plumbing.NewHash(hash)}, nil
}

func (p *packedReferences) scan(ctx context.Context, start int64, visit func(packedReference) bool) error {
	reader := bufio.NewReader(io.NewSectionReader(p.reader, start, p.size-start))
	previousRef := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.HasPrefix(line, "^") {
			if !previousRef {
				return fmt.Errorf("peeled packed ref without a preceding reference")
			}
			previousRef = false
		} else if len(line) > 0 {
			entry, parseErr := parsePackedReference(strings.TrimSuffix(line, "\n"))
			if parseErr != nil {
				return parseErr
			}
			if !visit(entry) {
				return nil
			}
			previousRef = true
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (p *packedReferences) lineStart(at int64) (int64, error) {
	var data [refReadBuffer]byte
	for at > p.start {
		start := max(p.start, at-int64(len(data)))
		n, err := p.reader.ReadAt(data[:at-start], start)
		if err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(data[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		at = start
	}
	return p.start, nil
}

func (p *packedReferences) entryAt(at int64) (packedReference, error) {
	start, err := p.lineStart(at)
	if err != nil {
		return packedReference{}, err
	}
	line, _, err := p.line(start)
	if err != nil {
		return packedReference{}, err
	}
	if strings.HasPrefix(line, "^") {
		start, err = p.lineStart(start - 1)
		if err != nil {
			return packedReference{}, err
		}
	}
	return p.entry(start)
}

func (p *packedReferences) lowerBound(ctx context.Context, name string) (int64, error) {
	low, high := p.start, p.size
	for low < high {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entry, err := p.entryAt(low + (high-low)/2)
		if err != nil {
			return 0, err
		}
		if entry.name < name {
			low = entry.end
		} else {
			high = entry.start
		}
	}
	return low, nil
}

func (p *packedReferences) prefix(ctx context.Context, prefix string) (map[string]*plumbing.Reference, error) {
	out := make(map[string]*plumbing.Reference)
	start := p.start
	if p.sorted {
		var err error
		start, err = p.lowerBound(ctx, prefix)
		if err != nil {
			return nil, err
		}
	}
	err := p.scan(ctx, start, func(entry packedReference) bool {
		if !strings.HasPrefix(entry.name, prefix) {
			return !p.sorted
		}
		out[entry.name] = plumbing.NewHashReference(plumbing.ReferenceName(entry.name), entry.hash)
		return true
	})
	return out, err
}

func (p *packedReferences) find(ctx context.Context, name string) (*plumbing.Reference, error) {
	if !p.sorted {
		var found *plumbing.Reference
		err := p.scan(ctx, p.start, func(entry packedReference) bool {
			if entry.name != name {
				return true
			}
			found = plumbing.NewHashReference(plumbing.ReferenceName(name), entry.hash)
			return false
		})
		if err != nil {
			return nil, err
		}
		if found == nil {
			return nil, plumbing.ErrReferenceNotFound
		}
		return found, nil
	}
	start, err := p.lowerBound(ctx, name)
	if err != nil {
		return nil, err
	}
	if start == p.size {
		return nil, plumbing.ErrReferenceNotFound
	}
	entry, err := p.entry(start)
	if err != nil {
		return nil, err
	}
	if entry.name != name {
		return nil, plumbing.ErrReferenceNotFound
	}
	return plumbing.NewHashReference(plumbing.ReferenceName(name), entry.hash), nil
}

func (r *Repo) reference(ctx context.Context, name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := name.Validate(); err != nil {
		return nil, plumbing.ErrReferenceNotFound
	}
	f, err := r.fs.Open(string(name))
	if err == nil {
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, dotgit.ErrEmptyRefFile
		}
		return plumbing.NewReferenceFromStrings(string(name), strings.TrimSpace(string(data))), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	p, file, err := r.openPackedRefs()
	if errors.Is(err, os.ErrNotExist) {
		return nil, plumbing.ErrReferenceNotFound
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return p.find(ctx, string(name))
}

func (r *Repo) openPackedRefs() (*packedReferences, io.Closer, error) {
	f, err := r.fs.Open("packed-refs")
	if err != nil {
		return nil, nil, err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	p, err := openPackedReferences(f, size)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return p, f, nil
}

func (r *Repo) packedPrefix(ctx context.Context, prefix string) (map[string]*plumbing.Reference, error) {
	p, file, err := r.openPackedRefs()
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]*plumbing.Reference), nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return p.prefix(ctx, prefix)
}

func (r *Repo) referencePrefix(ctx context.Context, prefix string) (map[string]*plumbing.Reference, error) {
	out, err := r.packedPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix("HEAD", prefix) {
		head, err := r.reference(ctx, plumbing.HEAD)
		if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, err
		}
		if head != nil {
			out["HEAD"] = head
		}
	}
	if prefix != "" && !strings.HasPrefix(prefix, "refs/") && !strings.HasPrefix("refs/", prefix) {
		return out, nil
	}
	dir := path.Dir(prefix)
	if strings.HasSuffix(prefix, "/") {
		dir = strings.TrimSuffix(prefix, "/")
	}
	if dir == "." || !strings.HasPrefix(dir, "refs") {
		dir = "refs"
	}
	var walk func(string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := r.fs.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := path.Join(dir, entry.Name())
			if !strings.HasPrefix(name, prefix) && !strings.HasPrefix(prefix, name+"/") {
				continue
			}
			if entry.IsDir() {
				if err := walk(name); err != nil {
					return err
				}
				continue
			}
			ref, err := r.reference(ctx, plumbing.ReferenceName(name))
			if errors.Is(err, plumbing.ErrReferenceNotFound) {
				delete(out, name)
				continue
			}
			if err != nil {
				return err
			}
			out[name] = ref
		}
		return nil
	}
	if err := walk(dir); err != nil {
		return nil, err
	}
	return out, nil
}
