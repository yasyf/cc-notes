package gitobj

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: git's pack and index trailers are SHA-1.
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitsync "github.com/go-git/go-git/v5/utils/sync"
)

const (
	// objectBufferLimit bounds the raw content one source scope holds before it
	// writes a pack and keeps going, so a reindex stays bounded.
	objectBufferLimit = 64 << 20
	// packMinObjects is the smallest write worth two pack files over loose ones.
	packMinObjects = 5
	// packTierFanout is how many packs in a tier merge into one in the next.
	packTierFanout = 8
	// packTopTier never merges; git maintenance consumes it.
	packTopTier = 4
	// packTierBase is the largest pack that starts in tier 0; each tier above
	// holds packTierFanout times more.
	packTierBase = 256 << 10
	// rollupMaxBytes keeps a merged pack inside an index's 4-byte offsets.
	rollupMaxBytes = 1 << 30
	// rollupOneIn samples pack checksums, so most writes skip listing a pack
	// directory that can hold thousands of files.
	rollupOneIn = 8
	// packKeepAge bounds the keep file of a writer that died before releasing it.
	packKeepAge = time.Hour

	managedPackPrefix = "ccn"
	packFileExt       = ".pack"
	packKeepExt       = ".keep"
	packHeaderSize    = 12
	indexFanoutSize   = 256 * 4
	indexHeaderSize   = 8 + indexFanoutSize
	midxHeaderSize    = 12
	midxChunkRowSize  = 12
	midxFile          = "multi-pack-index"
	midxChainDir      = "multi-pack-index.d"
)

var (
	packMagic  = []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2}
	indexMagic = []byte{0xff, 't', 'O', 'c', 0, 0, 0, 2}
)

// objectBuffer holds the objects one source scope wrote until the scope succeeds.
type objectBuffer struct {
	objects map[plumbing.Hash]plumbing.EncodedObject
	size    int64
	roots   []plumbing.Hash
	hold    bool
}

func newObjectBuffer() *objectBuffer {
	return &objectBuffer{objects: make(map[plumbing.Hash]plumbing.EncodedObject)}
}

func (b *objectBuffer) all() []plumbing.EncodedObject {
	kept := make([]plumbing.EncodedObject, 0, len(b.objects))
	for _, value := range b.objects {
		kept = append(kept, value)
	}
	return kept
}

// reachable returns the buffered closure of the roots, or everything without roots.
func (b *objectBuffer) reachable() ([]plumbing.EncodedObject, error) {
	if len(b.roots) == 0 {
		return b.all(), nil
	}
	seen := make(map[plumbing.Hash]struct{}, len(b.objects))
	stack := slices.Clone(b.roots)
	kept := make([]plumbing.EncodedObject, 0, len(b.objects))
	for len(stack) > 0 {
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		value, buffered := b.objects[hash]
		if _, visited := seen[hash]; visited || !buffered {
			continue
		}
		seen[hash] = struct{}{}
		kept = append(kept, value)
		switch value.Type() {
		case plumbing.CommitObject:
			var commit object.Commit
			if err := commit.Decode(value); err != nil {
				return nil, fmt.Errorf("decode buffered commit %s: %w", hash, err)
			}
			stack = append(append(stack, commit.TreeHash), commit.ParentHashes...)
		case plumbing.TreeObject:
			var tree object.Tree
			if err := tree.Decode(value); err != nil {
				return nil, fmt.Errorf("decode buffered tree %s: %w", hash, err)
			}
			for _, entry := range tree.Entries {
				stack = append(stack, entry.Hash)
			}
		}
	}
	return kept, nil
}

func (s *objectStorage) bufferObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	hash := o.Hash()
	if _, found := s.buffer.objects[hash]; found {
		return hash, nil
	}
	s.buffer.objects[hash] = o
	s.buffer.size += o.Size()
	if s.buffer.size < objectBufferLimit {
		return hash, nil
	}
	if err := s.writeObjects(s.buffer.all()); err != nil {
		return plumbing.ZeroHash, err
	}
	s.buffer.objects, s.buffer.size = make(map[plumbing.Hash]plumbing.EncodedObject), 0
	return hash, nil
}

// flushBuffer makes the scope's reachable objects visible before its caller can
// publish a ref to them.
func (s *objectStorage) flushBuffer() error {
	objects, err := s.buffer.reachable()
	if err != nil {
		return err
	}
	return s.writeObjects(objects)
}

