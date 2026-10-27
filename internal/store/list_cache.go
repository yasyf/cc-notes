package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/model"
)

const listCacheVersion = 1

var executableIdentity = sync.OnceValues(func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	ctime, inode := gitobj.StatIdentity(info)
	return fmt.Sprintf("%s %s %d %d %d %d", exe, version.Version, info.Size(), info.ModTime().UnixNano(), ctime, inode), nil
})

// CachedList runs compute over a view of s pinned once to kind's refs, reusing
// the stored result for the same tips, variant, fold generation, records
// repository, and binary; any tip change is a new key. variant must encode
// every other input compute reads. Errors and results with skipped ops are
// never stored, and an unidentifiable executable is an error.
func CachedList[T model.Snapshot](ctx context.Context, s *Store, kind model.Kind, variant []byte, compute func(*Store) ([]T, error)) ([]T, error) {
	pinned, err := s.PinnedKinds(ctx, kind)
	if err != nil {
		return nil, err
	}
	identity, err := executableIdentity()
	if err != nil {
		return nil, fmt.Errorf("identify executable for the %s list cache: %w", kind, err)
	}
	name := pinned.listCacheName(kind, variant, foldCacheGeneration, identity)
	if data, ok := s.relevant.read(name); ok {
		var cached []T
		if json.Unmarshal(data, &cached) == nil {
			s.relevant.touch(name)
			return cached, nil
		}
	}
	out, err := compute(pinned)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(out, func(v T) bool { return v.Meta().SkippedOps > 0 }) {
		return out, nil
	}
	if data, err := json.Marshal(out); err == nil {
		s.relevant.write(name, data)
	}
	return out, nil
}

func (s *Store) listCacheName(kind model.Kind, variant []byte, generation, identity string) string {
	entries := directChildren(refs.Root(kind), s.pins)
	slices.SortFunc(entries, func(a, b tipEntry) int { return strings.Compare(a.ref, b.ref) })
	key := fmt.Appendf(nil, "list %d\n%s\n%d %s\n%s\n%s\n%d %s\n", listCacheVersion, generation, len(identity), identity, s.recordsCommonDir, kind, len(variant), variant)
	for _, e := range entries {
		key = fmt.Appendf(key, "%s %s\n", e.ref, e.tip)
	}
	sum := sha256.Sum256(key)
	return "list-" + hex.EncodeToString(sum[:])
}
