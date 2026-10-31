package store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/model"
)

func TestLRUDirPublicationBatchRowBound(t *testing.T) {
	c := newFoldCache(t.TempDir(), cachePublicationRows*2)
	t.Cleanup(func() { _ = c.close() })
	b := newFoldPublicationBatch(t.Context(), c)
	defer b.discard()
	for i := range cachePublicationRows - 1 {
		b.add(&cachePublication{name: strconv.Itoa(i), data: []byte("value")})
	}
	if _, ok := c.read("0"); ok {
		t.Fatal("queued value became a cache hit")
	}
	b.add(&cachePublication{name: "last", data: []byte("last")})
	if got := len(c.names()); got != cachePublicationRows || len(b.values) != 0 || b.bytes != 0 {
		t.Fatalf("row flush: entries=%d queued=%d bytes=%d", got, len(b.values), b.bytes)
	}
	for _, value := range b.values[:cap(b.values)] {
		if value.name != "" || value.data != nil {
			t.Fatal("flushed values retained payload references")
		}
	}
	b.add(&cachePublication{name: "tail", data: []byte("tail")})
	b.flush()
	if data, ok := c.read("tail"); !ok || string(data) != "tail" {
		t.Fatalf("tail = %q, %v", data, ok)
	}
}

func TestLRUDirPublicationBatchFlushDuration(t *testing.T) {
	for _, size := range []int{13, cachePublicationBytes / cachePublicationRows} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c := newFoldCache(t.TempDir(), cachePublicationRows+1)
			t.Cleanup(func() { _ = c.close() })
			c.write("prime", []byte("prime"))
			b := newFoldPublicationBatch(t.Context(), c)
			defer b.discard()
			data := bytes.Repeat([]byte("x"), size)
			for i := range cachePublicationRows - 1 {
				b.add(&cachePublication{name: strconv.Itoa(i), data: data})
			}
			started := time.Now()
			b.add(&cachePublication{name: "last", data: data})
			elapsed := time.Since(started)
			if len(b.values) != 0 || b.bytes != 0 {
				t.Fatalf("chunk did not flush: queued=%d bytes=%d", len(b.values), b.bytes)
			}
			for _, name := range []string{"0", "last"} {
				got, ok := c.read(name)
				if !ok || !bytes.Equal(got, data) {
					t.Fatalf("published %s = %d bytes, %v", name, len(got), ok)
				}
			}
			t.Logf("publication_rows=%d logical_bytes=%d flush_elapsed_ns=%d", cachePublicationRows, size*cachePublicationRows, elapsed.Nanoseconds())
		})
	}
}

func TestLRUDirPublicationBatchByteBoundAndOversize(t *testing.T) {
	c := newFoldCache(t.TempDir(), 3)
	t.Cleanup(func() { _ = c.close() })
	b := newFoldPublicationBatch(t.Context(), c)
	defer b.discard()
	data := bytes.Repeat([]byte("x"), cachePublicationBytes/2+1)
	b.add(&cachePublication{name: "a", data: data})
	b.add(&cachePublication{name: "b", data: data})
	if _, ok := c.entry("a"); !ok || len(b.values) != 1 || b.bytes != len(data) {
		t.Fatalf("byte flush: queued=%d bytes=%d first=%v", len(b.values), b.bytes, ok)
	}
	if _, ok := c.entry("b"); ok {
		t.Fatal("next chunk published before its flush")
	}
	large := bytes.Repeat([]byte("z"), cachePublicationBytes+1)
	b.add(&cachePublication{name: "large", data: large})
	if len(b.values) != 0 || b.bytes != 0 {
		t.Fatalf("oversize retained queue: %d, %d", len(b.values), b.bytes)
	}
	if _, ok := c.entry("b"); !ok {
		t.Fatal("queued predecessor was not flushed before oversize")
	}
	reader, ok := c.open("large")
	if !ok {
		t.Fatal("oversize publication missing")
	}
	defer func() { _ = reader.Close() }()
	n, err := io.Copy(io.Discard, reader)
	if err != nil || n != int64(len(large)) {
		t.Fatalf("oversize stream = %d, %v", n, err)
	}
}

func TestLRUDirPublicationBatchCancelDiscards(t *testing.T) {
	c := newFoldCache(t.TempDir(), 1)
	t.Cleanup(func() { _ = c.close() })
	ctx, cancel := context.WithCancel(t.Context())
	b := newFoldPublicationBatch(ctx, c)
	b.add(&cachePublication{name: "pending", data: []byte("pending")})
	cancel()
	b.flush()
	if len(b.values) != 0 || b.bytes != 0 {
		t.Fatal("cancelled batch retained values")
	}
	if _, ok := c.read("pending"); ok || c.index != nil {
		t.Fatal("cancelled tail opened or published metadata")
	}
}

