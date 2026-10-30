package store

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
)

func TestLRUDirMissingUntilDeferredFirstBatch(t *testing.T) {
	c := newFoldCache(t.TempDir(), cachePublicationRows+1)
	t.Cleanup(func() { _ = c.close() })
	b := newFoldPublicationBatch(t.Context(), c)
	defer b.discard()
	for i := range cachePublicationRows - 1 {
		name := strconv.Itoa(i)
		if _, ok := c.read(name); ok {
			t.Fatal("unpublished entry became a hit")
		}
		b.add(&cachePublication{name: name, data: []byte("payload")})
	}
	if !c.lookupMissing || c.db != nil || c.active {
		t.Fatalf("deferred state: missing=%v database=%v active=%v", c.lookupMissing, c.db, c.active)
	}
	b.add(&cachePublication{name: "last", data: []byte("last")})
	if c.lookupMissing || c.db == nil || !c.active {
		t.Fatalf("published state: missing=%v database=%v active=%v", c.lookupMissing, c.db, c.active)
	}
	if data, ok := c.read("0"); !ok || string(data) != "payload" {
		t.Fatalf("first payload = %q, %v", data, ok)
	}
	if data, ok := c.read("last"); !ok || string(data) != "last" {
		t.Fatalf("last payload = %q, %v", data, ok)
	}
}

func TestLRUDirMissingOtherProcessAndRefresh(t *testing.T) {
	for _, refresh := range []string{"enumerate", "gc"} {
		t.Run(refresh, func(t *testing.T) {
			l := newProcessCache(t, 2)
			if _, ok := l.read("remote"); ok || !l.lookupMissing {
				t.Fatal("initial absence was not recorded")
			}
			if err := os.RemoveAll(l.dir); err != nil {
				t.Fatal(err)
			}
			runCacheProcess(t, cacheProcessRequest{Mode: "write", Dir: l.dir, Capacity: l.capacity, Name: "remote", Data: "remote payload"}, 0)
			if _, ok := l.read("remote"); ok {
				t.Fatal("known-absent owner refreshed during point lookup")
			}
			fresh := &lruDir{capacity: l.capacity, dir: l.dir}
			t.Cleanup(func() { _ = fresh.close() })
			if data, ok := fresh.read("remote"); !ok || string(data) != "remote payload" {
				t.Fatalf("fresh owner = %q, %v", data, ok)
			}
			if refresh == "enumerate" {
				if got := l.names(); !slices.Equal(got, []string{"remote"}) {
					t.Fatalf("refreshed entries = %v", got)
				}
			} else {
				l.remove("absent")
			}
			if data, ok := l.read("remote"); !ok || string(data) != "remote payload" || l.lookupMissing {
				t.Fatalf("refreshed owner = %q, %v, missing=%v", data, ok, l.lookupMissing)
			}
		})
	}
}

func TestLRUDirMissingPreservesOtherErrorsAndClose(t *testing.T) {
	l := newProcessCache(t, 1)
	index := filepath.Join(l.dir, "index.sqlite")
	if err := os.Symlink("index.sqlite", index); err != nil {
		t.Fatal(err)
	}
	if _, err := l.database(cacheLookup); err == nil || errors.Is(err, os.ErrNotExist) || l.lookupMissing {
		t.Fatalf("non-absence error = %v, missing=%v", err, l.lookupMissing)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if _, err := l.database(cacheLookup); !errors.Is(err, os.ErrNotExist) || !l.lookupMissing {
		t.Fatalf("absence error = %v, missing=%v", err, l.lookupMissing)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.database(cacheLookup); !errors.Is(err, sql.ErrConnDone) || l.lookupMissing {
		t.Fatalf("closed owner = %v, missing=%v", err, l.lookupMissing)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
}

func TestLRUDirMissingConcurrentWriterAndClose(t *testing.T) {
	l := newProcessCache(t, 1)
	data := bytes.Repeat([]byte("x"), cacheChunkSize+1)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 64 {
				if got, ok := l.read("value"); ok && !bytes.Equal(got, data) {
					t.Error("concurrent lookup returned another publication")
				}
			}
		})
	}
	wg.Go(func() { l.write("value", data) })
	wg.Wait()
	if got, ok := l.read("value"); !ok || !bytes.Equal(got, data) {
		t.Fatalf("writer did not clear absence: %d bytes, %v", len(got), ok)
	}
	reader, ok := l.open("value")
	if !ok {
		t.Fatal("published reader missing")
	}
	defer func() { _ = reader.Close() }()
	for range 8 {
		wg.Go(func() {
			for range 64 {
				if got, ok := l.read("value"); ok && !bytes.Equal(got, data) {
					t.Error("closing lookup returned another publication")
				}
			}
		})
	}
	wg.Go(func() {
		if err := l.close(); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	if _, err := l.database(cacheWrite); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("closed writer = %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("held reader after Close = %d bytes, %v", len(got), err)
	}
}
