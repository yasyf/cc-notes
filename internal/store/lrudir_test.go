package store

import (
	"slices"
	"sync"
	"testing"
)

func TestLRUDirHitPromotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		hits []string
		want []string
	}{
		{"no hit evicts the oldest write", nil, []string{"b", "c", "d"}},
		{"a hit on the oldest keeps it", []string{"a"}, []string{"a", "c", "d"}},
		{"later hits rank above earlier ones", []string{"b", "a"}, []string{"a", "b", "d"}},
		{"a hit on an unknown name changes nothing", []string{"z"}, []string{"b", "c", "d"}},
		{"unknown hits cannot displace a real hit", []string{"a", "w", "x", "y", "z"}, []string{"a", "c", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &lruDir{capacity: 3, dir: t.TempDir()}
			t.Cleanup(func() { _ = l.close() })
			for _, name := range []string{"a", "b", "c"} {
				l.write(name, []byte(name))
			}
			for _, name := range tc.hits {
				l.touch(name)
			}
			l.write("d", []byte("d"))
			if got := slices.Sorted(slices.Values(l.names())); !slices.Equal(got, tc.want) {
				t.Fatalf("entries = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLRUDirRacingFirstWritesEvictTheOldestPersistedEntries(t *testing.T) {
	dir := t.TempDir()
	seed := &lruDir{capacity: 4, dir: dir}
	for _, name := range []string{"a", "b", "c", "d"} {
		seed.write(name, []byte(name))
	}
	if err := seed.close(); err != nil {
		t.Fatal(err)
	}
	l := &lruDir{capacity: 4, dir: dir}
	t.Cleanup(func() { _ = l.close() })
	var wg sync.WaitGroup
	for _, name := range []string{"w", "x", "y"} {
		wg.Go(func() { l.write(name, []byte(name)) })
	}
	wg.Wait()
	if got, want := l.names(), []string{"d", "w", "x", "y"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func cacheEntryPath(t *testing.T, l *lruDir, name string) string {
	t.Helper()
	f, ok := l.open(name)
	if !ok {
		t.Fatalf("cache entry %s missing", name)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
