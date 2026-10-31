package store

import (
	"bytes"
	"strconv"
	"testing"
)

func mutateCacheHint(t *testing.T, l *lruDir, name string, change func(*cacheHint)) {
	t.Helper()
	entry, ok := l.entry(name)
	if !ok {
		t.Fatalf("entry %s missing before hint mutation", name)
	}
	file, err := l.hintFile(cacheWrite)
	if err != nil {
		t.Fatal(err)
	}
	var data [cacheHintRecordSize]byte
	if _, err := file.ReadAt(data[:], cacheHintHeaderSize+int64(entry.slot)*cacheHintRecordSize); err != nil {
		t.Fatal(err)
	}
	hint, err := decodeCacheHint(data[:])
	if err != nil {
		t.Fatal(err)
	}
	change(&hint)
	if err := writeCacheHint(file, entry.slot, hint); err != nil {
		t.Fatal(err)
	}
}

func TestCacheHintBoundedCandidatesPromoteWithinFullWindow(t *testing.T) {
	l := newProcessCache(t, 128)
	names := make([]string, 0, cacheProbeLimit+1)
	for i := range 100000 {
		name := strconv.Itoa(i)
		if cacheNameTag(name)%uint64(l.capacity) == 0 {
			names = append(names, name)
			if len(names) == cacheProbeLimit+1 {
				break
			}
		}
	}
	if len(names) != cacheProbeLimit+1 {
		t.Fatal("bounded collision fixture incomplete")
	}
	for _, name := range names[:cacheProbeLimit] {
		l.write(name, []byte(name))
	}
	l.touch(names[0])
	l.write(names[cacheProbeLimit], []byte("last"))
	if _, ok := l.read(names[1]); ok {
		t.Fatal("oldest candidate survived the full probe window")
	}
	if data, ok := l.read(names[0]); !ok || string(data) != names[0] {
		t.Fatal("candidate-local hit was not promoted")
	}
	if data, ok := l.read(names[cacheProbeLimit]); !ok || string(data) != "last" {
		t.Fatal("new candidate missing")
	}
	if got := len(l.names()); got != cacheProbeLimit {
		t.Fatalf("early candidate eviction: entries=%d capacity=%d", got, l.capacity)
	}
}

func TestCacheHintRoutingCollisionCannotReturnAnotherKey(t *testing.T) {
	l := newProcessCache(t, 1)
	l.write("a", []byte("a"))
	mutateCacheHint(t, l, "a", func(hint *cacheHint) { hint.tag = cacheNameTag("b") })
	if data, ok := l.read("b"); ok {
		t.Fatalf("routing collision exposed another key: %q", data)
	}
	l.write("b", []byte("b"))
	if data, ok := l.read("b"); !ok || string(data) != "b" {
		t.Fatal("routing collision did not permit correct replacement")
	}
	if _, ok := l.read("a"); ok {
		t.Fatal("overwritten routing collision survived")
	}
}

func TestCacheHintHeaderRollbackDoesNotReusePublication(t *testing.T) {
	dir := t.TempDir()
	l := &lruDir{capacity: 1, dir: dir}
	l.write("a", []byte("old"))
	before, ok := l.entry("a")
	if !ok {
		t.Fatal("old publication missing")
	}
	if err := writeCacheHintRank(l.index, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	fresh := &lruDir{capacity: 1, dir: dir}
	t.Cleanup(func() { _ = fresh.close() })
	fresh.write("a", []byte("new"))
	after, ok := fresh.entry("a")
	if !ok || bytes.Equal(before.token, after.token) || after.rank <= before.rank {
		t.Fatal("rank header rollback reused a publication or candidate rank")
	}
	if data, ok := fresh.readEntry(before); ok {
		t.Fatalf("old hint exposed new publication: %q", data)
	}
	if data, ok := fresh.readEntry(after); !ok || string(data) != "new" {
		t.Fatal("new hint did not bind its publication")
	}
}
