package gitobj

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestPackIndexWriterTruncatesWALPastOverlappingReader(t *testing.T) {
	dir, commonDir, _ := indexedCommitFixture(t)
	first, _ := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	keeper, err := first.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := keeper.Close(); err != nil {
			t.Error(err)
		}
	})
	var packs int
	if err := keeper.QueryRowContext(t.Context(), "SELECT count(*) FROM packs").Scan(&packs); err != nil {
		t.Fatal(err)
	}
	for round := range 3 {
		gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "next")
		next := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
		gittest.Git(t, dir, "repack", "-dq")
		reader := observeIndexedData(t, first)
		tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM packs").Scan(&packs); err != nil {
			t.Fatal(err)
		}
		released := make(chan error, 1)
		time.AfterFunc(100*time.Millisecond, func() { released <- tx.Rollback() })
		writer, _ := testPackIndex(t, commonDir)
		writer.walLimit = 1
		err = writer.prepare(t.Context())
		if releaseErr := <-released; releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err != nil {
			t.Fatalf("round %d writer beside an overlapping reader: %v", round, err)
		}
		info, err := os.Stat(writer.path + "-wal")
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			t.Fatalf("round %d WAL after publication = %d bytes, want 0", round, info.Size())
		}
		assertIndexedCommit(t, writer, next)
	}
}

func TestPackIndexInterruptedReconcileKeepsCommittedPacks(t *testing.T) {
	dir, commonDir, first := indexedCommitFixture(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-dq")
	index, _ := testPackIndex(t, commonDir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var opened []string
	index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(path string) {
		opened = append(opened, path)
		if len(opened) == 2 {
			cancel()
		}
	}})
	if err := index.prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare cancelled on the second pack = %v, want Canceled", err)
	}
	if len(opened) != 2 || index.ready {
		t.Fatalf("opened indexes = %v, ready = %v; want two and false", opened, index.ready)
	}
	fresh, reads := testPackIndex(t, commonDir)
	if err := fresh.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[opened[1]] == 0 {
		t.Fatalf("resumed reconcile reads = %v, want only the interrupted %s", reads, opened[1])
	}
	assertIndexedCommit(t, fresh, first)
	assertIndexedCommit(t, fresh, second)
}
