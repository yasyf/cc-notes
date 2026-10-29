package gitobj

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gittest"
)

type cancelIndexFS struct {
	billy.Filesystem
	cancel context.CancelFunc
}

func (fs cancelIndexFS) Open(path string) (billy.File, error) {
	file, err := fs.Filesystem.Open(path)
	if err != nil || filepath.Ext(path) != ".idx" {
		return file, err
	}
	return cancelIndexFile{File: file, cancel: fs.cancel}, nil
}

type cancelIndexFile struct {
	billy.File
	cancel context.CancelFunc
}

func (f cancelIndexFile) Read(data []byte) (int, error) {
	n, err := f.File.Read(data)
	if n > 0 {
		f.cancel()
	}
	return n, err
}

func TestPackIndexCancellationDoesNotPublishPartialGeneration(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	index, _ := testPackIndex(t, commonDir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	index.dir = dotgit.New(cancelIndexFS{Filesystem: index.dir.Fs(), cancel: cancel})
	if err := index.prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare cancelled during decode = %v, want Canceled", err)
	}
	if index.ready {
		t.Fatal("cancelled generation became ready")
	}
	db, err := index.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'metadata'").Scan(&count)
	if count != 0 {
		t.Error("cancelled generation was published")
	}
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	fresh, _ := testPackIndex(t, commonDir)
	if err := fresh.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndexedCommit(t, fresh, hash)
}

func TestPackIndexWriterWaitHonorsCancellation(t *testing.T) {
	_, commonDir, _ := indexedCommitFixture(t)
	index, _ := testPackIndex(t, commonDir)
	if err := index.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := index.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer, err := index.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	err = index.reconcile(ctx, writer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcile cancelled while locked = %v, want Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled writer took %v, want under one second", elapsed)
	}
}

func TestIndexedLargeBlobRemainsFilesystemBacked(t *testing.T) {
	dir, commonDir, _ := indexedCommitFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "large"), bytes.Repeat([]byte("0123456789abcdef"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "large")
	gittest.Git(t, dir, "commit", "-qm", "large")
	hash := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD:large"))
	gittest.Git(t, dir, "repack", "-adq")
	gitDir, _ := gittest.Dirs(t, dir)
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(t.Context())
	defer func() {
		var closeErr error
		repo.endRead(&closeErr)
		if closeErr != nil {
			t.Error(closeErr)
		}
	}()
	object, err := repo.storage.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := object.(*packfile.FSObject); !ok {
		t.Fatalf("large blob = %T, want filesystem-backed object", object)
	}
	if got := object.Size(); got != 16<<20 {
		t.Fatalf("large blob size = %d, want %d", got, 16<<20)
	}
}
