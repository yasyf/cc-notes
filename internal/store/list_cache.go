package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/model"
)

const listCacheVersion = 2

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

// CachedList runs compute over supplied pins, or a view pinned once to kind's refs. Each
// variant, fold generation, records repository, and binary keeps one entry,
// served only while its recorded tips equal the pinned tips and rewritten in
// place when they differ. variant must encode every other input compute reads.
// Errors and results with skipped ops are never stored.
func CachedList[T model.Snapshot](ctx context.Context, s *Store, kind model.Kind, variant []byte, compute func(*Store) ([]T, error)) ([]T, error) {
	pinned := s
	if s.pins == nil {
		var err error
		pinned, err = s.PinnedKinds(ctx, kind)
		if err != nil {
			return nil, err
		}
	}
	return cachedPinnedList(ctx, pinned, kind, variant, compute)
}

func cachedPinnedList[T model.Snapshot](ctx context.Context, pinned *Store, kind model.Kind, variant []byte, compute func(*Store) ([]T, error)) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := pinned.CheckRecords(); err != nil {
		return nil, err
	}
	identity, err := executableIdentity()
	if err != nil {
		return nil, fmt.Errorf("identify executable for the %s list cache: %w", kind, err)
	}
	name := pinned.listCacheName(kind, variant, foldCacheGeneration, identity)
	tips := pinned.listTips(kind)
	if cached, ok := readCachedList[T](pinned.relevant, name, tips); ok {
		return cached, nil
	}
	out, err := compute(pinned)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(out, func(v T) bool { return v.Meta().SkippedOps > 0 }) {
		return out, nil
	}
	if data, err := json.Marshal(out); err == nil {
		pinned.relevant.write(name, slices.Concat([]byte(tips+"\n"), data))
	}
	return out, nil
}

func readCachedList[T model.Snapshot](dir *lruDir, name, tips string) ([]T, bool) {
	f, ok := dir.open(name)
	if !ok {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	if header, err := r.ReadString('\n'); err != nil || header != tips+"\n" {
		return nil, false
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, false
	}
	var cached []T
	if json.Unmarshal(data, &cached) != nil {
		return nil, false
	}
	dir.touch(name)
	return cached, true
}

func (s *Store) listCacheName(kind model.Kind, variant []byte, generation, identity string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "list %d\n%s\n%d %s\n%s\n%s\n%d %s", listCacheVersion, generation, len(identity), identity, s.recordsCommonDir, kind, len(variant), variant))
	return "list-" + hex.EncodeToString(sum[:])
}

func (s *Store) listTips(kind model.Kind) string {
	entries := directChildren(refs.Root(kind), s.pins)
	slices.SortFunc(entries, func(a, b tipEntry) int { return strings.Compare(a.ref, b.ref) })
	var key []byte
	for _, e := range entries {
		key = fmt.Appendf(key, "%s %s\n", e.ref, e.tip)
	}
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}
