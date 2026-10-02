package store

import (
	"bytes"
	"io"
	"testing"
)

func TestOpenRelevantCache(t *testing.T) {
	s := &Store{relevant: &lruDir{capacity: relevantCacheCap, dir: t.TempDir()}}
	s.WriteRelevantCache("present", []byte("header\nout{}"))
	for _, tc := range []struct {
		name string
		key  string
		want []byte
		ok   bool
	}{
		{"written entry", "present", []byte("header\nout{}"), true},
		{"missing entry", "absent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := s.OpenRelevantCache(tc.key)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if !ok {
				return
			}
			defer func() { _ = f.Close() }()
			got, err := io.ReadAll(f)
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("read = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
