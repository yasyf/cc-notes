package gitobj

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func indexedDataVersion(t *testing.T, conn *sql.Conn) int {
	t.Helper()
	var version int
	if err := conn.QueryRowContext(t.Context(), "PRAGMA data_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func observeIndexedData(t *testing.T, index *packIndex) *sql.Conn {
	t.Helper()
	db, err := index.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return conn
}

func TestPackIndexDuplicateObjectSurvivesSelectedPackRemoval(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	gittest.Git(t, dir, "repack", "-aq")
	index, _ := testPackIndex(t, commonDir)
	if err := index.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := index.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var copies int
	err = tx.QueryRowContext(t.Context(), "SELECT count(*) FROM objects WHERE hash = ?", hash[:]).Scan(&copies)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatal(err)
	}
	if copies != 2 {
		t.Errorf("indexed copies = %d, want 2", copies)
	}
	selected, _, err := locateIndexedPack(t.Context(), tx, hash)
	if err := errors.Join(err, tx.Rollback(), db.Close()); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"pack", "idx"} {
		if err := os.Remove(filepath.Join(commonDir, packDir, "pack-"+selected.String()+"."+ext)); err != nil {
			t.Fatal(err)
		}
	}
	fresh, _ := testPackIndex(t, commonDir)
	if err := fresh.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndexedCommit(t, fresh, hash)
}

func TestPackIndexMaintenanceAliasesKeepOneLogicalGeneration(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	first, reads := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	observer := observeIndexedData(t, first)
	before := indexedDataVersion(t, observer)
	for path := range reads {
		base := filepath.Base(path)
		stem := base[len(packPrefix) : len(base)-len(packIndexExt)]
		for _, ext := range []string{"pack", "idx"} {
			raw, err := os.ReadFile(filepath.Join(commonDir, packDir, packPrefix+stem+"."+ext))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(commonDir, packDir, loosePackPrefix+stem+"."+ext), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for attempt := range 2 {
		fresh, reads := testPackIndex(t, commonDir)
		if err := fresh.prepare(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertIndexedCommit(t, fresh, hash)
		if len(reads) != 0 {
			t.Errorf("attempt %d reread indexes %v, want no payload reads", attempt, reads)
		}
		if got := indexedDataVersion(t, observer); got != before {
			t.Errorf("attempt %d data version = %d, want unchanged %d", attempt, got, before)
		}
	}
}

func TestPackIndexIncompleteGenerationReusesValidatedPacks(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	first, _ := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := first.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), "DELETE FROM metadata WHERE key = 'generation'")
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	fresh, reads := testPackIndex(t, commonDir)
	if err := fresh.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndexedCommit(t, fresh, hash)
	if len(reads) != 0 {
		t.Fatalf("recovery reread indexes %v, want reuse of validated packs", reads)
	}
	observer := observeIndexedData(t, fresh)
	last := indexedDataVersion(t, observer)
	complete, _ := testPackIndex(t, commonDir)
	if err := complete.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := indexedDataVersion(t, observer); got != last {
		t.Errorf("recovered generation data version = %d, want stable %d", got, last)
	}
}

func TestIndexedRepoReadsPackedAlternateCommit(t *testing.T) {
	_, alternateDir, hash := indexedCommitFixture(t)
	dir := gittest.InitRepo(t)
	gitDir, commonDir := gittest.Dirs(t, dir)
	info := filepath.Join(commonDir, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(alternateDir, "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gittest.Git(t, dir, "cat-file", "-t", hash.String()); got != "commit" {
		t.Fatalf("git alternate object type = %q, want commit", got)
	}
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	sha := model.SHA(hash.String())
	if got, err := repo.Ancestry(t.Context(), sha, sha); err != nil || got != Ancestor {
		t.Fatalf("alternate Ancestry = %v, %v; want Ancestor", got, err)
	}
}

type countedPackOpenFS struct {
	billy.Filesystem
	opens *int
}

func (fs countedPackOpenFS) Open(path string) (billy.File, error) {
	file, err := fs.Filesystem.Open(path)
	if err == nil && filepath.Ext(path) == ".pack" {
		*fs.opens++
	}
	return file, err
}

func TestIndexedRepoFreshAncestryReusesIndexAndPackReads(t *testing.T) {
	dir, commonDir, oldest := indexedCommitFixture(t)
	for range 32 {
		gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "next")
	}
	tip := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-adq")
	gitDir, _ := gittest.Dirs(t, dir)
	for attempt := range 2 {
		repo, err := Open(gitDir, commonDir)
		if err != nil {
			t.Fatal(err)
		}
		index, reads := testPackIndex(t, commonDir)
		opens := 0
		index.dir = dotgit.New(countedPackOpenFS{Filesystem: index.dir.Fs(), opens: &opens})
		repo.storage.index = index
		if got, err := repo.Ancestry(t.Context(), model.SHA(oldest.String()), tip); err != nil || got != Ancestor {
			t.Fatalf("Ancestry attempt %d = %v, %v; want Ancestor", attempt, got, err)
		}
		if attempt == 0 && len(reads) != 1 {
			t.Errorf("initial Ancestry index reads = %v, want exactly one", reads)
		}
		if attempt == 1 && len(reads) != 0 {
			t.Errorf("fresh Ancestry index reads = %v, want zero", reads)
		}
		if opens != 1 {
			t.Errorf("Ancestry attempt %d pack opens = %d, want 1 within the operation", attempt, opens)
		}
	}
}

func TestIndexedGenerationWriterDoesNotWaitForExistingReadScope(t *testing.T) {
	dir, commonDir, first := indexedCommitFixture(t)
	gitDir, _ := gittest.Dirs(t, dir)
	reader, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.beginRead(t.Context())
	closed := false
	closeReader := func() {
		if closed {
			return
		}
		closed = true
		var closeErr error
		reader.endRead(&closeErr)
		if closeErr != nil {
			t.Error(closeErr)
		}
	}
	defer closeReader()
	if _, err := reader.storage.EncodedObject(plumbing.CommitObject, first); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-dq")
	writer, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		verdict Ancestry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		verdict, err := writer.Ancestry(ctx, model.SHA(first.String()), second)
		done <- result{verdict: verdict, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.verdict != Ancestor {
			t.Fatalf("writer beside live read scope = %v, %v; want Ancestor before reader closes", got.verdict, got.err)
		}
	case <-ctx.Done():
		closeReader()
		<-done
		t.Fatal("changed-pack writer waited for the existing read scope")
	}
	object, err := reader.storage.EncodedObject(plumbing.CommitObject, first)
	if err != nil || object.Hash() != first {
		t.Fatalf("original read scope after publication = %v, %v; want commit %s", object, err, first)
	}
}
