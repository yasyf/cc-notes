package gitobj

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: git's pack and index trailers are SHA-1.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	gitsync "github.com/go-git/go-git/v5/utils/sync"
)

// managedPackListFile, under the common directory, holds managedPackNames' listing.
const managedPackListFile = "cc-notes/managed-packs"

// managedPacks serves objects from the packs source scopes wrote, in process.
// These packs hold no deltas, so one mapped lookup replaces a round trip to a
// git child. A pack this list has not seen falls through to that child, which
// always finds it. Callers hold the handle's mu.
type managedPacks struct {
	modTime time.Time
	packs   []*managedPack
}

type managedPack struct {
	path  string
	tier  int
	index []byte
	data  []byte
	gone  bool
	// cleanup unmaps a pack whose handle is dropped without a later refresh.
	cleanup runtime.Cleanup
}

func (s *objectStorage) managedObject(hash plumbing.Hash) (plumbing.EncodedObject, error) {
	if err := s.refreshManagedPacks(); err != nil {
		return nil, err
	}
	for _, pack := range s.managed.packs {
		if err := pack.mapFiles(); err != nil {
			return nil, err
		}
		offset, found := pack.find(hash)
		if !found {
			continue
		}
		if cached, ok := s.objects.Get(hash); ok {
			return cached, nil
		}
		value, err := pack.object(offset)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", hash, pack.path, err)
		}
		s.objects.Put(value)
		return value, nil
	}
	return nil, plumbing.ErrObjectNotFound
}

// refreshManagedPacks relists the pack directory at most once per read scope,
// and only when its mtime moved. Listing it costs real time on a repository that
// holds thousands of pack files.
func (s *objectStorage) refreshManagedPacks() error {
	if s.scope.managedFresh {
		return nil
	}
	s.scope.managedFresh = true
	dir := filepath.Join(s.root, packDir)
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.ModTime().Equal(s.managed.modTime) {
		return nil
	}
	names, err := s.managedPackNames(dir, info.ModTime())
	if err != nil {
		return err
	}
	current := make(map[string]*managedPack, len(s.managed.packs))
	for _, pack := range s.managed.packs {
		current[pack.path] = pack
	}
	packs := make([]*managedPack, 0, len(s.managed.packs))
	for _, name := range names {
		tier, stem, ok := managedPackStem(name, packIndexExt)
		if !ok {
			continue
		}
		path := filepath.Join(dir, stem)
		if pack, known := current[path]; known {
			packs = append(packs, pack)
			delete(current, path)
			continue
		}
		packs = append(packs, &managedPack{path: path, tier: tier})
	}
	slices.SortStableFunc(packs, func(a, b *managedPack) int { return a.tier - b.tier })
	var result error
	for _, pack := range current {
		result = errors.Join(result, pack.unmap())
	}
	s.managed.modTime, s.managed.packs = info.ModTime(), packs
	return result
}

// managedPackNames lists the managed pack indexes in dir. A pack directory that
// fetches have filled with thousands of other packs costs a long read, so the
// names are kept beside the authority, keyed by the directory's modification
// time, which any pack written or removed there changes.
func (s *objectStorage) managedPackNames(dir string, modified time.Time) ([]string, error) {
	cache := filepath.Join(s.root, managedPackListFile)
	key := strconv.FormatInt(modified.UnixNano(), 10)
	//nolint:gosec // G304: cache is a fixed path inside the repository's git directory.
	if data, err := os.ReadFile(cache); err == nil {
		if header, names, found := strings.Cut(string(data), "\n"); found && header == key {
			return strings.Fields(names), nil
		}
	}
	listing, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range listing {
		if _, _, ok := managedPackStem(entry.Name(), packIndexExt); ok {
			names = append(names, entry.Name())
		}
	}
	// The list only saves the next process a directory read, so failing to
	// store it costs nothing but that.
	_ = writeManagedPackNames(cache, key, names)
	return names, nil
}

