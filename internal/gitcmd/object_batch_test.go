package gitcmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func objectBatchFixture(t *testing.T) (Git, plumbing.Hash, []byte) {
	t.Helper()
	dir := gittest.InitRepo(t)
	body := bytes.Repeat([]byte("0123456789abcdef"), 1<<20)
	path := filepath.Join(dir, "body")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := plumbing.NewHash(gittest.Git(t, dir, "hash-object", "-w", path))
	_, common := gittest.Dirs(t, dir)
	return Backend(common, dir), hash, body
}

func TestObjectBatchStreamsAndReusesOwnedChild(t *testing.T) {
	git, hash, want := objectBatchFixture(t)
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := batch.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		info, err := batch.Info(hash)
		if err != nil || info.Type != plumbing.BlobObject || info.Size != int64(len(want)) {
			t.Fatalf("Info = %v, %v", info, err)
		}
		reader, err := batch.Contents(info)
		if err != nil {
			t.Fatal(err)
		}
		var actual bytes.Buffer
		if _, err := io.Copy(&actual, reader); err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual.Bytes(), want) || !batch.Idle() {
			t.Fatal("stream differs or batch was not retained for reuse")
		}
		select {
		case <-batch.done:
			t.Fatal("batch child exited before owner release")
		default:
		}
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if batch.cmd.ProcessState == nil || !batch.cmd.ProcessState.Success() {
		t.Fatal("completed child was not reaped successfully")
	}
}

func TestObjectBatchPartialStreamCloseReapsChild(t *testing.T) {
	git, hash, want := objectBatchFixture(t)
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	info, err := batch.Info(hash)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := batch.Contents(info)
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 17)
	if _, err := io.ReadFull(reader, prefix); err != nil || !bytes.Equal(prefix, want[:len(prefix)]) {
		t.Fatalf("prefix = %q, %v", prefix, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if batch.cmd.ProcessState == nil || batch.Idle() {
		t.Fatal("partial stream child was not reaped")
	}
}

func TestObjectBatchCancellationReapsChild(t *testing.T) {
	git, hash, _ := objectBatchFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	batch, err := git.ObjectBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	cancel()
	if _, err := batch.Info(hash); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Info = %v", err)
	}
	if batch.cmd.ProcessState == nil {
		t.Fatal("cancelled child was not reaped")
	}
}

func TestObjectBatchMissingLeavesProtocolReusable(t *testing.T) {
	git, hash, _ := objectBatchFixture(t)
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	if _, err := batch.Info(plumbing.ZeroHash); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("missing Info = %v", err)
	}
	if _, err := batch.Info(hash); err != nil {
		t.Fatalf("Info after miss: %v", err)
	}
}

func TestObjectBatchReleaseTransfersActiveStreamOwnership(t *testing.T) {
	git, hash, want := objectBatchFixture(t)
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	info, err := batch.Info(hash)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := batch.Contents(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Release(); err != nil {
		t.Fatal(err)
	}
	if batch.Idle() {
		t.Fatal("released active stream became reusable")
	}
	var actual bytes.Buffer
	if _, err := io.Copy(&actual, stream); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual.Bytes(), want) {
		t.Fatal("stream changed across owner release")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if batch.cmd.ProcessState == nil || !batch.cmd.ProcessState.Success() {
		t.Fatal("transferred stream did not reap completed child")
	}
}

func TestObjectBatchUsesSelectedRawObjectDespiteRoutingAndReplacement(t *testing.T) {
	git, hash, want := objectBatchFixture(t)
	replacement := filepath.Join(git.workDir, "replacement")
	if err := os.WriteFile(replacement, []byte("replacement body"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacementHash := gittest.Git(t, git.workDir, "hash-object", "-w", replacement)
	gittest.Git(t, git.workDir, "replace", hash.String(), replacementHash)
	decoy := gittest.InitRepo(t)
	_, decoyCommon := gittest.Dirs(t, decoy)
	t.Setenv("GIT_DIR", decoyCommon)
	t.Setenv("GIT_COMMON_DIR", decoyCommon)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoyCommon, "objects"))
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	info, err := batch.Info(hash)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := batch.Contents(info)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("object routing or replacement changed selected raw bytes")
	}
}