func (s *objectStorage) writeObjects(objects []plumbing.EncodedObject) error {
	if len(objects) < packMinObjects {
		for _, value := range objects {
			if _, err := s.writeLoose(value); err != nil {
				return err
			}
		}
		return nil
	}
	pack, entries, err := encodePack(objects)
	if err != nil {
		return err
	}
	checksum := pack[len(pack)-sha1.Size:]
	dir := filepath.Join(s.root, packDir)
	tmp, err := os.CreateTemp(dir, "tmp_pack_")
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // G301: git creates the pack directory under the process umask.
			return err
		}
		tmp, err = os.CreateTemp(dir, "tmp_pack_")
	}
	if err != nil {
		return err
	}
	_, err = tmp.Write(pack)
	tier := packTier(int64(len(pack)))
	if err := publishPack(dir, tmp, tier, checksum, entries, s.buffer.hold, false, err); err != nil {
		return err
	}
	if s.buffer.hold {
		s.held = append(s.held, filepath.Join(dir, packStem(tier, checksum)+packKeepExt))
	}
	if int(checksum[0])%rollupOneIn == 0 {
		// best-effort: the pack is already visible, the next sampled write retries,
		// and `cc-notes gc` reports a merge that keeps failing.
		_ = s.rollupPacks()
	}
	return nil
}

func packTier(size int64) int {
	tier := 0
	for limit := int64(packTierBase); size > limit && tier < packTopTier; limit *= packTierFanout {
		tier++
	}
	return tier
}

type packEntry struct {
	hash   plumbing.Hash
	crc    uint32
	offset uint32
}

func encodePack(objects []plumbing.EncodedObject) ([]byte, []packEntry, error) {
	slices.SortFunc(objects, func(a, b plumbing.EncodedObject) int {
		left, right := a.Hash(), b.Hash()
		return bytes.Compare(left[:], right[:])
	})
	var pack bytes.Buffer
	pack.Write(packMagic)
	if err := binary.Write(&pack, binary.BigEndian, uint32(len(objects))); err != nil { //nolint:gosec // G115: a scope holds far fewer than 2^32 objects.
		return nil, nil, err
	}
	compressor := gitsync.GetZlibWriter(&pack)
	defer gitsync.PutZlibWriter(compressor)
	entries := make([]packEntry, 0, len(objects))
	for _, value := range objects {
		offset := pack.Len()
		if offset > 1<<31-1 {
			return nil, nil, fmt.Errorf("pack offset %d needs a 64-bit index", offset)
		}
		writeObjectHeader(&pack, value.Type(), value.Size())
		compressor.Reset(&pack)
		reader, err := value.Reader()
		if err != nil {
			return nil, nil, err
		}
		_, copyErr := io.Copy(compressor, reader)
		if err := errors.Join(copyErr, reader.Close(), compressor.Close()); err != nil {
			return nil, nil, err
		}
		entries = append(entries, packEntry{hash: value.Hash(), crc: crc32.ChecksumIEEE(pack.Bytes()[offset:]), offset: uint32(offset)}) //nolint:gosec // G115: bounded above.
	}
	sum := sha1.Sum(pack.Bytes()) //nolint:gosec // G401: git's pack trailer is SHA-1.
	pack.Write(sum[:])
	return pack.Bytes(), entries, nil
}

func writeObjectHeader(w *bytes.Buffer, typ plumbing.ObjectType, size int64) {
	first := byte(typ)<<4 | byte(size&0x0f) //nolint:gosec // G115: git object types fit three bits.
	size >>= 4
	for size > 0 {
		w.WriteByte(first | 0x80)
		first = byte(size & 0x7f)
		size >>= 7
	}
	w.WriteByte(first)
}

