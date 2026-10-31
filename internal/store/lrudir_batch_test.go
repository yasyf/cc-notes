package store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestLRUDirRequestedEntries(t *testing.T) {
	l := newProcessCache(t, 4096)
	var names []string
	for i := range 260 {
		name := fmt.Sprintf("key-%03d", i)
		l.write(name, []byte(name))
		if i%2 == 0 {
			names = append(names, name)
		}
	}
	names = append(names, "missing", names[0])
	entries := l.entries(t.Context(), names)
	if len(entries) != 130 {
		t.Fatalf("requested entries = %d, want 130", len(entries))
	}
	for i := range 260 {
		name := fmt.Sprintf("key-%03d", i)
		entry, ok := entries[name]
		if ok != (i%2 == 0) {
			t.Fatalf("entry %s present = %v", name, ok)
		}
		if ok {
			if data, hit := l.readEntry(entry); !hit || string(data) != name {
				t.Fatalf("entry %s = %q, %v", name, data, hit)
			}
		}
	}
	if len(l.hits) != 0 {
		t.Fatal("prefetch promoted entries")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if entries := l.entries(ctx, names); len(entries) != 0 {
		t.Fatalf("cancelled lookup = %v", entries)
	}
}

func TestLRUDirRequestedEntriesRejectWrongKey(t *testing.T) {
	l := newProcessCache(t, 2)
	l.write("a", []byte("a"))
	l.write("b", []byte("b"))
	mutateCacheHint(t, l, "a", func(hint *cacheHint) { hint.tag ^= 1 })
	entries := l.entries(t.Context(), []string{"a", "b"})
	if _, ok := entries["a"]; ok || len(entries) != 1 {
		t.Fatalf("wrong-key metadata accepted: %v", entries)
	}
	if data, ok := l.readEntry(entries["b"]); !ok || string(data) != "b" {
		t.Fatalf("valid metadata = %q, %v", data, ok)
	}
}

func TestLRUDirRequestedEntrySurvivesOnlyItsPublication(t *testing.T) {
	l := newProcessCache(t, 1)
	want := bytes.Repeat([]byte("old"), cacheChunkSize)
	l.write("old", want)
	entry := l.entries(t.Context(), []string{"old"})["old"]
	reader, ok := l.openEntry(entry)
	if !ok {
		t.Fatal("requested stream missing")
	}
	defer func() { _ = reader.Close() }()
	l.write("new", []byte("new"))
	if data, ok := l.readEntry(entry); ok {
		t.Fatalf("stale metadata read replacement: %q", data)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(data, want) {
		t.Fatalf("held requested stream = %d bytes, %v", len(data), err)
	}
}

func TestLRUDirFoldBatchPromotion(t *testing.T) {
	dir := t.TempDir()
	seed := newFoldCache(dir, 2)
	for _, name := range []string{"a", "b"} {
		seed.put(model.SHA(name), model.Note{ID: model.EntityID(name), Head: model.SHA(name)}, 1, 0)
	}
	if err := seed.close(); err != nil {
		t.Fatal(err)
	}
	c := newFoldCache(dir, 2)
	t.Cleanup(func() { _ = c.close() })
	entries := c.entries(t.Context(), []string{"a", "b"})
	if len(c.hits) != 0 {
		t.Fatal("prefetch promoted entries")
	}
	if _, ok := c.getEntry("a", entries["a"]); !ok || len(c.hits) != 0 {
		t.Fatal("pre-write decode missed or promoted")
	}
	c.put("c", model.Note{ID: "c", Head: "c"}, 1, 0)
	if names := c.names(); !slices.Equal(names, []string{"b", "c"}) {
		t.Fatalf("pre-write batch changed eviction: %v", names)
	}
	entries = c.entries(t.Context(), []string{"b"})
	if _, ok := c.getEntry("b", entries["b"]); !ok {
		t.Fatal("active batch hit missing")
	}
	c.put("d", model.Note{ID: "d", Head: "d"}, 1, 0)
	if names := c.names(); !slices.Equal(names, []string{"b", "d"}) {
		t.Fatalf("successful batch hit was not promoted: %v", names)
	}
	c.write("b", []byte("invalid fold"))
	entries = c.entries(t.Context(), []string{"b"})
	if _, ok := c.getEntry("b", entries["b"]); ok || len(c.hits) != 0 {
		t.Fatal("invalid semantic payload hit or promoted")
	}
}
