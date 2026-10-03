package store

import (
	"slices"
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
