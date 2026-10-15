package store

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &lruDir{capacity: 3, dir: t.TempDir()}
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

func TestLRUDirRacingFirstRecordsEvictTheOldestSeededEntries(t *testing.T) {
	dir := t.TempDir()
	oldest := time.Now().Add(-time.Hour)
	for i, name := range []string{"a", "b", "c", "d", "w", "x", "y"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		at := oldest.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("age %s: %v", name, err)
		}
	}
	l := &lruDir{capacity: 4, dir: dir}

	var wg sync.WaitGroup
	for _, name := range []string{"w", "x", "y"} {
		wg.Go(func() { l.record(name) })
	}
	wg.Wait()

	if got, want := slices.Sorted(slices.Values(l.names())), []string{"d", "w", "x", "y"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}