type changedObjectBody struct {
	source  io.Reader
	header  bool
	changed bool
}

func (r *changedObjectBody) Read(data []byte) (int, error) {
	n, err := r.source.Read(data)
	for i := range n {
		if r.changed {
			break
		}
		if r.header {
			data[i] ^= 1
			r.changed = true
			break
		}
		if data[i] == '\n' {
			r.header = true
		}
	}
	return n, err
}

func TestObjectBatchRejectsReturnedContentCorruption(t *testing.T) {
	git, hash, _ := objectBatchFixture(t)
	batch, err := git.ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	info, err := batch.Info(hash)
	if err != nil {
		t.Fatal(err)
	}
	changed := &changedObjectBody{source: batch.output}
	batch.output = bufio.NewReader(changed)
	stream, err := batch.Contents(info)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, stream)
	if !changed.changed || err == nil || !strings.Contains(err.Error(), "content hash mismatch") || errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("corrupted returned bytes=%v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if batch.cmd.ProcessState == nil {
		t.Fatal("corrupt content child was not reaped")
	}
}

func TestObjectBatchReleaseOverlapsCallerCancellationAndReapsBothChildren(t *testing.T) {
	git, hash, _ := objectBatchFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	batches := make([]*ObjectBatch, 0, 2)
	streams := make([]io.ReadCloser, 0, 2)
	for range 2 {
		batch, err := git.ObjectBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = batch.Close() })
		info, err := batch.Info(hash)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := batch.Contents(info)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(stream, make([]byte, 17)); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, batch)
		streams = append(streams, stream)
	}
	if batches[0].cmd.Process.Pid == batches[1].cmd.Process.Pid {
		t.Fatal("independent streams shared one child")
	}
	start := make(chan struct{})
	released := make(chan error, 1)
	go func() { <-start; released <- errors.Join(batches[0].Release(), batches[1].Release()) }()
	close(start)
	cancel()
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	for i, batch := range batches {
		<-batch.done
		if batch.cmd.ProcessState == nil {
			t.Fatalf("child%d was not reaped on caller cancellation", i)
		}
		if err := streams[i].Close(); err != nil {
			t.Fatal(err)
		}
		if err := streams[i].Close(); err != nil {
			t.Fatal(err)
		}
		if err := batch.Close(); err != nil {
			t.Fatal(err)
		}
		if err := batch.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestObjectBatchPromisorMissRemainsLocalAndReusable(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "--allow-empty", "-qm", "one")
	gittest.Git(t, source, "config", "uploadpack.allowFilter", "true")
	thin := t.TempDir()
	gittest.Git(t, filepath.Dir(thin), "clone", "-q", "--no-local", "--filter=blob:none", "file://"+source, thin)
	head := plumbing.NewHash(gittest.Git(t, thin, "rev-parse", "HEAD"))
	gittest.Git(t, source, "commit", "--allow-empty", "-qm", "not fetched")
	missing := plumbing.NewHash(gittest.Git(t, source, "rev-parse", "HEAD"))
	if gittest.Git(t, thin, "config", "--get", "remote.origin.promisor") != "true" {
		t.Fatal("fixture is not promisor-backed")
	}
	trace := filepath.Join(t.TempDir(), "git-trace")
	t.Setenv("GIT_TRACE", trace)
	batch, err := Backend(filepath.Join(thin, ".git"), thin).ObjectBatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = batch.Close() })
	if _, err := batch.Info(missing); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("promisor miss=%v, want local missing", err)
	}
	if _, err := batch.Info(head); err != nil {
		t.Fatalf("present object after miss=%v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("local missing emitted diagnostic or leaked child: %v", err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), " fetch ") {
		t.Fatalf("native reader attempted promisor fetch: %s", data)
	}
}