func writeManagedPackNames(cache, key string, names []string) error {
	if err := os.MkdirAll(filepath.Dir(cache), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".managed-packs-*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(key + "\n" + strings.Join(names, "\n") + "\n")
	if err = errors.Join(err, tmp.Close()); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return os.Rename(tmp.Name(), cache)
}

// mapFiles maps a pack on its first lookup. A pack another writer merged away
// since the listing is simply not consulted; its objects moved to a pack the git
// child can see.
func (p *managedPack) mapFiles() error {
	if p.index != nil || p.gone {
		return nil
	}
	index, err := mapFile(p.path + packIndexExt)
	if errors.Is(err, os.ErrNotExist) {
		p.gone = true
		return nil
	}
	if err != nil {
		return err
	}
	if len(index) < indexHeaderSize+2*sha1.Size || !bytes.Equal(index[:len(indexMagic)], indexMagic) {
		return errors.Join(fmt.Errorf("malformed pack index %s", p.path), syscall.Munmap(index))
	}
	data, err := mapFile(p.path + packFileExt)
	if errors.Is(err, os.ErrNotExist) {
		p.gone = true
		return syscall.Munmap(index)
	}
	if err != nil {
		return errors.Join(err, syscall.Munmap(index))
	}
	p.index, p.data = index, data
	p.cleanup = runtime.AddCleanup(p, func(mappings [2][]byte) {
		_ = syscall.Munmap(mappings[0])
		_ = syscall.Munmap(mappings[1])
	}, [2][]byte{index, data})
	return nil
}

func (p *managedPack) unmap() error {
	if p.index == nil {
		return nil
	}
	p.cleanup.Stop()
	err := errors.Join(syscall.Munmap(p.index), syscall.Munmap(p.data))
	p.index, p.data = nil, nil
	return err
}

func mapFile(path string) (_ []byte, resultErr error) {
	file, err := os.Open(path) //nolint:gosec // G304: a path this repository's object directory names.
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED) //nolint:gosec // G115: a descriptor fits an int.
}

func (p *managedPack) find(hash plumbing.Hash) (int64, bool) {
	if p.index == nil {
		return 0, false
	}
	fanout := p.index[len(indexMagic):indexHeaderSize]
	high := int(binary.BigEndian.Uint32(fanout[int(hash[0])*4:]))
	low := 0
	if hash[0] > 0 {
		low = int(binary.BigEndian.Uint32(fanout[(int(hash[0])-1)*4:]))
	}
	count := int(binary.BigEndian.Uint32(fanout[255*4:]))
	ids := p.index[indexHeaderSize:]
	for low < high {
		middle := int(uint(low+high) >> 1)
		switch bytes.Compare(ids[middle*sha1.Size:(middle+1)*sha1.Size], hash[:]) {
		case 0:
			offsets := ids[count*(sha1.Size+4):]
			return int64(binary.BigEndian.Uint32(offsets[middle*4:])), true
		case -1:
			low = middle + 1
		default:
			high = middle
		}
	}
	return 0, false
}

func (p *managedPack) object(offset int64) (plumbing.EncodedObject, error) {
	if offset < packHeaderSize || offset >= int64(len(p.data)) {
		return nil, fmt.Errorf("offset %d outside the pack", offset)
	}
	reader := bytes.NewReader(p.data[offset:])
	first, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	typ := plumbing.ObjectType(first >> 4 & 0x07)
	if typ != plumbing.CommitObject && typ != plumbing.TreeObject && typ != plumbing.BlobObject && typ != plumbing.TagObject {
		return nil, fmt.Errorf("unexpected object type %s", typ)
	}
	size := int64(first & 0x0f)
	for shift := 4; first&0x80 != 0; shift += 7 {
		if first, err = reader.ReadByte(); err != nil {
			return nil, err
		}
		size |= int64(first&0x7f) << shift
	}
	inflater, err := gitsync.GetZlibReader(reader)
	defer gitsync.PutZlibReader(inflater)
	if err != nil {
		return nil, err
	}
	value := &plumbing.MemoryObject{}
	value.SetType(typ)
	value.SetSize(size)
	writer, err := value.Writer()
	if err != nil {
		return nil, err
	}
	buffer := gitsync.GetByteSlice()
	defer gitsync.PutByteSlice(buffer)
	written, copyErr := io.CopyBuffer(writer, io.LimitReader(inflater.Reader, size+1), *buffer)
	if err := errors.Join(copyErr, writer.Close()); err != nil {
		return nil, err
	}
	if written != size {
		return nil, fmt.Errorf("object inflated to %d bytes, header says %d", written, size)
	}
	return value, nil
}