func TestLRUDirPublicationBatchCapacityAndRepeatedKeys(t *testing.T) {
	for _, capacity := range []int{0, 1, 3} {
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
			l := newProcessCache(t, capacity)
			l.writeBatch([]cachePublication{{"a", []byte("first")}, {"b", []byte("b")}, {"a", []byte("last")}, {"c", []byte("c")}, {"d", []byte("d")}})
			var want []string
			switch capacity {
			case 1:
				want = []string{"d"}
			case 3:
				want = []string{"a", "c", "d"}
			}
			if got := l.names(); !slices.Equal(got, want) {
				t.Fatalf("batch capacity = %v, want %v", got, want)
			}
			if capacity == 3 {
				if data, ok := l.read("a"); !ok || string(data) != "last" {
					t.Fatalf("repeated key = %q, %v", data, ok)
				}
				l.remove("a")
				l.writeBatch([]cachePublication{{"e", []byte("e")}, {"f", []byte("f")}})
				if got := l.names(); !slices.Equal(got, []string{"d", "e", "f"}) {
					t.Fatalf("free-slot batch = %v", got)
				}
			}
			files, err := filepath.Glob(filepath.Join(l.dir, "slot-*"))
			if err != nil || len(files) > capacity {
				t.Fatalf("physical slots = %v, %v, capacity %d", files, err, capacity)
			}
		})
	}
}

func TestLRUDirPublicationBatchRetainsValidPrefixAfterRenameFailure(t *testing.T) {
	l := newProcessCache(t, 3)
	for _, name := range []string{"a", "b", "c"} {
		data := []byte("old " + name)
		if name == "b" {
			data = bytes.Repeat(data, cacheChunkSize)
		}
		l.write(name, data)
	}
	l.touch("c")
	blocked := cacheEntryPath(t, l, "b")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	l.writeBatch([]cachePublication{{"a", []byte("new a")}, {"b", bytes.Repeat([]byte("new b"), cacheChunkSize)}})
	if data, ok := l.read("a"); !ok || string(data) != "new a" {
		t.Fatalf("valid prefix = %q, %v", data, ok)
	}
	if data, ok := l.read("b"); ok {
		t.Fatalf("failed rename exposed bytes %q", data)
	}
	if data, ok := l.read("c"); !ok || string(data) != "old c" {
		t.Fatalf("untouched key = %q, %v", data, ok)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	l.writeBatch([]cachePublication{{"a", []byte("repaired a")}, {"b", []byte("repaired b")}})
	for _, name := range []string{"a", "b"} {
		if data, ok := l.read(name); !ok || string(data) != "repaired "+name {
			t.Fatalf("repaired %s = %q, %v", name, data, ok)
		}
	}
}

func reopenColdPublicationStore(t *testing.T, s *Store) *Store {
	t.Helper()
	dir, cacheDir := s.Git.Dir, s.cache.dir
	queryDir := filepath.Join(s.CommonDir(), "cc-notes-query")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cacheDir, queryDir} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	return fresh
}

func TestLRUDirFoldAllPublishesTailAfterWait(t *testing.T) {
	s := initStore(t)
	heads := make([]model.SHA, 0, 3)
	for i := range 3 {
		note := create(t, s, noteOps(fmt.Sprintf("tail %d", i))).(model.Note)
		heads = append(heads, note.Head)
	}
	s = reopenColdPublicationStore(t, s)
	notes, err := s.ListNotes(t.Context(), false, false)
	if err != nil || len(notes) != len(heads) {
		t.Fatalf("cold list = %d, %v", len(notes), err)
	}
	for _, head := range heads {
		if _, ok := s.cache.get(head); !ok {
			t.Fatalf("successful worker tail was not published: %s", head)
		}
	}
}

func TestLRUDirQueryRefreshPublishesUnselectedTail(t *testing.T) {
	s := initStore(t)
	heads := make([]model.SHA, 0, 3)
	for i := range 3 {
		label := "other"
		if i == 0 {
			label = "target"
		}
		task := create(t, s, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("tail %d", i), Type: model.TypeTask, Labels: []string{label}}}).(model.Task)
		heads = append(heads, task.Head)
	}
	s = reopenColdPublicationStore(t, s)
	tasks, hit, err := s.IndexedTasks(t.Context(), []string{"target"})
	if err != nil || !hit || len(tasks) != 1 {
		t.Fatalf("cold query = %d, %v, %v", len(tasks), hit, err)
	}
	for _, head := range heads {
		if _, ok := s.cache.get(head); !ok {
			t.Fatalf("query source tail was not published: %s", head)
		}
	}
}

func TestLRUDirPointLoadAtStillPublishesImmediately(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("point")).(model.Note)
	s = reopenColdPublicationStore(t, s)
	if _, err := s.LoadAt(t.Context(), note.Head); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.cache.get(note.Head); !ok {
		t.Fatal("point LoadAt delayed publication")
	}
}