func encodeIndex(entries []packEntry, packChecksum []byte) []byte {
	slices.SortFunc(entries, func(a, b packEntry) int { return bytes.Compare(a.hash[:], b.hash[:]) })
	index := bytes.NewBuffer(make([]byte, 0, indexHeaderSize+len(entries)*(sha1.Size+8)+2*sha1.Size))
	index.Write(indexMagic)
	var fanout [256]uint32
	for _, entry := range entries {
		fanout[entry.hash[0]]++
	}
	total := uint32(0)
	var word [4]byte
	for _, count := range fanout {
		total += count
		binary.BigEndian.PutUint32(word[:], total)
		index.Write(word[:])
	}
	for _, entry := range entries {
		index.Write(entry.hash[:])
	}
	for _, entry := range entries {
		binary.BigEndian.PutUint32(word[:], entry.crc)
		index.Write(word[:])
	}
	for _, entry := range entries {
		binary.BigEndian.PutUint32(word[:], entry.offset)
		index.Write(word[:])
	}
	index.Write(packChecksum)
	sum := sha1.Sum(index.Bytes()) //nolint:gosec // G401: git's index trailer is SHA-1.
	index.Write(sum[:])
	return index.Bytes()
}

func packStem(tier int, checksum []byte) string {
	return fmt.Sprintf("%s%d-%x", managedPackPrefix, tier, checksum)
}

// publishPack closes tmp and renames it into place, any keep file before the
// index, since the index is what makes git see a pack. A kept pack survives
// `git repack -a -d` while no ref reaches it yet. A durable publish reaches disk
// before it returns, because its caller deletes the packs it replaces. writeErr
// carries a failed write to tmp.
func publishPack(dir string, tmp *os.File, tier int, checksum []byte, entries []packEntry, keep, durable bool, writeErr error) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.Remove(tmp.Name()))
		}
	}()
	if err := errors.Join(writeErr, syncIf(durable, tmp), tmp.Chmod(0o444), tmp.Close()); err != nil {
		return err
	}
	stem := filepath.Join(dir, packStem(tier, checksum))
	indexTmp, err := os.CreateTemp(dir, "tmp_idx_")
	if err != nil {
		return err
	}
	_, err = indexTmp.Write(encodeIndex(entries, checksum))
	if err := errors.Join(err, syncIf(durable, indexTmp), indexTmp.Chmod(0o444), indexTmp.Close()); err != nil {
		return errors.Join(err, os.Remove(indexTmp.Name()))
	}
	if keep {
		if err := os.WriteFile(stem+packKeepExt, nil, 0o600); err != nil {
			return errors.Join(err, os.Remove(indexTmp.Name()))
		}
	}
	if err := os.Rename(tmp.Name(), stem+packFileExt); err != nil {
		return errors.Join(err, os.Remove(indexTmp.Name()))
	}
	if err := os.Rename(indexTmp.Name(), stem+packIndexExt); err != nil {
		return err
	}
	if !durable {
		return nil
	}
	directory, err := os.Open(dir) //nolint:gosec // G304: this repository's pack directory.
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func syncIf(durable bool, file *os.File) error {
	if !durable {
		return nil
	}
	return file.Sync()
}

func managedPackStem(name, ext string) (int, string, bool) {
	stem, ok := strings.CutSuffix(name, ext)
	if !ok {
		return 0, "", false
	}
	rest, ok := strings.CutPrefix(stem, managedPackPrefix)
	if !ok || len(rest) < 2 || rest[0] < '0' || rest[0] > '0'+packTopTier || rest[1] != '-' {
		return 0, "", false
	}
	return int(rest[0] - '0'), stem, true
}

// ReleaseHeldPacks drops the keep files of the packs held scopes wrote, once
// their refs are published or abandoned. The caller owns every hold on this
// handle, which a publication does by holding the publication lock around both.
func (r *Repo) ReleaseHeldPacks() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result error
	for _, path := range r.storage.held {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	r.storage.held = nil
	return result
}

// RollupPacks merges every full tier of the packs source scopes wrote and reports
// what a write's best-effort attempt would have dropped.
func (r *Repo) RollupPacks() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storage.rollupPacks()
}

