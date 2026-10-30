package gitobj

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func nativeLargeBlobFixture(t *testing.T) (*Repo, plumbing.Hash, int64) {
	t.Helper()
	dir, _, _ := indexedCommitFixture(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<20)
	if err := os.WriteFile(filepath.Join(dir, "large"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "large")
	gittest.Git(t, dir, "commit", "-qm", "large")
	hash := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD:large"))
	gittest.Git(t, dir, "repack", "-adq")
	return openPackedRepo(t, dir), hash, int64(len(data))
}

func TestNativeLargeBlobStreamSurvivesReadScopeRelease(t *testing.T) {
	repo, hash, size := nativeLargeBlobFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(ctx)
	encoded, err := repo.storage.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := encoded.(*nativeObject); !ok || encoded.Size() != size {
		t.Fatalf("large object = %T size%d", encoded, encoded.Size())
	}
	stream, err := encoded.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Error(err)
		}
	}()
	prefix := make([]byte, 17)
	if _, err := io.ReadFull(stream, prefix); err != nil || string(prefix) != "0123456789abcdef0" {
		t.Fatalf("prefix=%q, %v", prefix, err)
	}
	ended := make(chan error, 1)
	go func() { var err error; repo.endRead(&err); ended <- err }()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-ended
		t.Fatal("scope release waited for the held stream")
	}
	if _, err := encoded.Reader(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("new stream after scope release=%v", err)
	}
	hasher := plumbing.NewHasher(plumbing.BlobObject, size)
	_, _ = hasher.Write(prefix)
	if n, err := io.Copy(hasher, stream); err != nil || n != size-int64(len(prefix)) {
		t.Fatalf("remaining stream=%d, %v", n, err)
	}
	if hasher.Sum() != hash {
		t.Fatal("held stream contents changed across scope release")
	}
}

func TestNativePackedReadCancellationLeavesNextScopeUsable(t *testing.T) {
	repo, hash, _ := nativeLargeBlobFixture(t)
	func() {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		repo.mu.Lock()
		defer repo.mu.Unlock()
		repo.beginRead(ctx)
		defer func() {
			var err error
			repo.endRead(&err)
			if err != nil {
				t.Error(err)
			}
		}()
		encoded, err := repo.storage.EncodedObject(plumbing.BlobObject, hash)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := encoded.Reader()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := stream.Close(); err != nil {
				t.Error(err)
			}
		}()
		cancel()
		if _, err := stream.Read(make([]byte, 17)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stream=%v", err)
		}
	}()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(t.Context())
	defer func() {
		var err error
		repo.endRead(&err)
		if err != nil {
			t.Error(err)
		}
	}()
	encoded, err := repo.storage.EncodedObject(plumbing.BlobObject, hash)
	if err != nil || encoded.Size() != 16<<20 {
		t.Fatalf("fresh scope=%v, %v", encoded, err)
	}
}

func TestNativeSimultaneousStreamsRemainIndependentAfterScopeRelease(t *testing.T) {
	repo, hash, size := nativeLargeBlobFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(ctx)
	encoded, err := repo.storage.EncodedObject(plumbing.BlobObject, hash)
	if err != nil {
		t.Fatal(err)
	}
	first, err := encoded.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	}()
	second, err := encoded.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()
	firstPrefix, secondPrefix := make([]byte, 17), make([]byte, 17)
	if _, err := io.ReadFull(first, firstPrefix); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(second, secondPrefix); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstPrefix, secondPrefix) {
		t.Fatal("simultaneous streams returned different prefixes")
	}
	if len(repo.storage.scope.batches) != 2 {
		t.Fatal("simultaneous readers did not own distinct batches")
	}
	released := make(chan error, 1)
	go func() { var err error; repo.endRead(&err); released <- err }()
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-released
		t.Fatal("scope release waited for simultaneous held streams")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	hasher := plumbing.NewHasher(plumbing.BlobObject, size)
	_, _ = hasher.Write(secondPrefix)
	if n, err := io.Copy(hasher, second); err != nil || n != size-int64(len(secondPrefix)) {
		t.Fatalf("second stream after independent first Close=%d, %v", n, err)
	}
	if hasher.Sum() != hash {
		t.Fatal("second stream changed after first reader released its child")
	}
}