// rollupPacks merges every full tier into the next one up, bounding the packs
// these writes leave behind. A pack with a fresh keep file waits for its writer.
// A pack a multi-pack index lists stays where it is: removing it would leave the
// index naming a missing pack, and the maintenance that wrote the index is what
// consumes it.
func (s *objectStorage) rollupPacks() error {
	dir := filepath.Join(s.root, packDir)
	listing, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	listed, err := midxPackNames(dir)
	if err != nil {
		return err
	}
	var result error
	var tiers [packTopTier][]string
	sizes := make(map[string]int64)
	kept := make(map[string]struct{})
	for _, entry := range listing {
		name := entry.Name()
		if _, stem, ok := managedPackStem(name, packKeepExt); ok {
			fresh, err := retireKeep(filepath.Join(dir, stem+packKeepExt), entry)
			result = errors.Join(result, err)
			if fresh {
				kept[stem] = struct{}{}
			}
			continue
		}
		if _, stem, ok := managedPackStem(name, packFileExt); ok {
			info, err := entry.Info()
			if err == nil {
				sizes[stem] = info.Size()
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		tier, stem, ok := managedPackStem(name, packIndexExt)
		if _, indexed := listed[name]; ok && !indexed && tier < packTopTier {
			tiers[tier] = append(tiers[tier], stem)
		}
	}
	for tier, stems := range tiers {
		stems = slices.DeleteFunc(stems, func(stem string) bool {
			_, held := kept[stem]
			return held
		})
		if len(stems) < packTierFanout {
			continue
		}
		selected := make([]string, 0, len(stems))
		total := int64(0)
		for _, stem := range stems {
			if total += sizes[stem]; total > rollupMaxBytes {
				break
			}
			selected = append(selected, stem)
		}
		if len(selected) < 2 {
			continue
		}
		merged, err := mergePacks(dir, tier+1, selected)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.Join(result, fmt.Errorf("merge tier %d packs: %w", tier, err))
		}
		if info, err := os.Stat(filepath.Join(dir, merged+packFileExt)); err == nil && tier+1 < packTopTier {
			sizes[merged] = info.Size()
			tiers[tier+1] = append(tiers[tier+1], merged) //nolint:gosec // G602: guarded by the tier bound above.
		}
	}
	return result
}

func retireKeep(path string, entry os.DirEntry) (bool, error) {
	info, err := entry.Info()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Since(info.ModTime()) < packKeepAge {
		return true, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

// midxPackNames returns the pack index names every multi-pack index in dir lists.
func midxPackNames(dir string) (map[string]struct{}, error) {
	paths := []string{filepath.Join(dir, midxFile)}
	chained, err := filepath.Glob(filepath.Join(dir, midxChainDir, "*.midx"))
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{})
	for _, path := range append(paths, chained...) {
		if err := readMidxPackNames(path, names); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}
	return names, nil
}

func readMidxPackNames(path string, names map[string]struct{}) (resultErr error) {
	file, err := os.Open(path) //nolint:gosec // G304: a path this repository's object directory names.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var header [midxHeaderSize]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return err
	}
	if string(header[:4]) != "MIDX" {
		return errors.New("bad multi-pack index signature")
	}
	table := make([]byte, (int(header[6])+1)*midxChunkRowSize)
	if _, err := file.ReadAt(table, midxHeaderSize); err != nil {
		return err
	}
	for i := 0; i+midxChunkRowSize < len(table); i += midxChunkRowSize {
		if string(table[i:i+4]) != "PNAM" { //nolint:gosec // G602: the loop bound leaves a full row.
			continue
		}
		start := int64(binary.BigEndian.Uint64(table[i+4:]))                //nolint:gosec // G115: a file offset.
		end := int64(binary.BigEndian.Uint64(table[i+midxChunkRowSize+4:])) //nolint:gosec // G115, G602: a file offset, and the loop stops one row early.
		chunk := make([]byte, end-start)
		if _, err := file.ReadAt(chunk, start); err != nil {
			return err
		}
		for name := range strings.SplitSeq(string(chunk), "\x00") {
			if name != "" {
				names[name] = struct{}{}
			}
		}
		return nil
	}
	return errors.New("multi-pack index has no pack names")
}

// mergePacks copies each source's compressed entries verbatim into one durable
// pack in tier, then removes the sources. These packs carry no deltas, so an entry
// means the same at any offset. os.ErrNotExist means another writer merged a
// source first.
func mergePacks(dir string, tier int, stems []string) (string, error) {
	sources := make([]mergeSource, 0, len(stems))
	seen := make(map[plumbing.Hash]struct{})
	for _, stem := range stems {
		source, err := readMergeSource(filepath.Join(dir, stem), seen)
		if err != nil {
			return "", err
		}
		sources = append(sources, source)
	}
	tmp, err := os.CreateTemp(dir, "tmp_pack_")
	if err != nil {
		return "", err
	}
	checksum, entries, writeErr := writeMergedPack(tmp, sources)
	if err := publishPack(dir, tmp, tier, checksum, entries, false, true, writeErr); err != nil {
		return "", err
	}
	merged := packStem(tier, checksum)
	var result error
	for _, stem := range stems {
		if stem == merged {
			continue
		}
		for _, ext := range []string{packIndexExt, packFileExt} {
			if err := os.Remove(filepath.Join(dir, stem+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}
	return merged, result
}

type mergeSource struct {
	stem    string
	entries []mergeEntry
}

type mergeEntry struct {
	hash   plumbing.Hash
	crc    uint32
	offset int64
	end    int64
	keep   bool
}

func readMergeSource(stem string, seen map[plumbing.Hash]struct{}) (mergeSource, error) {
	index, err := os.ReadFile(stem + packIndexExt) //nolint:gosec // G304: a path this repository's object directory names.
	if err != nil {
		return mergeSource{}, err
	}
	info, err := os.Stat(stem + packFileExt)
	if err != nil {
		return mergeSource{}, err
	}
	if len(index) < indexHeaderSize+2*sha1.Size || !bytes.Equal(index[:len(indexMagic)], indexMagic) {
		return mergeSource{}, fmt.Errorf("malformed pack index %s", stem)
	}
	count := int(binary.BigEndian.Uint32(index[indexHeaderSize-4:]))
	if len(index) != indexHeaderSize+count*(sha1.Size+8)+2*sha1.Size {
		return mergeSource{}, fmt.Errorf("pack index %s has a 64-bit table or the wrong size", stem)
	}
	ids := index[indexHeaderSize:]
	crcs := ids[count*sha1.Size:]
	offsets := crcs[count*4:]
	source := mergeSource{stem: stem, entries: make([]mergeEntry, count)}
	for i := range source.entries {
		entry := &source.entries[i]
		copy(entry.hash[:], ids[i*sha1.Size:])
		entry.crc = binary.BigEndian.Uint32(crcs[i*4:])
		entry.offset = int64(binary.BigEndian.Uint32(offsets[i*4:]))
		if _, duplicate := seen[entry.hash]; !duplicate {
			entry.keep = true
			seen[entry.hash] = struct{}{}
		}
	}
	slices.SortFunc(source.entries, func(a, b mergeEntry) int { return int(a.offset - b.offset) })
	for i := range source.entries {
		source.entries[i].end = info.Size() - sha1.Size
		if i+1 < len(source.entries) {
			source.entries[i].end = source.entries[i+1].offset
		}
	}
	return source, nil
}

func writeMergedPack(tmp *os.File, sources []mergeSource) ([]byte, []packEntry, error) {
	sum := sha1.New() //nolint:gosec // G401: git's pack trailer is SHA-1.
	buffered := bufio.NewWriterSize(tmp, 1<<20)
	out := io.MultiWriter(buffered, sum)
	total := 0
	for _, source := range sources {
		for _, entry := range source.entries {
			if entry.keep {
				total++
			}
		}
	}
	var header [packHeaderSize]byte
	copy(header[:], packMagic)
	binary.BigEndian.PutUint32(header[len(packMagic):], uint32(total)) //nolint:gosec // G115: bounded by rollupMaxBytes.
	if _, err := out.Write(header[:]); err != nil {
		return nil, nil, err
	}
	entries := make([]packEntry, 0, total)
	offset := int64(packHeaderSize)
	for _, source := range sources {
		file, err := os.Open(source.stem + packFileExt) //nolint:gosec // G304: a path this repository's object directory names.
		if err != nil {
			return nil, nil, err
		}
		for _, entry := range source.entries {
			if !entry.keep {
				continue
			}
			if _, err := io.Copy(out, io.NewSectionReader(file, entry.offset, entry.end-entry.offset)); err != nil {
				return nil, nil, errors.Join(err, file.Close())
			}
			entries = append(entries, packEntry{hash: entry.hash, crc: entry.crc, offset: uint32(offset)}) //nolint:gosec // G115: bounded by rollupMaxBytes.
			offset += entry.end - entry.offset
		}
		if err := file.Close(); err != nil {
			return nil, nil, err
		}
	}
	checksum := sum.Sum(nil)
	if _, err := buffered.Write(checksum); err != nil {
		return nil, nil, err
	}
	return checksum, entries, buffered.Flush()
}
